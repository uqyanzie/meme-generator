# TKT-105 Fix Plan: Duplicate Checkout / Double Billing Creation

- **Ticket:** [TKT-105](./TKT-105.md)
- **Layer:** Frontend + Backend (+ DB Migration)
- **Status:** Planned (Pending Review)

---

## 1. Root Cause Analysis

Four interacting defects across the frontend, HTTP middleware, and backend subscription module cause duplicate pending checkouts and broken/flaky checkout creation under slow networks or concurrent requests:

1. **Per-click `Idempotency-Key` generation (`web/src/lib/queries.ts:57-74`):**
   - `useCheckout()` generates `"Idempotency-Key": crypto.randomUUID()` inside `mutationFn` on every invocation. Rapid clicks or retries for the same plan send distinct UUIDs, bypassing backend idempotency.
2. **No in-flight submission guard in Pricing UI (`web/src/app/pricing/page.tsx:64-74`):**
   - The `"Berlangganan"` `<Button>` does not disable while `checkout.isPending` (or while navigating to `/checkout/{reference}`), nor does it show a loading state. Clicking twice under slow network immediately fires two concurrent `POST /api/v1/subscription/checkout` mutations.
3. **Backend TOCTOU race + lack of cross-refresh pending deduplication (`api/internal/modules/subscription/service.go:39-64`, `api/internal/modules/subscription/repository.go:57-121`):**
   - `Service.Checkout` performs a non-atomic `CheckoutByIdempotencyKey` lookup before calling `CreateSubscription`. When concurrent requests arrive with the **same** `Idempotency-Key`, multiple goroutines pass the check and race into `INSERT INTO payments ...`, hitting `payments_idempotency_key_idx` (PostgreSQL SQLSTATE `23505`) and returning **HTTP 500** instead of replaying the winning checkout.
   - `CheckoutByIdempotencyKey` does not filter by `s.user_id`, leaving a theoretical cross-user replay gap if a key ever collided.
   - There is no pending checkout deduplication by `(user_id, plan_id)`, so a user who refreshes `/pricing` or opens a second tab and submits a new `Idempotency-Key` creates multiple unpaid pending subscriptions and payments for the same package.
4. **CORS preflight rejects `Idempotency-Key` (`api/internal/middleware/cors.go:29-30`):**
   - Discovered during live browser reproduction: `Access-Control-Allow-Headers` is set to `"Content-Type, Authorization, X-Requested-With"` and omits `Idempotency-Key`. Cross-origin browser checkouts from `http://localhost:3000` to `http://localhost:8080` fail CORS preflight with `net::ERR_FAILED`.

---

## 2. Before Reproduction Evidence

- **Backend API & Postgres ([evidence/before-api-repro.txt](./evidence/before-api-repro.txt)):**
  - *Scenario A (different `Idempotency-Key` for same user & `premium_1d`):* Both requests returned `201 Created` with different subscription IDs (`feffc24d-...`, `3ba26d5a-...`) and payment references (`4cbee115...`, `88d0a7bd...`), creating 2 pending rows in `subscriptions` and `payments`.
  - *Scenario B (10 synchronized concurrent requests with identical `Idempotency-Key`):* 6 returned `201 Created` (same reference `f7fd82f3...`) while 4 failed with `HTTP 500` (`code: 50000`, `"internal server error"`).
- **Live Browser ([evidence/before-browser-repro.txt](./evidence/before-browser-repro.txt)):**
  - Under `Slow 3G` throttling on `/pricing`, clicking `"Berlangganan"` twice 150ms apart kept `disabled: false` and label `"Berlangganan"` throughout, firing 2 `POST /api/v1/subscription/checkout` requests with 2 distinct `Idempotency-Key` headers (`c8e80979-...` and `4405575d-...`).
  - Both browser requests were also blocked at CORS preflight because `Access-Control-Allow-Headers` lacked `Idempotency-Key`.

---

## 3. Agreed Decisions

- **Cross-refresh pending checkout deduplication (Resolved):** Reuse an existing pending checkout for the same `(user_id, plan_id)` on the backend even when the user refreshes the page or uses another tab with a new `Idempotency-Key`, backed by a partial unique index for concurrency safety.

---

## 4. Minimal Fix Design

### 4.1 Database Migration
- **`api/migrations/000006_pending_subscription_unique.up.sql`**
  - Clean up any pre-existing duplicate pending rows per `(user_id, plan_id)` (retaining the earliest row by `created_at`; `payments` cascades via `ON DELETE CASCADE`).
  - Create partial unique index:
    ```sql
    CREATE UNIQUE INDEX IF NOT EXISTS subscriptions_user_plan_pending_idx
      ON subscriptions (user_id, plan_id)
      WHERE status = 'pending';
    ```
- **`api/migrations/000006_pending_subscription_unique.down.sql`**
  - `DROP INDEX IF EXISTS subscriptions_user_plan_pending_idx;`

### 4.2 Backend CORS Middleware
- **`api/internal/middleware/cors.go` (L29-L30)**
  - Add `Idempotency-Key` to `Access-Control-Allow-Headers`:
    `"Content-Type, Authorization, X-Requested-With, Idempotency-Key"`.

### 4.3 Backend Subscription Repository & Service
- **`api/internal/modules/subscription/repository.go`**
  - Update `CheckoutByIdempotencyKey(ctx, userID, key)` to filter by `WHERE p.idempotency_key = $1 AND s.user_id = $2`.
  - Add `PendingCheckoutByUserAndPlan(ctx, userID, planID) (CheckoutResponse, bool, error)` querying `payments p JOIN subscriptions s ON s.id = p.subscription_id WHERE s.user_id = $1 AND s.plan_id = $2 AND s.status = 'pending' AND p.status = 'pending' ORDER BY s.created_at ASC LIMIT 1` (populating `MockGatewayURL`).
  - Add helper `isUniqueViolation(err error) bool` checking `*pgconn.PgError` with `Code == "23505"`.
- **`api/internal/modules/subscription/service.go`**
  - In `Service.Checkout(ctx, userID, dto, idempotencyKey)`:
    1. Replay via `CheckoutByIdempotencyKey(ctx, userID, idempotencyKey)` if found.
    2. Validate plan (`GetPlanByCode`) and active subscription (`ActiveByUser` → `ErrAlreadySubscribed`).
    3. Replay via `PendingCheckoutByUserAndPlan(ctx, userID, plan.ID)` if found.
    4. Call `CreateSubscription(...)`. On `isUniqueViolation(err)`, recover cleanly by re-querying `CheckoutByIdempotencyKey(ctx, userID, idempotencyKey)` and then `PendingCheckoutByUserAndPlan(ctx, userID, plan.ID)` and returning the winning `CheckoutResponse` (no `500`).
    5. Call `s.gateway.CreateCharge(...)` and return `CheckoutResponse`.

### 4.4 Frontend Query & Pricing UI
- **`web/src/lib/queries.ts` (L57-L74)**
  - Update `useCheckout()` so `mutationFn` accepts `{ planCode: string; idempotencyKey: string }` and forwards `idempotencyKey` in the `Idempotency-Key` header without generating a new UUID on every call.
- **`web/src/app/pricing/page.tsx` (L20-L81)**
  - Store one `Idempotency-Key` per `planCode` intent in `useRef<Record<string, string>>({})` (created lazily via `crypto.randomUUID()` on first click for a given plan, reused on any retry).
  - Track `isNavigating` state alongside `checkout.isPending` (and the clicked `planCode`) so buttons remain disabled from click until navigation to `/checkout/{reference}` finishes or an error occurs.
  - Guard `onClick` with `if (checkout.isPending || isNavigating) return;`.
  - Set `disabled={checkout.isPending || isNavigating}` on all `"Berlangganan"` buttons and show `"Memproses..."` on the actively submitting plan button.

---

## 5. Edge Cases & Regression Risks

- **Switching plans while a pending checkout exists:** The partial unique index is on `(user_id, plan_id) WHERE status = 'pending'`, so a user who starts checkout for `premium_1d`, abandons it, and chooses `premium_30d` can still create a checkout for `premium_30d`.
- **Re-subscribing to the same plan after expiration:** Once a previous subscription is `active`, `expired`, or `canceled`, its `status` is no longer `'pending'`, so the partial unique index allows a fresh checkout for the same plan.
- **Retry after transient network failure:** Because `isNavigating` only turns `true` inside `onSuccess` and `idempotencyKeysRef` retains the key for `planCode`, a failed request re-enables the button and retries with the exact same `Idempotency-Key`.
- **Cross-user `Idempotency-Key` isolation:** Scoping `CheckoutByIdempotencyKey` by `user_id` prevents returning another user's checkout record.
- **Scope boundary with TKT-106:** Do not modify `/checkout/[reference]/page.tsx` or `ConfirmPayment` (reserved for `TKT-106`).

---

## 6. Verification Plan

### 6.1 Automated Regression Tests (Red → Green)
- **`api/internal/modules/subscription/repository_test.go`** (with `-race`):
  1. `TestCheckout_ReplaysSameIdempotencyKeyConcurrently`: Launch 10 concurrent goroutines calling `svc.Checkout` with the identical `Idempotency-Key`; assert 0 errors, all 10 return the exact same `Subscription.ID` and `Payment.Reference`, and Postgres has exactly 1 subscription and 1 payment row.
  2. `TestCheckout_DeduplicatesPendingCheckoutAcrossNewIdempotencyKeys`: Call `svc.Checkout` sequentially and concurrently for the same `(user_id, planCode)` with *different* `Idempotency-Key` values; assert all return the same pending checkout and only 1 pending row exists in Postgres.
  3. `TestCheckout_AllowsDifferentPlanWhilePendingExists`: Verify a user with a pending `premium_1d` checkout can still create a checkout for `premium_30d`.
- **`api/internal/middleware/cors_test.go`**:
  1. `TestCORS_AllowsIdempotencyKeyHeader`: Verify `OPTIONS` preflight with `Access-Control-Request-Headers: content-type,idempotency-key` returns `204` with `Idempotency-Key` included in `Access-Control-Allow-Headers`.

### 6.2 Static Analysis, Test, Lint & Build Gates
- Backend: `cd api && go vet ./... && go test -race ./... && go build -o bin/api ./cmd/server`
- Frontend: `cd web && bun run check && bun run lint`

### 6.3 Live "After" Reproduction Steps (Mirroring Step 2)
1. Apply migration `000006` (`migrate -path api/migrations -database $dbUrl up`) and restart the Go API.
2. **Backend After check (`evidence/after-api-repro.txt`):**
   - Re-run Scenario A (two different `Idempotency-Key`s for same user & `premium_1d`): assert both return the **same** `subscription.id` and `payment.reference`, and Postgres shows exactly **1 row**.
   - Re-run Scenario B (10 synchronized concurrent requests with identical `Idempotency-Key`): assert all 10 return `HTTP 201` with the **same** reference and **0** `HTTP 500` errors.
3. **Browser After check (`evidence/after-browser-repro.txt`):**
   - On `http://localhost:3000/pricing` under `Slow 3G` throttling, rapidly click `"Berlangganan"` twice.
   - Assert button immediately becomes `disabled: true` with `"Memproses..."`, only **1** `POST /api/v1/subscription/checkout` request fires, CORS preflight succeeds (`204`), checkout returns `201 Created`, and browser navigates to `/checkout/{reference}`.
   - Navigate back to `/pricing`, refresh the page, click the same plan again, and verify the backend replays the **exact same** `payment.reference`.

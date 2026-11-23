# TKT-105 Fix Report: Duplicate Checkout / Double Billing Creation

- **Ticket:** [TKT-105](./TKT-105.md)
- **Fix Plan:** [TKT-105-plan.md](./TKT-105-plan.md)
- **Status:** Resolved

---

## Summary (Non-Engineer Friendly)

Previously, clicking the **"Berlangganan"** (Subscribe) button multiple times on a slow internet connection—or refreshing the pricing page and clicking the same plan again—could create duplicate unpaid checkout records in the database. In addition, rapid concurrent requests with the same idempotency key could fail with an internal server error (`500`), and browser checkouts were blocked by CORS preflight because `Idempotency-Key` was not an allowed header.

With this fix:
- Clicking **"Berlangganan"** immediately disables all plan buttons and shows **"Memproses..."** until navigation completes or an error occurs.
- Retrying a failed checkout for the same plan reuses the same `Idempotency-Key`.
- The backend safely replays concurrent requests with the same `Idempotency-Key` without `500` errors and reuses any existing pending checkout for the same user and plan across page refreshes or tabs.
- Cross-origin browser preflight (`OPTIONS`) now allows the `Idempotency-Key` header.

---

## Root Cause (Technical)

1. **Per-click `Idempotency-Key` generation (`web/src/lib/queries.ts`):** `useCheckout()` generated a new `crypto.randomUUID()` inside `mutationFn` on every call.
2. **No in-flight UI guard (`web/src/app/pricing/page.tsx`):** The `"Berlangganan"` `<Button>` stayed enabled while `checkout.isPending` and during navigation to `/checkout/[reference]`.
3. **Backend TOCTOU race & missing pending deduplication (`api/internal/modules/subscription/service.go`, `repository.go`):** `Service.Checkout` performed a non-atomic `CheckoutByIdempotencyKey` check before `CreateSubscription`, causing concurrent identical keys to hit `payments_idempotency_key_idx` (`SQLSTATE 23505`) and return `HTTP 500`. `CheckoutByIdempotencyKey` also lacked a `user_id` filter, and there was no `(user_id, plan_id)` pending checkout deduplication.
4. **CORS preflight rejection (`api/internal/middleware/cors.go`):** `Access-Control-Allow-Headers` omitted `Idempotency-Key`.

---

## Changes Made

- **`api/migrations/000006_pending_subscription_unique.up.sql` & `.down.sql`:** Deduplicated any pre-existing pending rows per `(user_id, plan_id)` and added partial unique index `subscriptions_user_plan_pending_idx ON subscriptions (user_id, plan_id) WHERE status = 'pending'`.
- **`api/internal/middleware/cors.go`:** Added `Idempotency-Key` to `Access-Control-Allow-Headers`.
- **`api/internal/modules/subscription/repository.go`:** Scoped `CheckoutByIdempotencyKey` by `s.user_id`, added `PendingCheckoutByUserAndPlan`, and added `isUniqueViolation` (`SQLSTATE 23505`).
- **`api/internal/modules/subscription/service.go`:** Updated `Service.Checkout` to check `PendingCheckoutByUserAndPlan` and recover cleanly from `isUniqueViolation` by replaying the winning checkout.
- **`web/src/lib/queries.ts`:** Updated `useCheckout()` to accept `{ planCode, idempotencyKey }`.
- **`web/src/app/pricing/page.tsx`:** Added per-plan `idempotencyKeysRef`, `isBusy` (`checkout.isPending || isNavigating`) guard, disabled buttons, and `"Memproses..."` loading label.
- **Regression Tests:** Added `api/internal/middleware/cors_test.go` (`TestCORS_AllowsIdempotencyKeyHeader`) and 3 tests in `api/internal/modules/subscription/repository_test.go` (`TestCheckout_ReplaysSameIdempotencyKeyConcurrently`, `TestCheckout_DeduplicatesPendingCheckoutAcrossNewIdempotencyKeys`, `TestCheckout_AllowsDifferentPlanWhilePendingExists`).

---

## Proof of Completion (Before vs. After)

### Backend API & Concurrency
- **Before ([evidence/before-api-repro.txt](./evidence/before-api-repro.txt)):**
  - Different `Idempotency-Key`s for the same user & `premium_1d` created 2 distinct subscriptions (`feffc24d-...`, `3ba26d5a-...`) and 2 payment references (`4cbee115...`, `88d0a7bd...`).
  - 10 concurrent requests with the same `Idempotency-Key` resulted in 6 `201 Created` and 4 `HTTP 500` errors.
- **After ([evidence/after-api-repro.txt](./evidence/after-api-repro.txt)):**
  - Different `Idempotency-Key`s for the same user & `premium_1d` returned the exact same `subscription.id` (`ac442858-2655-42f3-b6b4-29c805330209`) and `payment.reference` (`b98d1f3762e0e4a31b9c6917`).
  - All 10 concurrent requests with the same `Idempotency-Key` returned `STATUS=201` with the identical `SUB=03bd844c-f2c1-44e1-9067-31479f4f9080` and `REF=fd505f351f4154832e997929` (0 errors).

### Live Browser (`/pricing` under Slow 3G)
- **Before ([evidence/before-browser-repro.txt](./evidence/before-browser-repro.txt)):**
  - Double-clicking `"Berlangganan"` kept `disabled: false` and fired 2 `POST /api/v1/subscription/checkout` requests with distinct `Idempotency-Key`s; both were blocked at CORS preflight.
- **After ([evidence/after-browser-repro.txt](./evidence/after-browser-repro.txt)):**
  - Double-clicking `"Berlangganan"` immediately disabled all plan buttons and showed `"Memproses..."`, firing only **1** `OPTIONS` (`204`) and **1** `POST` (`201`), navigating to `/checkout/59a22db9fd9b575e73a18ca2`.
  - Navigating back to `/pricing`, reloading, and clicking the same plan again replayed the exact same reference `59a22db9fd9b575e73a18ca2`.

---

## Regression Checks

- `cd api && go vet ./...`: **PASS**
- `cd api && go test -p 1 ./...`: **PASS** (note: `-race` skipped on this Windows host because `gcc` is not in `%PATH%`)
- `cd api && go build -o bin/api ./cmd/server`: **PASS**
- `cd web && bun run check`: **PASS**
- `cd web && bun run lint`: **PASS** (0 errors; 1 pre-existing warning in `web/src/app/checkout/[reference]/page.tsx`)

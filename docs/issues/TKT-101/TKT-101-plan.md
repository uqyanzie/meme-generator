# TKT-101 Fix Plan: Meme Creation Permitted After Subscription Expiration

- **Ticket:** [TKT-101](./TKT-101.md)
- **Layer:** Backend (`api/`)
- **Status:** Plan Ready for Review (Phase 1 — `edd-plan`)

---

## 1. Root Cause Analysis

### Defect Location
- [repository.go](../../../api/internal/modules/subscription/repository.go#L166-L189) (`Repository.ActiveByUser`)

### Mechanism
1. When a subscription's `ends_at` timestamp passes, its `subscriptions.status` column remains `'active'` in Postgres until the background janitor (`App.expireJanitor` in [app.go](../../../api/internal/app/app.go#L20-L21) and [app.go](../../../api/internal/app/app.go#L58-L83)) runs `MarkExpired` on its 1-hour `expireInterval` ticker.
2. `Repository.ActiveByUser` queries:
   ```sql
   SELECT s.id, s.user_id, s.plan_id, s.status, s.starts_at, s.ends_at,
          p.id, p.code, p.name, p.price_idr, p.duration_days, p.max_memes, p.active
   FROM subscriptions s
   JOIN plans p ON p.id = s.plan_id
   WHERE s.user_id = $1 AND s.status = 'active'
   ORDER BY s.ends_at DESC LIMIT 1
   ```
   It filters only on `s.status = 'active'` and omits `AND s.ends_at > now()`, even though the comments on `MarkExpired` ([repository.go](../../../api/internal/modules/subscription/repository.go#L191-L193)) and `expireJanitor` ([app.go](../../../api/internal/app/app.go#L56-L57)) explicitly assume `ActiveByUser` performs a runtime `ends_at > now()` check as the source of truth.
3. Because `ActiveByUser` is the single runtime lookup behind all subscription enforcement paths, an expired subscription with `status = 'active'` and `ends_at <= now()` causes three simultaneous failures for up to 1 hour:
   - **Status reporting:** `subscription.Service.Status` ([service.go](../../../api/internal/modules/subscription/service.go#L77-L100)) returns `subscribed: true` with a past `endsAt`.
   - **Meme creation gate:** `subscriptionGate.CanCreate` ([v1.go](../../../api/internal/server/v1/v1.go#L26-L38)) calls `sub.Status`, so `PrepareUpload`, `ResultUpload`, and `Complete` ([meme/service.go](../../../api/internal/modules/meme/service.go#L35-L95)) all permit meme creation after expiration.
   - **Renewal checkout:** `subscription.Service.Checkout` ([service.go](../../../api/internal/modules/subscription/service.go#L50-L52)) calls `ActiveByUser` and falsely rejects renewal attempts with `409 Conflict` (`ErrAlreadySubscribed`).

---

## 2. Before Reproduction Evidence

Raw output captured verbatim in [before-api-repro.txt](./evidence/before-api-repro.txt).

Summary of live reproduction against `http://localhost:8080` and Postgres (`localhost:5433`):
1. Registered a fresh user, checked out `premium_1d`, and confirmed payment (`status = 'active'`).
2. Backdated `ends_at = now() - interval '1 hour'` in Postgres while keeping `status = 'active'` (simulating the window before `expireJanitor` runs; verified `is_expired = t`).
3. Observed defective behavior:
   - `GET /api/v1/subscription/status` → `HTTP 200` with `"subscribed":true,"status":"active","endsAt":"2026-11-22T16:42:05Z"` (1 hour in the past).
   - `POST /api/v1/memes/upload` → `HTTP 201` (`upload disiapkan`), returning a presigned S3 upload URL and creating a meme record.
   - `POST /api/v1/memes/{id}/complete` → `HTTP 200` (`meme selesai`), completing the meme.
   - `POST /api/v1/subscription/checkout` → `HTTP 409` (blocked from renewing because `ActiveByUser` still treated the expired row as active).
4. Cleaned up the temporary test user.

---

## 3. Agreed Decisions

Recorded in [TKT-101.md](./TKT-101.md#L36-L38):
- **Quota counting window (`MemesUsed`):** Keep TKT-101 strictly scoped to `ends_at > now()`. Do not modify the `MemesUsed` rolling window in `subscription/service.go` (`Status`) in this ticket.

---

## 4. Minimal Fix Design

### Production Code Change
- **[api/internal/modules/subscription/repository.go](../../../api/internal/modules/subscription/repository.go#L166-L189)** (`Repository.ActiveByUser`):
  Update the `WHERE` clause from:
  ```sql
  WHERE s.user_id = $1 AND s.status = 'active'
  ```
  to:
  ```sql
  WHERE s.user_id = $1 AND s.status = 'active' AND s.ends_at > now()
  ```

No schema migrations, no API contract changes, and no frontend changes are required. The existing composite index `idx_subscriptions_user_active ON subscriptions (user_id, status, ends_at DESC)` ([000002_core_schema.up.sql](../../../api/migrations/000002_core_schema.up.sql#L33)) already supports this exact predicate and ordering.

---

## 5. Edge Cases & Regression Risks

- **Exact boundary (`ends_at == now()`):** `MarkExpired` uses `ends_at <= now()` ([repository.go](../../../api/internal/modules/subscription/repository.go#L197)). Using `s.ends_at > now()` in `ActiveByUser` is the exact logical complement — no gap or overlap at the boundary.
- **`NULL` `ends_at` safety:** In SQL, `NULL > now()` evaluates to `UNKNOWN` (falsy), so any malformed `'active'` row with a `NULL` `ends_at` is safely treated as not active rather than active indefinitely.
- **All meme creation stages:** `PrepareUpload`, `ResultUpload`, and `Complete` all enforce `s.gate.CanCreate(ctx, userID)` → `sub.Status` → `ActiveByUser`. If a subscription expires mid-flow (between upload and render/complete), subsequent steps are immediately blocked with `403 Forbidden` (`CodeNotSubscribed`).
- **Immediate renewal after expiration:** Because `Service.Checkout` checks `ActiveByUser`, users whose subscription just expired can immediately check out a new plan without waiting for the hourly janitor.
- **Unexpired active subscriptions:** Subscriptions with `status = 'active'` and `ends_at > now()` continue returning `subscribed: true` and permitting meme creation normally.
- **Periodic janitor (`expireJanitor`):** Continues running hourly to persist `status = 'expired'` in the database; no changes needed.

---

## 6. Verification Plan

### A. Automated Regression Tests (Red → Green in `edd-implement`)
1. **[api/internal/modules/subscription/repository_test.go](../../../api/internal/modules/subscription/repository_test.go)** (new test file using `newTestPool` from `mount_test.go` with per-test cleanup):
   - `TestActiveByUser_RejectsExpiredSubscription`: inserts a user + `'active'` subscription with `ends_at = now() - interval '1 minute'`; asserts `ActiveByUser` returns `ErrNotSubscribed` and `Service.Status` returns `Subscribed == false`.
   - `TestActiveByUser_RejectsNullEndsAt`: inserts `'active'` subscription with `ends_at = NULL`; asserts `ActiveByUser` returns `ErrNotSubscribed`.
   - `TestActiveByUser_ReturnsActiveUnexpiredSubscription`: inserts `'active'` subscription with `ends_at = now() + interval '1 hour'`; asserts `ActiveByUser` succeeds and `Service.Status` returns `Subscribed == true`.
   - `TestCheckout_AllowedAfterExpirationBeforeJanitor`: verifies `Service.Checkout` succeeds (does not return `ErrAlreadySubscribed`) when the user's prior `'active'` subscription has `ends_at < now()`.
2. **[api/internal/server/v1/v1_test.go](../../../api/internal/server/v1/v1_test.go)** (new test file):
   - `TestSubscriptionGate_BlocksExpiredSubscription`: verifies `subscriptionGate.CanCreate` returns `meme.ErrNotSubscribedGate` for an `'active'` subscription with `ends_at < now()`, and returns `nil` when `ends_at > now()`.

### B. Static Analysis, Race Tests & Build Gate
```powershell
cd api
go vet ./...
go test -race ./...
go build -o bin/api ./cmd/server
```

### C. Live "After" Reproduction (Mirroring Step 2)
Restart the local Go API with the fix and run the same reproduction flow, saving verbatim output to `docs/issues/TKT-101/evidence/after-api-repro.txt`:
1. Register user → checkout `premium_1d` → confirm payment → create one meme while active (optional, to test `complete` after expiry).
2. Backdate `ends_at = now() - interval '1 hour'` in Postgres (`status` still `'active'`).
3. Verify:
   - `GET /api/v1/subscription/status` → `HTTP 200`, `subscribed: false`, `memesUsed: 0`, `maxMemes: 0`.
   - `POST /api/v1/memes/upload` → `HTTP 403` (`CodeNotSubscribed`).
   - `POST /api/v1/memes/{id}/complete` → `HTTP 403` (`CodeNotSubscribed`).
   - `POST /api/v1/subscription/checkout` → `HTTP 201` (renewal succeeds).
4. Clean up test user.

# TKT-109 Fix Plan: Quota Bypass via Concurrent Requests (Race Condition)

## Root Cause Analysis

Two coupled defects exist in the meme creation and quota enforcement path:

1. **TOCTOU Race Condition in `PrepareUpload` (Quota Bypass):**
   - `api/internal/modules/meme/service.go` (`PrepareUpload`, L38–L54) calls `s.gate.CanCreate(ctx, userID)` **outside any database transaction**, and then separately inserts the meme row via `s.repo.Create(ctx, ...)` (`api/internal/modules/meme/repository.go`, L26–L40).
   - `subscriptionGate.CanCreate` (`api/internal/server/v1/v1.go`, L26–L38) delegates to `subscription.Service.Status` (`api/internal/modules/subscription/service.go`, L91–L114), which executes two separate non-locking queries: `ActiveByUser` (`api/internal/modules/subscription/repository.go`, L201–L224) and `MemesUsed` (`SELECT COUNT(*) FROM memes WHERE user_id = $1 AND created_at >= $2`, L239–L250).
   - Under concurrent `POST /api/v1/memes/upload` requests from the same user, multiple goroutines evaluate `COUNT(*) < max_memes` before any of them commits `INSERT INTO memes`. Every racing request passes the quota check and inserts a new row, allowing the user to exceed `max_memes`.

2. **Final-Meme Completion Off-By-One Blocking (`ResultUpload` & `Complete`):**
   - `api/internal/modules/meme/service.go` calls `s.gate.CanCreate(ctx, userID)` in both `ResultUpload` (L72–L75) and `Complete` (L97–L100).
   - Because `PrepareUpload` already inserted the meme row into `memes`, creating the final allowed meme (e.g., meme #10 of 10 on `premium_1d`) increments `MemesUsed` to `10`.
   - When the client immediately calls `POST /api/v1/memes/{id}/result-upload` and `POST /api/v1/memes/{id}/complete` to finish that 10th meme, `CanCreate` sees `10 >= 10` and returns `403 Forbidden` (`ErrNotSubscribedGate`), permanently blocking completion of the user's final valid meme.

---

## Before Reproduction Evidence

Raw verbatim live API reproduction output is saved at [before-api-repro.txt](./evidence/before-api-repro.txt):

- **Repro A (Concurrent Quota Bypass):**
  - Fresh user on `premium_1d` (`maxMemes = 10`, initial `memesUsed = 0`).
  - Fired 20 concurrent `POST /api/v1/memes/upload` requests.
  - Result: **15 succeeded (`HTTP 201`)**, 5 rejected (`HTTP 403`).
  - `GET /api/v1/subscription/status` reported `memesUsed = 15, maxMemes = 10`; `GET /api/v1/memes` reported `total = 15` (5 memes over quota).
- **Repro B (Final-Meme Completion Blocked):**
  - Fresh user on `premium_1d` (`maxMemes = 10`) created 10 sequential memes (`memesUsed = 10, maxMemes = 10`).
  - Calling `POST /api/v1/memes/{meme10Id}/result-upload` returned **`HTTP 403`** (`code: 20002`, `"butuh langganan aktif untuk membuat meme"`).
  - Calling `POST /api/v1/memes/{meme10Id}/complete` returned **`HTTP 403`** (`code: 20002`, `"butuh langganan aktif untuk membuat meme"`).

---

## Agreed Decisions

Both pending decisions in [TKT-109.md](./TKT-109.md) are finalized:

1. **PostgreSQL Concurrency Control:** Use row-level locking on the user's active subscription row inside a PostgreSQL transaction during `PrepareUpload`:
   - `SELECT ... FROM subscriptions s JOIN plans p ON p.id = s.plan_id WHERE s.user_id = $1 AND s.status = 'active' AND s.ends_at > now() ORDER BY s.ends_at DESC LIMIT 1 FOR UPDATE OF s`
   - Lock **only** `s` (`FOR UPDATE OF s`) so users sharing the same `plans` row do not serialize against each other.
   - Count memes and insert the new `memes` row within the same transaction before committing.
2. **Separate Gate Checks for `PrepareUpload` vs `ResultUpload`/`Complete`:**
   - `PrepareUpload` enforces active subscription + remaining quota atomically with meme row creation.
   - `ResultUpload` and `Complete` enforce only an active (unexpired) subscription without re-checking quota for an already-reserved meme row.

---

## Minimal Fix Design

Keep scope strictly limited to `TKT-109`, preserving module decoupling (`meme` does not import `subscription`) and existing HTTP/JSON contracts (`403 Forbidden`, `response.CodeNotSubscribed = 20002`, `meme.ErrNotSubscribedGate`):

1. **`api/internal/modules/subscription/repository.go`**
   - Add `ActiveByUserForUpdate(ctx context.Context, tx pgx.Tx, userID string) (Subscription, Plan, error)` executing the active subscription lookup with `FOR UPDATE OF s` on `tx`.
   - Add `MemesUsedTx(ctx context.Context, tx pgx.Tx, userID string, since time.Time) (int32, error)` executing `SELECT COUNT(*) FROM memes WHERE user_id = $1 AND created_at >= $2` on `tx`.
   - (Optional small refactor: share query/scan helper with `ActiveByUser` and `MemesUsed` to avoid duplication.)

2. **`api/internal/modules/subscription/service.go`**
   - Add `HasActive(ctx context.Context, userID string) error`: calls `s.repo.ActiveByUser(ctx, userID)` and returns `nil` if active, `ErrNotSubscribed` if no active/unexpired subscription, or unexpected DB error.
   - Add `CheckQuotaTx(ctx context.Context, tx pgx.Tx, userID string) error`:
     - Calls `s.repo.ActiveByUserForUpdate(ctx, tx, userID)`.
     - If `plan.MaxMemes > 0`, computes `since := time.Now().AddDate(0, 0, -int(plan.Duration))` (matching `Status`), calls `s.repo.MemesUsedTx(ctx, tx, userID, since)`, and returns `ErrQuotaExceeded` (or `ErrNotSubscribed` — mapped in `v1.go` to `meme.ErrNotSubscribedGate`) when `used >= plan.MaxMemes`.
     - If `plan.MaxMemes == 0` (unlimited plan, e.g., `premium_90d`), permits creation once active subscription is verified.

3. **`api/internal/modules/subscription/errors.go`**
   - Add `ErrQuotaExceeded = errors.New("kuota meme tercapai")` so callers/tests can distinguish quota exhaustion from missing/expired subscription while `v1.go` preserves the `meme.ErrNotSubscribedGate` HTTP 403 contract.

4. **`api/internal/modules/meme/repository.go`**
   - Add `CreateWithCheck(ctx context.Context, m Meme, check func(ctx context.Context, tx pgx.Tx) error) (Meme, error)`:
     - Starts a PostgreSQL transaction via `r.pool.Begin(ctx)` with `defer tx.Rollback(ctx)`.
     - Executes `check(ctx, tx)` (if non-nil); on error, rolls back and returns the error verbatim (preserving sentinel errors like `ErrNotSubscribedGate`).
     - Executes `INSERT INTO memes ... RETURNING ...` on `tx`.
     - Commits via `tx.Commit(ctx)` and returns the created `Meme`.

5. **`api/internal/modules/meme/service.go`**
   - Update `Gate` interface:
     ```go
     type Gate interface {
         HasActiveSubscription(ctx context.Context, userID string) error
         CheckQuotaTx(ctx context.Context, tx pgx.Tx, userID string) error
     }
     ```
   - Update `PrepareUpload`:
     - Validate file extension, MIME type, and file size (`validateImageUpload`) *before* opening the DB transaction (fast fail, no unnecessary row lock contention).
     - Call `s.repo.CreateWithCheck(ctx, Meme{...}, func(ctx context.Context, tx pgx.Tx) error { return s.gate.CheckQuotaTx(ctx, tx, userID) })`.
     - Generate S3 presigned URL *after* the transaction commits so lock hold time is minimal.
   - Update `ResultUpload` and `Complete`:
     - Replace `s.gate.CanCreate(ctx, userID)` with `s.gate.HasActiveSubscription(ctx, userID)`.

6. **`api/internal/server/v1/v1.go`**
   - Update `subscriptionGate` to implement `HasActiveSubscription` and `CheckQuotaTx`:
     - `HasActiveSubscription(ctx, userID)` calls `g.sub.HasActive(ctx, userID)` and maps `subscription.ErrNotSubscribed` to `meme.ErrNotSubscribedGate`.
     - `CheckQuotaTx(ctx, tx, userID)` calls `g.sub.CheckQuotaTx(ctx, tx, userID)` and maps `subscription.ErrNotSubscribed` / `subscription.ErrQuotaExceeded` to `meme.ErrNotSubscribedGate`.

7. **Existing Test Updates (`api/internal/modules/meme/service_test.go`, `api/internal/server/v1/v1_test.go`)**
   - Update `stubGate` in `api/internal/modules/meme/service_test.go` to implement `HasActiveSubscription` and `CheckQuotaTx`.
   - Update `TestSubscriptionGate_BlocksExpiredSubscription` in `api/internal/server/v1/v1_test.go` to verify both `HasActiveSubscription` and `CheckQuotaTx` block expired subscriptions and permit active ones.

---

## Edge Cases & Regression Risks

- **Cross-user lock contention:** Using `FOR UPDATE OF s` (not bare `FOR UPDATE`) ensures PostgreSQL locks only the user's row in `subscriptions` and never locks the shared `plans` row.
- **Unlimited plans (`max_memes = 0`, e.g., `premium_90d`):** Must not treat `used >= 0` as exhausted; only enforce `used >= plan.MaxMemes` when `plan.MaxMemes > 0`.
- **Expired subscription on `ResultUpload` / `Complete` (`TKT-101`):** `HasActiveSubscription` still enforces `status = 'active' AND ends_at > now()`, so expired users remain blocked with `403`.
- **Ownership / IDOR on `ResultUpload` / `Complete` (`TKT-107`):** `GetByID(ctx, id, userID)` and `SetResultURL(ctx, id, userID, resultURL)` remain untouched.
- **Invalid upload payload (`TKT-108`):** Running `validateImageUpload` before `CreateWithCheck` ensures invalid files/MIME/sizes fail fast with `400`/`413` and never acquire DB row locks or create orphan rows.
- **Concurrent transaction rollback:** `defer tx.Rollback(ctx)` guarantees failed quota checks or context cancellations release the `FOR UPDATE OF s` lock immediately without leaking transactions.

---

## Verification Plan

### 1. Automated Regression Tests (Red → Green in `edd-implement`)
Add integration/concurrency tests in `api/internal/server/v1/v1_test.go` (run with `-race`):
- **`TestMemeQuota_ConcurrentPrepareUploadEnforcesLimit`:**
  - Seed a user with an active `premium_1d` subscription (`max_memes = 10`).
  - Launch 20 goroutines simultaneously (synchronized via a start channel) calling `memeSvc.PrepareUpload(...)`.
  - Assert **exactly 10** succeed and **exactly 10** fail with `meme.ErrNotSubscribedGate`.
  - Assert `SELECT COUNT(*) FROM memes WHERE user_id = $1` equals **10**.
- **`TestMemeQuota_FinalAllowedMemeCanResultUploadAndComplete`:**
  - Seed a user with an active `premium_1d` subscription (`max_memes = 10`).
  - Create 10 memes via `PrepareUpload` (confirming 11th fails with `meme.ErrNotSubscribedGate`).
  - Call `ResultUpload` and `Complete` on the 10th meme and assert both succeed.
  - Expire the subscription (`ends_at = now() - interval '1 minute'`) and assert `ResultUpload` and `Complete` now fail with `meme.ErrNotSubscribedGate`.
- **`TestMemeQuota_UnlimitedPlanAllowsBeyondStandardLimits`:**
  - Seed a user with `premium_90d` (`max_memes = 0`) and verify concurrent `PrepareUpload` calls all succeed.

### 2. Static Analysis, Race Tests & Build Gates
- Backend: `cd api && go vet ./... && go test -race ./... && go build -o bin/api ./cmd/server`
- Frontend: `cd web && bun run check && bun run lint` (sanity check, no frontend changes expected)

### 3. Live "After" Reproduction (Mirroring Before Evidence)
Re-run the exact two live API scenarios against the rebuilt server and save raw output to `docs/issues/TKT-109/evidence/after-api-repro.txt`:
- **After Repro A:** 20 concurrent `POST /api/v1/memes/upload` requests on `premium_1d` (`maxMemes = 10`) -> **exactly 10 `HTTP 201`**, **10 `HTTP 403`**, `memesUsed = 10`, `total = 10`.
- **After Repro B:** 10 sequential memes on `premium_1d` -> 10th meme's `POST /api/v1/memes/{id}/result-upload` returns **`HTTP 200`** and `POST /api/v1/memes/{id}/complete` returns **`HTTP 200`**, while an 11th `POST /api/v1/memes/upload` returns **`HTTP 403`**.

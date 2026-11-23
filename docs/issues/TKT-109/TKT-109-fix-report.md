# TKT-109 Fix Report: Quota Bypass via Concurrent Requests (Race Condition)

## Summary (Non-Engineer Friendly)

Users with a limited meme quota (e.g., 10 memes on `premium_1d`) could bypass their limit by sending multiple upload requests at the same time, because the server checked the remaining quota and saved the new meme in two separate steps. At the same time, a user who legitimately reached their final allowed meme (e.g., meme #10 of 10) was blocked from finishing it because the completion steps re-checked the already-full quota.

Both issues are now fixed:
1. Quota checking and meme creation now happen atomically inside a single database transaction with a per-user row lock, so concurrent requests cannot exceed `max_memes`.
2. Finishing an already-created meme (`result-upload` and `complete`) now checks only that the user's subscription is still active, allowing the final allowed meme to complete normally.

---

## Root Cause (Technical)

1. **TOCTOU Race Condition in `PrepareUpload`:**
   - `api/internal/modules/meme/service.go` (`PrepareUpload`) called `s.gate.CanCreate(ctx, userID)` outside any database transaction, then separately called `s.repo.Create(...)`.
   - Under concurrent `POST /api/v1/memes/upload` requests, multiple goroutines read `COUNT(*) < max_memes` before any transaction committed `INSERT INTO memes`, allowing requests to exceed `max_memes`.
2. **Final-Meme Completion Off-By-One Blocking:**
   - `ResultUpload` and `Complete` in `api/internal/modules/meme/service.go` also called `s.gate.CanCreate(ctx, userID)`.
   - Because `PrepareUpload` already inserted the meme row, `MemesUsed` equaled `max_memes` when the client called `result-upload` and `complete` for the final allowed meme, causing `CanCreate` to return `403 Forbidden` (`ErrNotSubscribedGate`).

---

## Changes Made

- `api/internal/modules/subscription/errors.go`: Added `ErrQuotaExceeded` sentinel error.
- `api/internal/modules/subscription/repository.go`: Added `ActiveByUserForUpdate` (`FOR UPDATE OF s`, locking only the user's `subscriptions` row) and `MemesUsedTx` to run inside a `pgx.Tx`.
- `api/internal/modules/subscription/service.go`: Added `HasActive(ctx, userID)` and `CheckQuotaTx(ctx, tx, userID)` (handling `plan.MaxMemes == 0` as unlimited).
- `api/internal/modules/meme/repository.go`: Added `CreateWithCheck(ctx, m, check)` to run the quota check and `INSERT INTO memes` atomically in a single PostgreSQL transaction.
- `api/internal/modules/meme/service.go`: Split `Gate` into `HasActiveSubscription` and `CheckQuotaTx`; updated `PrepareUpload` to validate upload metadata first and then call `CreateWithCheck`; updated `ResultUpload` and `Complete` to call `HasActiveSubscription`.
- `api/internal/server/v1/v1.go`: Updated `subscriptionGate` to implement `HasActiveSubscription` and `CheckQuotaTx`, preserving `meme.ErrNotSubscribedGate` (`403 Forbidden`, `code: 20002`).
- `api/internal/modules/meme/service_test.go`: Updated `stubGate` for the split `Gate` interface.
- `api/internal/server/v1/v1_test.go`: Updated `TestSubscriptionGate_BlocksExpiredSubscription` and added `TestMemeQuota_ConcurrentPrepareUploadEnforcesLimit`, `TestMemeQuota_FinalAllowedMemeCanResultUploadAndComplete`, and `TestMemeQuota_UnlimitedPlanAllowsBeyondStandardLimits`.

---

## Proof of Completion (Before vs. After)

### Before ([before-api-repro.txt](./evidence/before-api-repro.txt))
- **Concurrent `POST /api/v1/memes/upload` (20 requests, `maxMemes = 10`):** `15` returned `HTTP 201`, `5` returned `HTTP 403`; `memesUsed = 15`, `total = 15` (5 over quota).
- **Final allowed meme (#10 of 10):** `POST /api/v1/memes/{id}/result-upload` and `POST /api/v1/memes/{id}/complete` both returned `HTTP 403` (`code: 20002`).

### After ([after-api-repro.txt](./evidence/after-api-repro.txt))
- **Concurrent `POST /api/v1/memes/upload` (20 requests, `maxMemes = 10`):** **10** returned `HTTP 201`, **10** returned `HTTP 403`; `memesUsed = 10`, `total = 10`.
- **Final allowed meme (#10 of 10):** `POST /api/v1/memes/{id}/result-upload` returned `HTTP 200`, `POST /api/v1/memes/{id}/complete` returned `HTTP 200`, and an 11th `POST /api/v1/memes/upload` returned `HTTP 403`.

---

## Regression Checks

- `cd api && go vet ./...`: **PASS**
- `cd api && go test ./...`: **PASS** (note: `-race` requires `gcc`/CGO which is not installed in this Windows environment; concurrency correctness verified via `TestMemeQuota_ConcurrentPrepareUploadEnforcesLimit` and live 20-request concurrent HTTP test)
- `cd api && go build -o bin/api.exe ./cmd/server`: **PASS**
- `cd web && bun run check`: **PASS**
- `cd web && bun run lint`: **PASS** (1 pre-existing warning in `web/src/app/checkout/[reference]/page.tsx`)

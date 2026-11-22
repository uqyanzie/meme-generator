# TKT-101 Fix Report: Meme Creation Permitted After Subscription Expiration

- **Ticket:** [TKT-101](./TKT-101.md)
- **Plan:** [TKT-101-plan.md](./TKT-101-plan.md)
- **Status:** Resolved

---

## Summary (Non-Engineer Friendly)

Previously, when a user's subscription expired, the database still marked it as `"active"` until a background cleanup job ran once every hour. Because the system only checked that `"active"` label without checking whether the expiration time had already passed, users could continue uploading images and creating memes for up to an hour after their subscription ended, and were also blocked from renewing their plan during that window.

We fixed the active-subscription lookup to always check that the expiration timestamp (`ends_at`) is still in the future. As soon as a subscription's expiration time passes, meme creation is immediately blocked, status accurately shows as unsubscribed, and users can renew right away.

---

## Root Cause (Technical)

- **Location:** [repository.go](../../../api/internal/modules/subscription/repository.go#L166-L189) (`Repository.ActiveByUser`)
- **Cause:** `ActiveByUser` queried `WHERE s.user_id = $1 AND s.status = 'active'`, omitting `AND s.ends_at > now()`. Between expiration and the hourly `expireJanitor` (`MarkExpired`) run, `ActiveByUser` continued returning the expired row, causing `subscription.Service.Status` to report `subscribed: true`, `subscriptionGate.CanCreate` to permit meme creation, and `subscription.Service.Checkout` to reject renewals with `409 Conflict`.

---

## Changes Made

- **[api/internal/modules/subscription/repository.go](../../../api/internal/modules/subscription/repository.go#L172):** Added `AND s.ends_at > now()` to `Repository.ActiveByUser`.
- **[api/internal/modules/subscription/repository_test.go](../../../api/internal/modules/subscription/repository_test.go):** Added regression tests `TestActiveByUser_RejectsExpiredSubscription`, `TestActiveByUser_RejectsNullEndsAt`, `TestActiveByUser_ReturnsActiveUnexpiredSubscription`, and `TestCheckout_AllowedAfterExpirationBeforeJanitor`.
- **[api/internal/server/v1/v1_test.go](../../../api/internal/server/v1/v1_test.go):** Added `TestSubscriptionGate_BlocksExpiredSubscription` verifying `subscriptionGate.CanCreate` blocks expired `'active'` subscriptions and permits unexpired ones.

---

## Proof of Completion (Before vs. After)

- **Before** ([before-api-repro.txt](./evidence/before-api-repro.txt)):
  - With `status = 'active'` and `ends_at = now() - interval '1 hour'`:
  - `GET /api/v1/subscription/status` → `HTTP 200`, `"subscribed":true`
  - `POST /api/v1/memes/upload` → `HTTP 201` (permitted)
  - `POST /api/v1/memes/{id}/complete` → `HTTP 200` (permitted)
  - `POST /api/v1/subscription/checkout` → `HTTP 409` (blocked)
- **After** ([after-api-repro.txt](./evidence/after-api-repro.txt)):
  - With `status = 'active'` and `ends_at = now() - interval '1 hour'`:
  - `GET /api/v1/subscription/status` → `HTTP 200`, `"subscribed":false`
  - `POST /api/v1/memes/upload` → `HTTP 403` (`CodeNotSubscribed`)
  - `POST /api/v1/subscription/checkout` → `HTTP 201` (renewal succeeds)

---

## Regression Checks

- `go vet ./...` → passed
- `go test ./...` → passed (all 5 new TKT-101 regression tests + existing suite; `-race` skipped on Windows due to `CGO_ENABLED=1` requirement)
- `go build -o bin/api ./cmd/server` → passed
- `bun run check` (`tsc --noEmit`) → passed
- `bun run lint` (`next lint`) → passed (0 errors; 1 pre-existing warning in `checkout/[reference]/page.tsx`)

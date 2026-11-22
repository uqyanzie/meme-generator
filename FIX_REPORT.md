# Fix Report — Meme Portal

Consolidated engineering & non-engineer summary of resolved production issues (`TKT-101` – `TKT-109`).

---

## TKT-103: Broken Logout Flow and Infinite Redirect Loop

- **Status:** Resolved
- **Detailed Report:** [docs/issues/TKT-103/TKT-103-fix-report.md](./docs/issues/TKT-103/TKT-103-fix-report.md)
- **Layer:** Backend + Frontend

### Summary
Clicking **Keluar (Logout)** previously failed to delete auth cookies (wrong `Path=/api/v1/auth` instead of `/`), left access tokens valid until expiry, and left non-`HttpOnly` cookies readable by JavaScript. Combined with `/login` & `/register` redirecting on raw `document.cookie` presence and `/` redirecting unauthenticated users to `/login`, users were trapped in an infinite `/login` ↔ `/` redirect loop.

### Solution
- **Backend:** Set `HttpOnly: true` in `cookie.Set`; cleared both auth cookies on `Path: "/"` via `cookie.Clear`; added unique `jti` claims and a Redis access-token denylist (`auth:revoked:access:<jti>`) checked in `VerifyAccess(ctx, raw)`; removed `authMW` from `POST /api/v1/auth/logout` so logout is idempotent and always clears cookies.
- **Frontend:** Removed the forced `/login` redirect from `/`; replaced `document.cookie` redirects in `/login` and `/register` with guarded `useMe()` checks; invalidated `["me"]` in `useRegister`; switched `useLogout` cleanup to `onSettled`.
- **Verification:** Added Go regression tests (`cookie_test.go`, `jwt_test.go`, `handler_test.go`), passed `go vet`, `go test`, `go build`, `bun run check`, `bun run lint`, and verified live Before vs. After API behavior (`docs/issues/TKT-103/evidence/after-api-repro.txt`).

---

## TKT-102: Inconsistent Subscription Status and Cross-User Data Leakage

- **Status:** Resolved
- **Detailed Report:** [docs/issues/TKT-102/TKT-102-fix-report.md](./docs/issues/TKT-102/TKT-102-fix-report.md)
- **Layer:** Backend

### Summary
`GET /api/v1/subscription/status` previously emitted `Cache-Control: public, max-age=86400` without varying on authentication credentials (`Vary: Origin` only). This allowed shared caches (office Wi-Fi proxies/CDNs) to leak one user's subscription status and quota to other users—including unauthenticated visitors—and caused users' browsers to serve stale pre-payment `"subscribed": false` responses from HTTP disk cache across tab reopens.

### Solution
- **Backend:** Removed the inline `Cache-Control: public, max-age=86400` wrapper from `GET /api/v1/subscription/status` in `api/internal/modules/subscription/mount.go`; enforced `Cache-Control: private, no-store` and appended `Vary: Cookie` and `Vary: Authorization` at the entry of `middleware.Auth` (`api/internal/middleware/auth.go`), protecting all authenticated endpoints (including `401` responses) while preserving `Vary: Origin` from `middleware.CORS` and leaving public routes untouched.
- **Verification:** Added Go regression tests (`api/internal/middleware/auth_test.go`, `api/internal/modules/subscription/mount_test.go`), passed `go vet`, `go test`, `go build`, `bun run check`, `bun run lint`, and verified live Before vs. After API behavior (`docs/issues/TKT-102/evidence/after-api-repro.txt`).


---

## TKT-104: Stale Subscription State in Navbar and Packages Page Until Hard Refresh

- **Layer:** Frontend (`web/`)
- **Status:** Resolved
- **Fix Report:** [TKT-104-fix-report.md](./docs/issues/TKT-104/TKT-104-fix-report.md)
- **Root Cause:** `useSubscriptionStatus` set `staleTime: Number.POSITIVE_INFINITY` and ran unconditionally even when logged out; `useLogin`, `useRegister`, `useCompleteMeme`, and `useDeleteMeme` did not invalidate `queryKeys.subscription`.
- **Solution:** Extracted `queryKeys` into `web/src/lib/query-keys.ts`, configured `useSubscriptionStatus` with `staleTime: 0`, `refetchOnWindowFocus: true`, `retry: false`, and `enabled: Boolean(user)`, added `queryKeys.subscription` invalidation to auth and meme mutations, and guarded `/studio` with `me.isLoading || (me.data && status.isPending)`.

---

## TKT-101: Meme Creation Permitted After Subscription Expiration

- **Status:** Resolved
- **Detailed Report:** [docs/issues/TKT-101/TKT-101-fix-report.md](./docs/issues/TKT-101/TKT-101-fix-report.md)
- **Layer:** Backend

### Summary
`Repository.ActiveByUser` previously queried `WHERE s.user_id = $1 AND s.status = 'active'` without checking `s.ends_at > now()`. Between expiration and the hourly `expireJanitor` (`MarkExpired`) run, expired subscriptions were still treated as active, permitting meme creation, reporting `subscribed: true`, and blocking renewal checkouts with `409 Conflict`.

### Solution
- **Backend:** Added `AND s.ends_at > now()` to `Repository.ActiveByUser` in `api/internal/modules/subscription/repository.go`.
- **Verification:** Added Go regression tests (`api/internal/modules/subscription/repository_test.go`, `api/internal/server/v1/v1_test.go`), passed `go vet`, `go test`, `go build`, `bun run check`, `bun run lint`, and verified live Before vs. After API behavior (`docs/issues/TKT-101/evidence/after-api-repro.txt`).

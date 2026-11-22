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

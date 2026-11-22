# TKT-103 Fix Report: Broken Logout Flow and Infinite Redirect Loop

- **Ticket:** [TKT-103](./TKT-103.md)
- **Plan:** [TKT-103-plan.md](./TKT-103-plan.md)
- **Layer:** Backend + Frontend
- **Status:** Resolved

---

## 1. Summary (Non-Engineer Friendly)

Previously, clicking **Keluar (Logout)** appeared to succeed, but the user's browser kept their login cookies because the server tried to delete them from the wrong URL path. Even worse, the server did not invalidate the active access token on logout, and the login/register pages blindly redirected users to the home page whenever they saw a cookie—while the home page redirected unauthenticated users back to `/login`. Once the stale cookie expired or failed verification, users were trapped in an infinite `/login` ↔ `/` redirect loop.

**How it is fixed:**
- Auth cookies are now marked `HttpOnly` (cannot be tampered with or read by browser JavaScript) and are properly cleared on `Path=/` during logout.
- Logging out immediately revokes both the active access token (via a Redis denylist until its natural expiry) and the refresh token, and always succeeds idempotently even if the session is already expired.
- The home page (`/`) is now a public landing page, and `/login` & `/register` check verified server session state (`useMe()`) instead of raw `document.cookie`, completely eliminating the redirect loop.

---

## 2. Root Cause (Technical)

1. **Cookie `Path` mismatch on logout (`api/internal/modules/auth/handler.go`):** `setAuthCookies` set cookies on `Path: "/"`, whereas `clearAuthCookies` emitted `Max-Age=0` on `Path: "/api/v1/auth"`, leaving the `Path=/` cookies intact in the browser.
2. **Non-`HttpOnly` auth cookies (`api/internal/platform/cookie/cookie.go`):** `cookie.Set` used `HttpOnly: false`, exposing JWTs to `document.cookie`.
3. **No access token revocation (`api/internal/platform/token/jwt.go`, `api/internal/middleware/auth.go`):** Access JWTs had no `jti` claim and `VerifyAccess` performed stateless signature/expiry checks only; replaying a logged-out access token still returned `200 OK`.
4. **Logout blocked for expired sessions (`api/internal/modules/auth/mount.go`, `api/internal/modules/auth/handler.go`):** `POST /api/v1/auth/logout` was wrapped in `authMW`, returning `401` without clearing stale cookies when the access token was expired/invalid.
5. **Client-side redirect loop (`web/src/app/login/page.tsx`, `web/src/app/register/page.tsx`, `web/src/app/page.tsx`, `web/src/lib/auth.ts`):** `/login` and `/register` redirected to `/` based on `document.cookie.includes("meme_access_token")`, while `/` redirected to `/login` when `!me.data`. Additionally, `useRegister` did not invalidate `["me"]` and `useLogout` only cleared client state in `onSuccess`.

---

## 3. Changes Made

### Backend (`api/`)
- [`api/internal/platform/cookie/cookie.go`](../../../api/internal/platform/cookie/cookie.go): Changed `cookie.Set` to `HttpOnly: true`.
- [`api/internal/platform/token/jwt.go`](../../../api/internal/platform/token/jwt.go): Added 128-bit random hex `jti` (`RegisteredClaims.ID`) to signed tokens; added `ParseAccess`, `RevokeAccess` (with `ttl <= 0` guard against immortal Redis keys), and Redis denylist check (`auth:revoked:access:<jti>`) in `VerifyAccess(ctx, raw)` (fail-closed on Redis error).
- [`api/internal/middleware/auth.go`](../../../api/internal/middleware/auth.go): Passed `r.Context()` to `tokens.VerifyAccess(r.Context(), raw)`.
- [`api/internal/modules/auth/mount.go`](../../../api/internal/modules/auth/mount.go): Removed `authMW` from `POST /api/v1/auth/logout` so expired/invalid sessions can always clear cookies.
- [`api/internal/modules/auth/service.go`](../../../api/internal/modules/auth/service.go): Updated `Logout(ctx, accessRaw, refreshRaw)` to revoke access token `jti` and refresh token in Redis whenever valid tokens are present.
- [`api/internal/modules/auth/handler.go`](../../../api/internal/modules/auth/handler.go): Replaced `clearAuthCookies` to use `cookie.Clear` (`Path: "/"`, `MaxAge: -1`, `HttpOnly: true`); added `extractAccessToken`; made `Logout` idempotent/best-effort (always clears cookies and returns `200 OK`).
- **Tests added:**
  - [`api/internal/platform/cookie/cookie_test.go`](../../../api/internal/platform/cookie/cookie_test.go)
  - [`api/internal/platform/token/jwt_test.go`](../../../api/internal/platform/token/jwt_test.go)
  - [`api/internal/modules/auth/handler_test.go`](../../../api/internal/modules/auth/handler_test.go)

### Frontend (`web/`)
- [`web/src/app/page.tsx`](../../../web/src/app/page.tsx): Removed forced redirect to `/login` (and `"use client"`), keeping `/` as a public landing page.
- [`web/src/lib/auth.ts`](../../../web/src/lib/auth.ts): Invalidated `["me"]` in `useRegister` `onSuccess`; switched `useLogout` cleanup (`qc.clear()` + redirect to `/`) from `onSuccess` to `onSettled`.
- [`web/src/app/login/page.tsx`](../../../web/src/app/login/page.tsx) & [`web/src/app/register/page.tsx`](../../../web/src/app/register/page.tsx): Replaced `document.cookie` redirect with guarded `useMe()` check (`if (me.data && !mutation.isPending && !mutation.isSuccess) router.replace("/")`).

---

## 4. Proof of Completion (Before vs. After)

Evidence files:
- **Before:** [before-api-repro.txt](./evidence/before-api-repro.txt) | [before-browser-repro.txt](./evidence/before-browser-repro.txt)
- **After:** [after-api-repro.txt](./evidence/after-api-repro.txt)

| Check | Before (Broken) | After (Fixed) |
| :--- | :--- | :--- |
| `POST /api/v1/auth/register` cookies & JWT | `Path=/; Max-Age=900; SameSite=Lax` (**no `HttpOnly`**, **no `jti`**) | `Path=/; Max-Age=900; HttpOnly; SameSite=Lax` + `"jti":"17348cda7fe4a0ca8501e0a73e906e72"` |
| `POST /api/v1/auth/logout` `Set-Cookie` | `Path=/api/v1/auth; Max-Age=0` (wrong path; cookies survive) | `Path=/; Max-Age=0; HttpOnly; SameSite=Lax` (both cookies cleared) |
| `GET /api/v1/auth/me` replaying logged-out access token | `200 OK` (still accepted) | `401 Unauthorized` (`{"success":false,"code":10002,"message":"unauthorized"}`) |
| `POST /api/v1/auth/refresh` after logout | `401 Unauthorized` | `401 Unauthorized` |
| `POST /api/v1/auth/logout` with expired/invalid cookies | `401 Unauthorized` (no `Set-Cookie` headers) | `200 OK` + `Set-Cookie` clearing both cookies on `Path=/` |

---

## 5. Regression Checks

- `cd api && go vet ./...` — **PASS**
- `cd api && go test -v ./...` — **PASS** (`cookie`, `token`, `auth` — all 7 tests passed; note: `-race` requires `gcc`/CGO which is not installed in this Windows host)
- `cd api && go build -o bin/api ./cmd/server` — **PASS**
- `cd web && bun run check` (`tsc --noEmit`) — **PASS**
- `cd web && bun run lint` (`next lint`) — **PASS** (0 errors; only pre-existing unused-var warning in `checkout/[reference]/page.tsx`)

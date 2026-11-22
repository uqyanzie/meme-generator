# TKT-103 Fix Plan: Broken Logout Flow and Infinite Redirect Loop

- **Ticket:** [TKT-103](./TKT-103.md)
- **Layer:** Backend + Frontend
- **Status:** Planned (Awaiting approval to implement)
- **Before Evidence:** [before-api-repro.txt](./evidence/before-api-repro.txt) | [before-browser-repro.txt](./evidence/before-browser-repro.txt)

---

## 1. Root Cause Analysis

The broken logout flow and `/login` ↔ `/` infinite redirect loop are caused by **five compounding defects** across the Go API and Next.js frontend:

### Backend Defects

1. **Wrong cookie `Path` on logout (`api/internal/modules/auth/handler.go:33-46`)**
   - `setAuthCookies` (`handler.go:25-31`) stores `meme_access_token` and `meme_refresh_token` using `cookie.Set` with `Path: "/"`.
   - `clearAuthCookies` (`handler.go:35-46`) ignores the existing `cookie.Clear` helper and manually calls `http.SetCookie` with `Path: "/api/v1/auth"`.
   - Per RFC 6265, cookies are keyed by `(name, domain, path)`. A `Max-Age=0` cookie on `Path=/api/v1/auth` does **not** delete cookies scoped to `Path=/`, so both auth cookies survive logout in the browser.

2. **Non-`HttpOnly` auth cookies (`api/internal/platform/cookie/cookie.go:11-21`)**
   - `cookie.Set` sets `HttpOnly: false` (exposing JWTs to XSS via `document.cookie`), which enabled the frontend to make auth routing decisions from raw cookie presence instead of server-verified session state.

3. **Access tokens remain valid after logout (`api/internal/platform/token/jwt.go:85-139`, `api/internal/middleware/auth.go:24-49`)**
   - `Manager.signed` (`jwt.go:85-99`) does not populate `jti` (`RegisteredClaims.ID`).
   - `Manager.Revoke` (`jwt.go:134-139`) deletes only `auth:refresh:<userID>` from Redis.
   - `Manager.VerifyAccess` (`jwt.go:115-117`) performs stateless signature/expiry parsing only, without checking any revocation denylist.
   - Result: After `POST /api/v1/auth/logout`, the previously issued `meme_access_token` is still accepted by `GET /api/v1/auth/me` (and all protected routes) for its full 15-minute TTL.

4. **Logout endpoint blocked for expired sessions (`api/internal/modules/auth/mount.go:23`, `api/internal/modules/auth/handler.go:119-131`)**
   - `POST /api/v1/auth/logout` is wrapped in `authMW`.
   - If a user's access token has expired or is invalid (and refresh fails), `POST /api/v1/auth/logout` returns `401 Unauthorized` without emitting `Set-Cookie` headers to clear stale cookies, leaving the user unable to recover via logout.

### Frontend Defects

5. **Unverified cookie redirect on auth pages + forced redirect on `/` (`web/src/app/login/page.tsx:21-25`, `web/src/app/register/page.tsx:20-24`, `web/src/app/page.tsx:16-20`, `web/src/lib/auth.ts:29-64`)**
   - `login/page.tsx:21-25` and `register/page.tsx:20-24` immediately call `router.replace("/")` whenever `document.cookie.includes("meme_access_token")`, without verifying whether the session is actually valid.
   - `page.tsx:16-20` calls `router.replace("/login")` whenever `!me.isLoading && !me.data`.
   - **Loop mechanism:** Because logout fails to clear `Path=/` cookies while deleting `auth:refresh:<userID>` in Redis, once `meme_access_token` is expired/invalid (or revoked):
     1. `/login` sees `meme_access_token` in `document.cookie` → `router.replace("/")`.
     2. `/` runs `useMe()` → `GET /api/v1/auth/me` returns `401` → axios interceptor calls `POST /api/v1/auth/refresh` → `401` → `!me.isLoading && !me.data` → `router.replace("/login")`.
     3. `/login` sees the same stale cookie → repeats infinitely.
   - Additionally:
     - `useRegister` (`web/src/lib/auth.ts:29-39`) does not invalidate `["me"]` on success, leaving `SiteHeader` stale after registration.
     - `useLogout` (`web/src/lib/auth.ts:55-64`) clears the query cache and navigates only in `onSuccess`, leaving UI state stuck if the logout request errors.

---

## 2. Before Reproduction Evidence

Captured against the live stack (`http://localhost:8080`) prior to any code changes (full raw output in [before-api-repro.txt](./evidence/before-api-repro.txt)):

1. **Register (`POST /api/v1/auth/register`) → `201 Created`:**
   - `Set-Cookie: meme_access_token=<jwt>; Path=/; Max-Age=900; SameSite=Lax` (**missing `HttpOnly`**, JWT payload has **no `jti`**).
   - `Set-Cookie: meme_refresh_token=<jwt>; Path=/; Max-Age=2592000; SameSite=Lax` (**missing `HttpOnly`**).
2. **`POST /api/v1/auth/logout` → `200 OK`:**
   - `Set-Cookie: meme_access_token=; Path=/api/v1/auth; Max-Age=0; SameSite=Lax` (**wrong `Path=/api/v1/auth`**).
   - `Set-Cookie: meme_refresh_token=; Path=/api/v1/auth; Max-Age=0; SameSite=Lax` (**wrong `Path=/api/v1/auth`**).
3. **`GET /api/v1/auth/me` AFTER logout (replaying the exact same access token) → `200 OK`** (access token still accepted).
4. **`POST /api/v1/auth/refresh` AFTER logout → `401 Unauthorized`** (refresh token was deleted in Redis, creating the exact split-brain state that triggers the `/login` ↔ `/` redirect loop once the un-cleared access cookie expires or is revoked).
5. **`POST /api/v1/auth/logout` with expired/invalid token → `401 Unauthorized`** with **no `Set-Cookie` headers** to clear stale cookies.

### Live Browser Reproduction (`http://localhost:3000`, full log in [before-browser-repro.txt](./evidence/before-browser-repro.txt))

1. **Login (`tkt103-repro@example.com`):** Landed on `/studio`, header showed `TKT103 Repro` + `Keluar`; both `meme_access_token` and `meme_refresh_token` were readable in `document.cookie` on `Path=/`.
2. **Click "Keluar" (Logout):** `POST /api/v1/auth/logout` returned `200` with `Set-Cookie` targeting `Path=/api/v1/auth`. Both cookies remained intact in `document.cookie`, `/` returned `200` from `/api/v1/auth/me` (still logged in), and visiting `/login` immediately redirected back to `/`.
3. **Stale/expired `meme_access_token` + revoked `meme_refresh_token`:** Setting `meme_access_token=expired.stale.token; path=/` and navigating to `/login` triggered an **infinite `/login` ↔ `/` redirect loop** (6+ cycles in ~3s), firing `GET /api/v1/auth/me → 401` and `POST /api/v1/auth/refresh → 401` on every cycle.

---

## 3. Agreed Decisions

All decisions from [TKT-103.md](./TKT-103.md#L40-L46) are finalized:

1. **Cookie `HttpOnly`:** Change `cookie.Set` from `HttpOnly: false` to `HttpOnly: true`.
2. **Home page (`/`) access policy:** Keep `/` as a public landing page by removing its forced redirect to `/login`; `SiteHeader` already provides **Masuk / Daftar** links.
3. **Access token invalidation (`jti` denylist):** Add a unique `jti` (`RegisteredClaims.ID`) to issued tokens; on logout, store `auth:revoked:access:<jti>` in Redis with TTL equal to remaining token lifetime, delete `auth:refresh:<userID>`, and check the denylist in `VerifyAccess(ctx, raw)`.
4. **Auth pages (`/login` & `/register`) redirect guard:** Replace `document.cookie.includes("meme_access_token")` in both pages with verified `useMe()` state (guarded so post-submit redirects to `/studio` and `/pricing` are not overridden), and invalidate `["me"]` in `useRegister`.
5. **Logout resilience (idempotent / best-effort):** Remove `authMW` from `POST /api/v1/auth/logout`; revoke access token (`jti`) and/or refresh token whenever valid tokens are present, **always** clear both cookies with `Path: "/"` and return `200 OK`; pair with `onSettled` cleanup in `useLogout`.

---

## 4. Minimal Fix Design

### Backend (`api/`)

#### 1. `api/internal/platform/cookie/cookie.go`
- In `Set` (L11-21): change `HttpOnly: false` to `HttpOnly: true` and update comment.
- Keep `Clear` (L23-33) as-is (`Path: "/"`, `MaxAge: -1`, `HttpOnly: true`, `Secure: false`, `SameSite: http.SameSiteLaxMode`).

#### 2. `api/internal/platform/token/jwt.go`
- Add constant:
  ```go
  const revokedAccessKeyPrefix = "auth:revoked:access:"
  ```
- In `signed` (L85-99): generate a cryptographically random 16-byte (32-char hex) `jti` using `crypto/rand` (returning an error if `rand.Read` fails) and set `ID: jti` in `jwt.RegisteredClaims`.
- Expose `ParseAccess(raw string) (*Claims, error)` calling `m.parse(raw, TypeAccess)` so logout can extract `UserID`, `ID` (`jti`), and `ExpiresAt` even on repeated/idempotent logout calls.
- Update `VerifyAccess` signature from `VerifyAccess(raw string)` to `VerifyAccess(ctx context.Context, raw string) (*Claims, error)`:
  - Call `m.parse(raw, TypeAccess)`.
  - If `m.redis != nil && claims.ID != ""`: check `m.redis.Exists(ctx, revokedAccessKeyPrefix+claims.ID).Result()`.
    - On Redis error: fail closed (return wrapped error).
    - If `exists > 0`: return `nil, ErrTokenRevoked`.
- Add `RevokeAccess(ctx context.Context, jti string, expiresAt time.Time) error`:
  - If `m.redis == nil || jti == ""`: return `nil`.
  - Compute `ttl := time.Until(expiresAt)`.
  - Guard `if ttl <= 0 { return nil }` (critical: prevents `go-redis` from writing a key with `0` / infinite TTL or erroring on negative TTL).
  - Store `m.redis.Set(ctx, revokedAccessKeyPrefix+jti, "1", ttl).Err()`.

#### 3. `api/internal/middleware/auth.go`
- Line 40: update call to `tokens.VerifyAccess(r.Context(), raw)`.

#### 4. `api/internal/modules/auth/mount.go`
- Line 23: change:
  ```go
  mux.Handle("POST /api/v1/auth/logout", authMW(http.HandlerFunc(h.Logout)))
  ```
  to:
  ```go
  mux.HandleFunc("POST /api/v1/auth/logout", h.Logout)
  ```

#### 5. `api/internal/modules/auth/service.go`
- Update `Logout` (L72-74) signature to `Logout(ctx context.Context, accessRaw, refreshRaw string) error`:
  - If `accessRaw != ""` and `claims, err := s.tokens.ParseAccess(accessRaw)` succeeds:
    - If `claims.ExpiresAt != nil`: call `s.tokens.RevokeAccess(ctx, claims.ID, claims.ExpiresAt.Time)`.
    - Call `s.tokens.Revoke(ctx, claims.UserID)` and record `revokedUserID = claims.UserID`.
  - If `refreshRaw != ""` and `claims, err := s.tokens.VerifyRefresh(ctx, refreshRaw)` succeeds and `claims.UserID != revokedUserID`:
    - Call `s.tokens.Revoke(ctx, claims.UserID)`.
  - Return `errors.Join(errs...)` so callers/tests can inspect any Redis errors while both revocation steps are attempted.

#### 6. `api/internal/modules/auth/handler.go`
- Replace `clearAuthCookies` (L33-46) implementation with:
  ```go
  func clearAuthCookies(w http.ResponseWriter) {
      cookie.Clear(w, cookie.AccessTokenName)
      cookie.Clear(w, cookie.RefreshTokenName)
  }
  ```
- Add small helper `extractAccessToken(r *http.Request) string` reading `Authorization: Bearer <token>` first, then falling back to `r.Cookie(cookie.AccessTokenName)` (same precedence as `middleware.Auth`).
- Update `Handler.Logout` (L119-131):
  - Extract `accessRaw := extractAccessToken(r)` and `refreshRaw` from `r.Cookie(cookie.RefreshTokenName)` (empty string if missing).
  - Call `if err := h.service.Logout(r.Context(), accessRaw, refreshRaw); err != nil { slog.WarnContext(r.Context(), "auth: gagal mencabut token saat logout", "err", err) }`.
  - **Always** call `clearAuthCookies(w)` and `response.Success(w, http.StatusOK, CodeAuthLogoutSuccess, nil, "logout berhasil")`.

---

### Frontend (`web/`)

#### 7. `web/src/app/page.tsx`
- Remove `useRouter`, `useEffect`, `useMe`, and the forced redirect to `/login` (L13-20), making `/` a clean public landing page. (Remove `"use client"` directive since no client hooks remain.)

#### 8. `web/src/lib/auth.ts`
- `useRegister` (L29-39): acquire `const qc = useQueryClient();` and add `onSuccess: () => qc.invalidateQueries({ queryKey: ["me"] })`.
- `useLogout` (L55-64): change `onSuccess` to `onSettled` so `qc.clear()` and `window.location.href = "/"` always run even if the logout request fails.

#### 9. `web/src/app/login/page.tsx`
- Import `useMe` from `@/lib/auth`.
- Replace `document.cookie.includes("meme_access_token")` check (L19-25) with:
  ```tsx
  const me = useMe();

  useEffect(() => {
    if (me.data && !login.isPending && !login.isSuccess) {
      router.replace("/");
    }
  }, [me.data, login.isPending, login.isSuccess, router]);
  ```
  - Why this guard works: `useLogin`'s `onSuccess` returns `qc.invalidateQueries({ queryKey: ["me"] })`, which runs while `login.isPending` is `true`, then transitions directly to `login.isSuccess === true` before `onSubmit` calls `router.push("/studio")`. Thus, the effect redirects **only** users who visit `/login` while already authenticated, and never hijacks post-login navigation to `/studio`.

#### 10. `web/src/app/register/page.tsx`
- Import `useMe` from `@/lib/auth`.
- Replace `document.cookie.includes("meme_access_token")` check (L19-24) with the same guarded pattern:
  ```tsx
  const me = useMe();

  useEffect(() => {
    if (me.data && !register.isPending && !register.isSuccess) {
      router.replace("/");
    }
  }, [me.data, register.isPending, register.isSuccess, router]);
  ```
  - Preserves post-register navigation to `/pricing` while redirecting already-authenticated visitors to `/`.

---

## 5. Edge Cases & Regression Risks

| Edge Case / Risk | Mitigation |
| :--- | :--- |
| **`go-redis` zero/negative TTL trap** (`Set(..., 0)` stores keys forever) | `RevokeAccess` checks `ttl := time.Until(expiresAt); if ttl <= 0 { return nil }` before writing to Redis. |
| **Idempotent double-logout** (same cookies submitted twice) | `Service.Logout` uses `ParseAccess` (not `VerifyAccess`), so re-logging out does not fail on `ErrTokenRevoked`; Redis `SET` + `DEL` are idempotent, cookies are cleared on `Path=/`, returns `200 OK`. |
| **Expired access token + valid refresh token on logout** | `authMW` removed from `/auth/logout`; `ParseAccess` fails on expired access token, then `VerifyRefresh` validates and revokes the active refresh token in Redis, and both cookies are cleared. |
| **Completely invalid/missing cookies on logout** | `Service.Logout` is a no-op; `Handler.Logout` still clears both cookies with `Path=/` and returns `200 OK` so users are never trapped. |
| **Redis unreachable during `VerifyAccess`** | Fail-closed: `VerifyAccess` returns an error → `middleware.Auth` responds `401 Unauthorized` instead of accepting potentially revoked tokens. |
| **Post-login / post-register redirect hijacking** | Guarding `useEffect` with `!login.isPending && !login.isSuccess` (and `!register.isPending && !register.isSuccess`) prevents `router.replace("/")` from racing with `router.push("/studio")` or `router.push("/pricing")`. |
| **Pre-existing tokens without `jti` during rolling deploy** | `VerifyAccess` skips Redis denylist lookup when `claims.ID == ""` (old tokens naturally expire within 15m; all newly issued tokens include a 128-bit hex `jti`). |
| **Unauthenticated `/` calling `useSubscriptionStatus()` via `SiteHeader`** | Existing `SiteHeader` calls `useSubscriptionStatus()`, which already fails silently with `sub.data === undefined` (header renders `Masuk / Daftar` when `!user`). Keep subscription query changes out of scope here (tracked under `TKT-104`). |
| **Cookie attribute mismatch between `Set` and `Clear`** | Both `cookie.Set` and `cookie.Clear` use `Path: "/"`, `HttpOnly: true`, `Secure: false`, `SameSite: http.SameSiteLaxMode`, guaranteeing browser deletion. |

---

## 6. Verification Plan

### Automated Tests (Go)
1. **`api/internal/platform/cookie/cookie_test.go`**
   - Assert `cookie.Set` emits `HttpOnly=true`, `Path=/`, `SameSite=Lax`.
   - Assert `cookie.Clear` emits `Path=/`, `MaxAge=-1` (`Max-Age=0`), `HttpOnly=true`, empty value.
2. **`api/internal/platform/token/jwt_test.go`**
   - Assert `Issue` generates unique non-empty `jti` (`Claims.ID`) per token.
   - Assert `RevokeAccess` with past `expiresAt` does not write an immortal key to Redis.
   - Assert `VerifyAccess` rejects a revoked `jti` with `ErrTokenRevoked`, while still accepting a different token's `jti` for the same user (using local Redis with isolated keys and `t.Cleanup`).
   - Assert `VerifyRefresh` rejects revoked refresh token after `Revoke`.
3. **`api/internal/modules/auth/handler_test.go`**
   - Full `Logout` + `middleware.Auth` HTTP tests covering:
     - Active session logout → `200 OK`, `Set-Cookie` clears both cookies with `Path=/` & `HttpOnly`, replayed access token rejected with `401`, refresh token rejected.
     - Expired access token + valid refresh token logout → `200 OK`, refresh token revoked in Redis, cookies cleared with `Path=/`.
     - Missing / garbage tokens logout → `200 OK`, cookies still cleared with `Path=/`.
     - Repeated logout (idempotency) → `200 OK`, cookies cleared with `Path=/`.

### Static Analysis, Tests & Build Gate
```powershell
# Backend
Push-Location api; go vet ./...; go test -race ./...; go build -o bin/api ./cmd/server; Pop-Location

# Frontend
Push-Location web; bun run check; bun run lint; Pop-Location
```

### Live Reproduction Verification ("After" Evidence)
1. Restart the local API & Web processes (if needed) so they run the updated build.
2. Re-run the exact Step 2 live API script and verify:
   - Register/Login `Set-Cookie` headers include `HttpOnly` and `Path=/`, and access JWT includes `jti`.
   - `POST /api/v1/auth/logout` returns `200 OK` with `Set-Cookie` headers having `Path=/; Max-Age=0; HttpOnly`.
   - Replaying the logged-out access token against `GET /api/v1/auth/me` returns `401 Unauthorized`.
   - `POST /api/v1/auth/logout` with an expired/invalid token returns `200 OK` and clears both cookies on `Path=/`.
3. Verify browser flows on `http://localhost:3000`:
   - Login → lands on `/studio` (not hijacked to `/`).
   - Register → lands on `/pricing` and header immediately shows the new user.
   - Logout ("Keluar") → clears cookies, lands on public `/` showing **Masuk / Daftar**, navigating to `/login` or `/register` renders forms cleanly with zero redirect loop.
   - Visiting `/login` or `/register` while already logged in redirects cleanly to `/`.

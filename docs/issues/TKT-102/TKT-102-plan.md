# TKT-102 Fix Plan: Inconsistent Subscription Status and Cross-User Data Leakage

- **Ticket:** [TKT-102](./TKT-102.md)
- **Layer:** Backend
- **Status:** Implemented & Verified
- **Before Evidence:** [before-api-repro.txt](./evidence/before-api-repro.txt)

---

## 1. Root Cause Analysis

The inconsistent post-payment subscription status and cross-user data leakage are caused by **explicit public HTTP caching on a per-user authenticated endpoint**, compounded by the absence of defensive cache-control headers on authenticated middleware:

1. **Public 24-hour `Cache-Control` on per-user endpoint (`api/internal/modules/subscription/mount.go:18-24`)**
   - `GET /api/v1/subscription/status` is wrapped in an inline handler inside `authMW` that explicitly sets:
     ```go
     w.Header().Set("Cache-Control", "public, max-age=86400")
     ```
   - **Mechanism of cross-user leakage:** Per RFC 9111, the `public` directive explicitly permits **shared caches** (CDNs, corporate/office Wi-Fi HTTP proxies, reverse proxies) to store and reuse the response even when the request carried authentication (`Authorization` or `Cookie`). Because the response only sets `Vary: Origin` (from `middleware.CORS`) and does **not** vary on `Cookie` or `Authorization`, a shared proxy caches User A's JSON payload (`subscribed`, `planName`, `endsAt`, `memesUsed`, `maxMemes`) under the bare URL `/api/v1/subscription/status` and serves it to any other user on the same network—including unauthenticated users—for up to 24 hours.
   - **Mechanism of stale post-payment status across tab reopens:** `max-age=86400` instructs the user's own browser HTTP disk cache to serve the cached `"subscribed": false` response for 24 hours without revalidating with the origin server. Even closing and reopening the tab (which clears client-side memory) still hits the browser HTTP disk cache.

2. **Missing default cache protection across authenticated routes (`api/internal/middleware/auth.go:24-49`)**
   - Full route audit of `api/internal/server/v1/v1.go` confirmed **no other route** emits `Cache-Control: public`.
   - However, none of the other authenticated `GET` endpoints (`GET /api/v1/auth/me` in `api/internal/modules/auth/mount.go:22`, `GET /api/v1/memes` in `api/internal/modules/meme/mount.go:22`) nor `401 Unauthorized` responses from `middleware.Auth` emit `Cache-Control: private, no-store` or `Vary: Cookie, Authorization`.
   - Without explicit `private, no-store` in `middleware.Auth`, authenticated cookie-based responses rely on heuristic/default cache behavior rather than enforcing non-cacheability centrally.

3. **Middleware ordering nuance (`api/internal/app/app.go:41-43`, `api/internal/middleware/cors.go:24-26`)**
   - Request pipeline order is `RequestLogger` → `CORS` → `ServeMux` → `middleware.Auth` → route handler.
   - `CORS` sets `w.Header().Set("Vary", "Origin")` before calling `next.ServeHTTP`. Therefore, `middleware.Auth` must **append** `Cookie` and `Authorization` using `w.Header().Add("Vary", ...)` rather than `Set`, so `Vary: Origin` is never overwritten.

*(Note: Client-side TanStack Query staleness where `useSubscriptionStatus` uses `staleTime: Number.POSITIVE_INFINITY` is a separate frontend defect tracked in [TKT-104](../TKT-104/TKT-104.md) and intentionally kept out of scope here.)*

---

## 2. Before Reproduction Evidence

Captured against the live Go API (`http://localhost:8080`) prior to any code changes (full verbatim headers & payloads in [before-api-repro.txt](./evidence/before-api-repro.txt)):

1. **Unauthenticated `GET /api/v1/subscription/status` (`Origin: http://localhost:3000`) → `401 Unauthorized`:**
   - Emitted `Vary: Origin` only; **no `Cache-Control`** and **no `Vary: Cookie` / `Authorization`**.
2. **Authenticated `GET /api/v1/subscription/status` BEFORE payment → `200 OK`:**
   - Emitted **`Cache-Control: public, max-age=86400`** and `Vary: Origin` with `"subscribed":false`.
3. **Checkout (`premium_1d`) + Confirm payment → `200 OK` (`status: "active"`).**
4. **Authenticated `GET /api/v1/subscription/status` AFTER payment → `200 OK`:**
   - Still emitted **`Cache-Control: public, max-age=86400`** and `Vary: Origin` while returning private user quota/plan details (`"subscribed":true,"planCode":"premium_1d","memesUsed":0,"maxMemes":10`).
5. **Audit of other authenticated `GET` endpoints (`GET /api/v1/auth/me`, `GET /api/v1/memes`) → `200 OK`:**
   - Neither emitted `Cache-Control: public`, and neither emitted `Cache-Control: private, no-store` or `Vary: Cookie, Authorization`.
6. **Audit of public endpoint (`GET /api/v1/subscription/plans`) → `200 OK`:**
   - Public catalog endpoint, not wrapped in `authMW`; emits `Vary: Origin` and no `Cache-Control: public, max-age=86400`.

---

## 3. Agreed Decisions

Finalized in [TKT-102.md](./TKT-102.md#L36-L40):

1. **Cache header policy:** Emit `Cache-Control: private, no-store` together with `Vary: Cookie` and `Vary: Authorization` (preserving `Vary: Origin` set by `middleware.CORS`).
2. **Enforcement scope:** Enforce globally at the entry of `middleware.Auth` for all authenticated endpoints (covering both authenticated responses and `401 Unauthorized` responses), and remove the buggy inline `Cache-Control: public, max-age=86400` wrapper from `GET /api/v1/subscription/status`.

---

## 4. Minimal Fix Design

Strictly backend-only (`api/`); zero frontend changes (keeps TKT-104 cleanly separated).

### 1. `api/internal/modules/subscription/mount.go`
- Lines 18–24: Remove the buggy inline closure that sets `Cache-Control: public, max-age=86400` and register `h.Status` directly under `authMW`:
  ```go
  mux.Handle("GET /api/v1/subscription/status", authMW(http.HandlerFunc(h.Status)))
  ```

### 2. `api/internal/middleware/auth.go`
- Lines 24–49: At the very start of the `Auth` middleware handler (before checking `Authorization` / `Cookie` or calling `unauthorized(w)`), apply defensive no-store and credential-vary headers:
  ```go
  w.Header().Set("Cache-Control", "private, no-store")
  w.Header().Add("Vary", "Cookie")
  w.Header().Add("Vary", "Authorization")
  ```
- Placing this before the `raw == ""` and `VerifyAccess` checks guarantees that:
  - Every `2xx`/`4xx`/`5xx` response on every route wrapped in `authMW` (`GET /api/v1/subscription/status`, `GET /api/v1/auth/me`, `GET /api/v1/memes`, and authenticated mutations) carries `Cache-Control: private, no-store` plus `Vary: Cookie` and `Vary: Authorization`.
  - Unauthenticated `401 Unauthorized` responses on protected endpoints also carry `Cache-Control: private, no-store` and `Vary: Cookie, Authorization`, preventing any intermediary from caching a `401` and serving it to an authenticated user (or vice versa).
  - `Vary: Origin` written earlier by `middleware.CORS` is preserved intact.

---

## 5. Edge Cases & Regression Risks

| Edge Case / Risk | Mitigation |
| :--- | :--- |
| **`Vary: Origin` overwritten by `Auth` middleware** | `middleware.CORS` executes before `router`/`authMW` and sets `Vary: Origin`. `middleware.Auth` uses `w.Header().Add("Vary", ...)` (never `Set`), preserving `Origin` alongside `Cookie` and `Authorization` (valid multi-line `Vary` per RFC 9110 §5.3). |
| **Cached `401 Unauthorized` poisoning authenticated users** | Headers are written at the top of `middleware.Auth` before token extraction/verification, so `401` responses are also marked `private, no-store` + `Vary: Cookie, Authorization`. |
| **Public endpoints unintentionally restricted** | `GET /api/v1/subscription/plans` and `GET /api/v1/health` are not wrapped in `authMW`, so they remain completely unaffected. |
| **Non-`authMW` auth mutation routes (`POST /auth/login`, `/register`, `/refresh`, `/logout`)** | All are `POST` endpoints with no explicit caching headers; per RFC 9111 §3, HTTP caches do not cache `POST` responses without explicit freshness lifetimes. |
| **Next.js rewrite proxy (`web/next.config.ts`)** | Next.js `/api/:path*` rewrites pass upstream response headers (`Cache-Control`, `Vary`) through to the browser; `private, no-store` prevents both browser HTTP cache and any upstream CDN/proxy from caching. |
| **Already-cached responses in existing user browsers** | Clients that already stored the pre-fix `max-age=86400` response will naturally expire or clear on hard refresh; all subsequent fetches receive `private, no-store` and will never be stored again. |
| **Frontend in-memory TanStack Query cache** | `useSubscriptionStatus` still uses `staleTime: Number.POSITIVE_INFINITY` until [TKT-104](../TKT-104/TKT-104.md) is resolved; TKT-102 fixes the HTTP/proxy/disk-cache layer cleanly without overlapping TKT-104's scope. |

---

## 6. Verification Plan

### Automated Regression Tests (Go — Red → Green in `edd-implement`)

1. **`api/internal/middleware/auth_test.go`**
   - **Authenticated via cookie (`meme_access_token`) & via `Authorization: Bearer`:** returns `200 OK`, `Cache-Control: private, no-store`, and `Vary` contains `Cookie` and `Authorization`.
   - **Unauthenticated / invalid token:** returns `401 Unauthorized`, `Cache-Control: private, no-store`, and `Vary` contains `Cookie` and `Authorization`.
   - **Composed with `middleware.CORS("http://localhost:3000")` (matching `app.New`):** request with `Origin: http://localhost:3000` preserves `Vary: Origin` **and** includes `Cookie` and `Authorization` (no clobbering).

2. **`api/internal/modules/subscription/mount_test.go`**
   - Mount `subscription.RegisterRoutes(mux, handler, middleware.Auth(tokens))` wrapped with `middleware.CORS("http://localhost:3000")`.
   - **Authenticated `GET /api/v1/subscription/status`:** returns `Cache-Control: private, no-store` (explicitly asserting it does **not** contain `public` or `max-age`), and `Vary` includes `Origin`, `Cookie`, and `Authorization`.
   - **Unauthenticated `GET /api/v1/subscription/status`:** returns `401 Unauthorized` with `Cache-Control: private, no-store` and `Vary` including `Origin`, `Cookie`, and `Authorization`.
   - **Public `GET /api/v1/subscription/plans`:** does **not** require auth and does **not** emit `Cache-Control: public, max-age=86400`.

### Static Analysis, Tests & Build Gate

```powershell
# Backend
Push-Location api; go vet ./...; go test -race ./...; go build -o bin/api ./cmd/server; Pop-Location
```

### Live Reproduction Verification ("After" Evidence)

1. Restart the local Go API process so it serves the rebuilt binary/code.
2. Re-run the exact Step 2 live API verification script against `http://localhost:8080` (saving raw output to `docs/issues/TKT-102/evidence/after-api-repro.txt`) and verify:
   - Unauthenticated `GET /api/v1/subscription/status` (`Origin: http://localhost:3000`) → `401 Unauthorized` with `Cache-Control: private, no-store` and `Vary` containing `Origin`, `Cookie`, `Authorization`.
   - Authenticated `GET /api/v1/subscription/status` (before payment and after payment confirmation) → `200 OK` with `Cache-Control: private, no-store` (no `public`, no `max-age=86400`) and `Vary` containing `Origin`, `Cookie`, `Authorization`.
   - Authenticated `GET /api/v1/auth/me` and `GET /api/v1/memes` → `200 OK` with `Cache-Control: private, no-store` and `Vary` containing `Origin`, `Cookie`, `Authorization`.
   - Public `GET /api/v1/subscription/plans` → `200 OK` with `Vary: Origin` and no auth requirement.

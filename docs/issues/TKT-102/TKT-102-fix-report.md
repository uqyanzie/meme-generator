# TKT-102 Fix Report: Inconsistent Subscription Status and Cross-User Data Leakage

- **Ticket:** [TKT-102](./TKT-102.md)
- **Plan:** [TKT-102-plan.md](./TKT-102-plan.md)
- **Layer:** Backend
- **Status:** Resolved

---

## 1. Summary (Non-Engineer Friendly)

Previously, the API endpoint that checks a user's subscription status (`GET /api/v1/subscription/status`) explicitly instructed browsers and shared network proxies (such as office Wi-Fi proxies or CDNs) to cache the response publicly for 24 hours (`Cache-Control: public, max-age=86400`).

Because the response was marked publicly cacheable without separating users by session credentials, two serious bugs occurred:
1. **Cross-user data leakage:** A shared network proxy could save one logged-in user's subscription status and quota and serve it to another user on the same network—even someone who was not logged in.
2. **Stale subscription status after payment:** A user's own browser cached the pre-payment `"not subscribed"` response for 24 hours on disk, so even closing and reopening the tab after paying still showed them as unsubscribed.

**How it is fixed:**
- Removed the buggy 24-hour public cache header from `GET /api/v1/subscription/status`.
- Enforced `Cache-Control: private, no-store` together with `Vary: Cookie` and `Vary: Authorization` centrally in the authentication middleware (`middleware.Auth`), protecting **all** authenticated endpoints (including `401 Unauthorized` responses) while preserving `Vary: Origin` for CORS and leaving public endpoints (`/api/v1/subscription/plans`, `/api/v1/health`) untouched.

*(Note: Client-side in-memory TanStack Query caching where `useSubscriptionStatus` uses `staleTime: Number.POSITIVE_INFINITY` is tracked separately in [TKT-104](../TKT-104/TKT-104.md).)*

---

## 2. Root Cause (Technical)

1. **Explicit public 24h caching on a per-user endpoint (`api/internal/modules/subscription/mount.go`):** `GET /api/v1/subscription/status` was wrapped in an inline closure setting `w.Header().Set("Cache-Control", "public, max-age=86400")`. Under RFC 9111, `public` explicitly authorizes shared caches to store authenticated responses, and because `Vary` only contained `Origin` (set by `middleware.CORS`), intermediaries keyed the cached payload solely by URL+Origin—leaking private user quota/plan data across users and serving stale pre-payment status from browser disk cache for up to 24 hours.
2. **No centralized cache protection on authenticated routes (`api/internal/middleware/auth.go`):** Other authenticated endpoints (`GET /api/v1/auth/me`, `GET /api/v1/memes`) and `401 Unauthorized` responses from `middleware.Auth` did not emit `Cache-Control: private, no-store` or `Vary: Cookie, Authorization`, leaving caching behavior dependent on intermediary defaults.

---

## 3. Changes Made

### Backend (`api/`)
- [`api/internal/modules/subscription/mount.go`](../../../api/internal/modules/subscription/mount.go): Removed the inline `Cache-Control: public, max-age=86400` wrapper around `GET /api/v1/subscription/status` and mounted `authMW(http.HandlerFunc(h.Status))` directly.
- [`api/internal/middleware/auth.go`](../../../api/internal/middleware/auth.go): At the entry of `Auth` (before token extraction/verification so both authenticated responses and `401 Unauthorized` are covered), set `Cache-Control: private, no-store` and appended `Vary: Cookie` and `Vary: Authorization` using `w.Header().Add` so `Vary: Origin` from `middleware.CORS` is preserved.
- **Tests added:**
  - [`api/internal/middleware/auth_test.go`](../../../api/internal/middleware/auth_test.go): Verifies `Cache-Control: private, no-store` and `Vary: Cookie, Authorization` for cookie auth, Bearer auth, missing/invalid credentials (`401`), and composition with `middleware.CORS` (`Vary` preserves `Origin` + `Cookie` + `Authorization`).
  - [`api/internal/modules/subscription/mount_test.go`](../../../api/internal/modules/subscription/mount_test.go): Verifies mounted `GET /api/v1/subscription/status` (authenticated `200` and unauthenticated `401`) emits `Cache-Control: private, no-store` (no `public`/`max-age`) + `Vary: Origin, Cookie, Authorization`, and confirms public `GET /api/v1/subscription/plans` stays unauthenticated and unaffected.

---

## 4. Proof of Completion (Before vs. After)

Evidence files:
- **Before:** [before-api-repro.txt](./evidence/before-api-repro.txt)
- **After:** [after-api-repro.txt](./evidence/after-api-repro.txt)

| Check (`Origin: http://localhost:3000`) | Before (Broken) | After (Fixed) |
| :--- | :--- | :--- |
| Unauthenticated `GET /api/v1/subscription/status` (`401`) | No `Cache-Control`; `Vary: Origin` only | `Cache-Control: private, no-store`; `Vary: Origin`, `Cookie`, `Authorization` |
| Authenticated `GET /api/v1/subscription/status` BEFORE payment (`200`, `subscribed: false`) | **`Cache-Control: public, max-age=86400`**; `Vary: Origin` only | `Cache-Control: private, no-store`; `Vary: Origin`, `Cookie`, `Authorization` |
| Authenticated `GET /api/v1/subscription/status` AFTER payment (`200`, `subscribed: true`, `premium_1d`) | **`Cache-Control: public, max-age=86400`**; `Vary: Origin` only | `Cache-Control: private, no-store`; `Vary: Origin`, `Cookie`, `Authorization` |
| Authenticated `GET /api/v1/auth/me` (`200`) | No `Cache-Control`; `Vary: Origin` only | `Cache-Control: private, no-store`; `Vary: Origin`, `Cookie`, `Authorization` |
| Authenticated `GET /api/v1/memes` (`200`) | No `Cache-Control`; `Vary: Origin` only | `Cache-Control: private, no-store`; `Vary: Origin`, `Cookie`, `Authorization` |
| Public `GET /api/v1/subscription/plans` (`200`) | No `Cache-Control`; `Vary: Origin` | No `Cache-Control`; `Vary: Origin` (unchanged, public) |

---

## 5. Regression Checks

- `cd api && go vet ./...` — **PASS**
- `cd api && go test -v ./...` — **PASS** (`middleware`, `auth`, `subscription`, `cookie`, `token` — all 13 tests passed, 0 skipped; note: `-race` requires `gcc`/CGO which is not installed on this Windows host)
- `cd api && go build -o bin/api ./cmd/server` — **PASS**
- `cd web && bun run check` (`tsc --noEmit`) — **PASS**
- `cd web && bun run lint` (`next lint`) — **PASS** (0 errors; only pre-existing unused-var warning in `checkout/[reference]/page.tsx`)

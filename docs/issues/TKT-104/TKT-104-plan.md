# TKT-104 Fix Plan: Stale Subscription State in Navbar and Packages Page Until Hard Refresh

- **Ticket:** [TKT-104](./TKT-104.md)
- **Layer:** Frontend
- **Status:** Planned (Awaiting approval to implement)
- **Before Evidence:** [before-browser-repro.txt](./evidence/before-browser-repro.txt)

---

## 1. Root Cause Analysis

The stale subscription package name in `SiteHeader` (navbar), stale quota on `/pricing` and `/studio`, and wasted unauthenticated `401` churn are caused by **four compounding TanStack Query defects** in the Next.js frontend (which previously compounded with TKT-102's backend HTTP `Cache-Control: public, max-age=86400` header):

1. **Infinite client-side cache lifetime (`web/src/lib/queries.ts:29-38`)**
   - `useSubscriptionStatus()` explicitly configures `staleTime: Number.POSITIVE_INFINITY` (paired with `Providers`'s global `refetchOnWindowFocus: false` in `web/src/components/providers.tsx:11`).
   - **Mechanism:** Once TanStack Query caches any response for `queryKeys.subscription` (`["subscription"]`), it considers that data eternally fresh. Navigating between `/`, `/pricing`, `/studio`, and `/checkout/[reference]`, returning to the browser tab after a purchase/expiration, or having quota change externally triggers **zero** `GET /api/v1/subscription/status` refetches until a full hard refresh (`Ctrl+Shift+R`) destroys the in-memory `QueryClient`.

2. **Missing subscription cache invalidation on auth mutations (`web/src/lib/auth.ts:29-55`)**
   - `useRegister()` (`auth.ts:39`) and `useLogin()` (`auth.ts:53`) only call `qc.invalidateQueries({ queryKey: ["me"] })`.
   - **Mechanism:** Neither invalidates `queryKeys.subscription` (nor `["memes"]`). If `["subscription"]` already holds cached data (e.g., `{ subscribed: false }` from before login or session transition), `useLogin` / `useRegister` leaves that stale payload cached forever under `staleTime: Number.POSITIVE_INFINITY`, causing `/studio` to block an active subscriber and the navbar hover card to keep showing `"Belum berlangganan"`.

3. **Missing subscription cache invalidation on meme mutations (`web/src/lib/queries.ts:91-110`)**
   - `useCompleteMeme()` (`queries.ts:100`) and `useDeleteMeme()` (`queries.ts:108`) only invalidate `{ queryKey: ["memes"] }`.
   - **Mechanism:** Backend `memesUsed` is computed from the user's `memes` count (`api/internal/modules/subscription/repository.go:205-215`). Completing or deleting a meme changes `memesUsed` on the server, but because `queryKeys.subscription` is never invalidated (and `staleTime: Infinity` blocks refetch on navigation), `Sisa kuota` in `/studio` and `Kuota meme` on `/pricing` stay stuck at the old value until a hard refresh.

4. **Unauthenticated subscription polling & retry churn (`web/src/lib/queries.ts:29-38`, `web/src/components/site-header.tsx:32`, `web/src/app/pricing/page.tsx:22`)**
   - `SiteHeader` (mounted in `RootLayout` across every route) and `PricingPage` call `useSubscriptionStatus()` unconditionally even when the user is logged out (`me.data === undefined`).
   - Because `Providers` (`providers.tsx:11`) sets global `retry: 1` and `api.ts` intercepts every `401` with `POST /api/v1/auth/refresh`, every logged-out page load fires `2× GET /api/v1/subscription/status (401)` + `3× POST /api/v1/auth/refresh (400)`.

*(Note: Backend HTTP caching on `GET /api/v1/subscription/status` was resolved in [TKT-102](../TKT-102/TKT-102.md), commit `1e4ba58`. CORS `Idempotency-Key` header support on `POST /api/v1/subscription/checkout` belongs to checkout / [TKT-105](../TKT-105/TKT-105.md) and is intentionally kept out of scope here.)*

---

## 2. Before Reproduction Evidence

Captured live against `http://localhost:3000` prior to any code changes (full verbatim requests, status codes, and UI text in [before-browser-repro.txt](./evidence/before-browser-repro.txt)):

1. **Unauthenticated visit to `http://localhost:3000/`:**
   - Fired `2× GET /api/v1/subscription/status` (`401`) and `3× POST /api/v1/auth/refresh` (`400`) while completely logged out.
2. **Stale subscription state after purchase across SPA navigation & tab focus:**
   - Registered fresh user (`tkt104_before_repro@example.com`), loaded `/pricing` (caching `{ subscribed: false }`), then completed checkout + payment confirmation on the backend (`status: "active"`, `planCode: "premium_1d"`).
   - Navigated SPA-style `/pricing` → `/studio` → `/` → `/pricing` and dispatched window `blur`/`focus`/`visibilitychange`.
   - **Result:** **Zero** `GET /api/v1/subscription/status` requests fired. `/studio` stayed blocked (`"Studio hanya untuk pelanggan"`), `/pricing` kept showing `"Status: belum berlangganan"`, and the navbar hover card kept showing `"Belum berlangganan — pilih paket"`.
   - In a fresh load, the same account immediately displayed `"Premium 1 Hari"`, `"Kuota meme: 0 / 10 (sisa 10)"`, and navbar `"Paket: Premium 1 Hari"`.
3. **Zero revalidation between `/studio` ↔ `/pricing`:**
   - Navigating `/studio` → `/pricing` → `/studio` fired **zero** `GET /api/v1/subscription/status` requests (proving quota displays stay frozen across navigation without invalidation or `staleTime: 0`).
4. **Missing invalidation in `useLogin`:**
   - Logging in via `/login` only invalidated `["me"]`. When `["subscription"]` already held a cached `{ subscribed: false }` response, logging in as the active subscriber fired **zero** `GET /api/v1/subscription/status` requests and locked them out of `/studio`.

---

## 3. Agreed Decisions

Finalized in [TKT-104.md](./TKT-104.md#L39-L42):

1. **Query caching strategy:** Configure `useSubscriptionStatus` with `staleTime: 0` and `refetchOnWindowFocus: true` so every navigation mount and browser window focus revalidates subscription/quota state against the server, paired with explicit invalidation.
2. **Auth gating & invalidation scope:** Gate `useSubscriptionStatus` with `enabled: Boolean(user)` (via `useMe()`) and `retry: false` to eliminate unauthenticated `401` + refresh-token churn, and explicitly invalidate `queryKeys.subscription` in `useLogin`, `useRegister`, `useConfirmPayment`, `useCompleteMeme`, and `useDeleteMeme`.

---

## 4. Minimal Fix Design

Strictly frontend-only (`web/`); zero backend changes (keeps scope strictly limited to TKT-104).

### 1. `web/src/lib/query-keys.ts` (new shared keys module, prevents `auth.ts` ↔ `queries.ts` circular import)
- Extract `queryKeys` into a tiny leaf module containing:
  ```ts
  export const queryKeys = {
    me: ["me"] as const,
    plans: ["plans"] as const,
    subscription: ["subscription"] as const,
    memes: (limit: number, offset: number) => ["memes", limit, offset] as const,
  };
  ```
- Re-export `export { queryKeys } from "@/lib/query-keys";` from `web/src/lib/queries.ts` so all existing imports continue working without churn.

### 2. `web/src/lib/auth.ts`
- Import `queryKeys` from `@/lib/query-keys`.
- Add small exported helper (also directly unit-testable with `QueryClient`):
  ```ts
  export function invalidateAuthQueries(qc: QueryClient) {
    return Promise.all([
      qc.invalidateQueries({ queryKey: queryKeys.me }),
      qc.invalidateQueries({ queryKey: queryKeys.subscription }),
      qc.invalidateQueries({ queryKey: ["memes"] }),
    ]);
  }
  ```
- `useMe()` (L21-27): use `queryKey: queryKeys.me` (identical `["me"]` key).
- `useRegister()` (L29-41): update `onSuccess` to `() => invalidateAuthQueries(qc)`.
- `useLogin()` (L43-55): update `onSuccess` to `() => invalidateAuthQueries(qc)`.
- Keep `useLogout()` (L57-66) as-is (`qc.clear()` + `window.location.href = "/"` in `onSettled`).

### 3. `web/src/lib/queries.ts`
- Import `queryOptions` and `type QueryClient` from `@tanstack/react-query`, `useMe` from `@/lib/auth`, and `queryKeys` from `@/lib/query-keys` (strictly acyclic: `query-keys.ts` → `auth.ts` → `queries.ts`).
- Re-export `export { queryKeys } from "@/lib/query-keys";`.
- Extract exported query options helper `subscriptionStatusQueryOptions(enabled: boolean)` and update `useSubscriptionStatus()` (L29-38):
  ```ts
  export function subscriptionStatusQueryOptions(enabled: boolean) {
    return queryOptions({
      queryKey: queryKeys.subscription,
      queryFn: async () =>
        unwrap<SubscriptionStatus>(await api.get("/api/v1/subscription/status")),
      enabled,
      staleTime: 0,
      refetchOnWindowFocus: true,
      retry: false,
    });
  }

  export function useSubscriptionStatus() {
    const { data: user } = useMe();
    return useQuery(subscriptionStatusQueryOptions(Boolean(user)));
  }
  ```
- Add small exported invalidation helpers:
  ```ts
  export function invalidateSubscriptionQuery(qc: QueryClient) {
    return qc.invalidateQueries({ queryKey: queryKeys.subscription });
  }

  export function invalidateMemeQueries(qc: QueryClient) {
    return Promise.all([
      qc.invalidateQueries({ queryKey: ["memes"] }),
      qc.invalidateQueries({ queryKey: queryKeys.subscription }),
    ]);
  }
  ```
- `useConfirmPayment()` (L59-70): `onSuccess: () => invalidateSubscriptionQuery(qc)` (preserves awaiting the returned promise before `CheckoutPage` redirects).
- `useCompleteMeme()` (L91-102): update `onSuccess` to `() => invalidateMemeQueries(qc)`.
- `useDeleteMeme()` (L104-110): update `onSuccess` to `() => invalidateMemeQueries(qc)`.
- Leave `useCheckout()`'s `Idempotency-Key` logic untouched (TKT-105 scope).

### 4. `web/src/app/studio/page.tsx` (defensive loading guard check)
- Line 206 currently checks:
  ```tsx
  if (me.isLoading || status.isLoading) {
  ```
- Update to:
  ```tsx
  if (me.isLoading || (me.data && status.isPending)) {
  ```
- **Why:** When `me.data` first becomes truthy and `useSubscriptionStatus` transitions from `enabled: false` → `enabled: true`, guarding on `(me.data && status.isPending)` guarantees zero single-frame flash of the `"Studio hanya untuk pelanggan"` paywall while the initial subscription fetch starts, while still rendering the login prompt immediately for unauthenticated users (`me.data === undefined`) and never hanging if `status` enters `error` state (`isPending === false`).

---

## 5. Edge Cases & Regression Risks

| Edge Case / Risk | Mitigation |
| :--- | :--- |
| **Circular module import (`auth.ts` ↔ `queries.ts`)** | `queryKeys` lives in leaf module `web/src/lib/query-keys.ts` and is re-exported by `queries.ts`; dependency graph is strictly acyclic (`query-keys` → `auth` → `queries`). |
| **Paywall flash on `/studio` when `enabled` flips `false` → `true`** | Guarding `StudioPage` with `me.isLoading || (me.data && status.isPending)` keeps the loading state until the first subscription status response resolves (or errors). |
| **Unauthenticated visitor on `/studio` or `/pricing`** | When `me.data` is `undefined`, `useSubscriptionStatus` has `enabled: false`, so zero `GET /api/v1/subscription/status` requests fire; `/studio` hits `if (!me.data)` immediately and `/pricing` renders public plans cleanly. |
| **Duplicate network calls from `SiteHeader` + `/pricing` or `/studio`** | Both components subscribe to the identical `queryKeys.subscription` key; TanStack Query deduplicates concurrent in-flight fetches into a single `GET /api/v1/subscription/status` request. |
| **Stale subscription after login without prior `qc.clear()`** | `invalidateAuthQueries` invalidates `queryKeys.me`, `queryKeys.subscription`, and `["memes"]` together, and `staleTime: 0` guarantees immediate refetch on mount/enable. |
| **`useConfirmPayment` navigation race before refetch finishes** | `onSuccess` returns the `invalidateQueries` Promise, so `await confirm.mutateAsync(reference)` in `CheckoutPage` waits for the active `SiteHeader` subscription query to refetch (and `/pricing` also revalidates on mount via `staleTime: 0`). |
| **HTTP disk cache masking frontend refetches** | Backend commit `1e4ba58` (TKT-102) sets `Cache-Control: private, no-store` and `Vary: Cookie, Authorization` on all `authMW` routes; ensure the running Go API process is restarted from current `HEAD` during live verification. |
| **TypeScript / ESLint compatibility of `bun test` regression tests** | Use `node:test` + `node:assert/strict` (typed by existing `@types/node`, executed natively by `bun test`) so zero new packages are needed and `bun run check` (`tsc --noEmit`) + `bun run lint` pass cleanly. |

---

## 6. Verification Plan

### Automated Regression Tests (TypeScript — Red → Green in `edd-implement`)

Create `web/src/lib/subscription-cache.test.ts` (executed via `cd web && bun test`):
1. **`subscriptionStatusQueryOptions(false)` (unauthenticated):**
   - Asserts `queryKey` equals `queryKeys.subscription`, `enabled === false`, `staleTime === 0` (not `Infinity`), `refetchOnWindowFocus === true`, and `retry === false`.
2. **`subscriptionStatusQueryOptions(true)` (authenticated):**
   - Asserts `enabled === true`, `staleTime === 0`, `refetchOnWindowFocus === true`, and `retry === false`.
3. **`invalidateAuthQueries(qc)` (`useLogin` & `useRegister`):**
   - Seeds a real `QueryClient` with cached `queryKeys.me`, `queryKeys.subscription`, and `queryKeys.memes(20, 0)` entries, awaits `invalidateAuthQueries(qc)`, and asserts all three queries are marked `isInvalidated === true`.
4. **`invalidateSubscriptionQuery(qc)` (`useConfirmPayment`):**
   - Seeds `queryKeys.subscription`, awaits `invalidateSubscriptionQuery(qc)`, and asserts `isInvalidated === true`.
5. **`invalidateMemeQueries(qc)` (`useCompleteMeme` & `useDeleteMeme`):**
   - Seeds `queryKeys.memes(20, 0)` and `queryKeys.subscription`, awaits `invalidateMemeQueries(qc)`, and asserts both are marked `isInvalidated === true`.

### Static Analysis, Tests & Build Gate

```powershell
# Frontend (unit tests, type-check, lint)
Push-Location web; bun test; bun run check; bun run lint; Pop-Location

# Backend (sanity check — unchanged in TKT-104)
Push-Location api; go vet ./...; go test -race ./...; Pop-Location
```

### Live Reproduction Verification ("After" Evidence)

1. **Restart the local Go API process** from current `HEAD` (so commit `1e4ba58` / TKT-102's `Cache-Control: private, no-store` is active on `http://localhost:8080`) and ensure Next.js dev server (`http://localhost:3000`) has picked up the updated `web/src/` files.
2. Re-run the exact Step 2 live browser scenarios against `http://localhost:3000` (saving raw output to `docs/issues/TKT-104/evidence/after-browser-repro.txt`) and verify:
   - **Unauthenticated visit (`/` and `/pricing`):** **Zero** `GET /api/v1/subscription/status` requests fire while logged out (only `GET /api/v1/auth/me` → `401` once).
   - **Register & Purchase flow:** Register a fresh user → `/pricing` shows `belum berlangganan` → complete checkout & payment confirmation → navigate SPA-style `/pricing` → `/studio` → `/pricing` (and focus window) **without** hard refresh → `GET /api/v1/subscription/status` fires with `Cache-Control: private, no-store`, navbar hover card immediately shows `Paket: Premium 1 Hari` + countdown, `/pricing` immediately shows `Premium 1 Hari` + `Kuota meme: 0 / 10 (sisa 10)`, and `/studio` grants access (`Sisa kuota: 10 meme`).
   - **Quota update without hard refresh:** Increment meme count (`POST /api/v1/memes/upload` / meme completion) and navigate `/studio` → `/pricing` → `/studio` (or refocus window) **without** hard refresh → `GET /api/v1/subscription/status` refetches and UI immediately updates to `Kuota meme: 1 / 10 (sisa 9)` / `Sisa kuota: 9 meme`.
   - **Login transition:** Log out and log back in via `/login` as the subscribed user → `useLogin` invalidates `["me"]` and `["subscription"]`, `/studio` shows `Sisa kuota` without flashing the paywall, and the navbar hover card immediately shows `Paket: Premium 1 Hari`.

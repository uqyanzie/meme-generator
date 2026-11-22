# TKT-104 Fix Report: Stale Subscription State in Navbar and Packages Page Until Hard Refresh

- **Ticket:** [TKT-104](./TKT-104.md)
- **Plan:** [TKT-104-plan.md](./TKT-104-plan.md)
- **Layer:** Frontend (`web/`)
- **Status:** Resolved

---

## 1. Summary (Non-Engineer Friendly)

Previously, after a user purchased a subscription, logged in, or created/deleted a meme, the website's navigation bar, Packages (`/pricing`) page, and Studio (`/studio`) page continued showing outdated subscription or quota information until the user performed a hard browser refresh (`Ctrl+Shift+R`). Additionally, logged-out visitors triggered unnecessary background requests that resulted in `401` errors.

We fixed this by:
1. Removing the "never expire" client-side cache setting on subscription status so the app automatically re-checks with the server when navigating between pages or returning to the browser tab.
2. Ensuring login, registration, payment confirmation, meme completion, and meme deletion all immediately refresh the cached subscription and quota data.
3. Only fetching subscription status when a user is actually logged in, eliminating wasted `401` requests for logged-out visitors.

---

## 2. Root Cause (Technical)

Four compounding TanStack Query defects caused the stale state:
1. **Infinite `staleTime` (`web/src/lib/queries.ts`):** `useSubscriptionStatus()` configured `staleTime: Number.POSITIVE_INFINITY` (paired with global `refetchOnWindowFocus: false` in `providers.tsx`), so `["subscription"]` never refetched on SPA navigation or tab focus.
2. **Missing invalidation on auth mutations (`web/src/lib/auth.ts`):** `useRegister()` and `useLogin()` only invalidated `["me"]`, leaving stale `["subscription"]` and `["memes"]` entries cached.
3. **Missing invalidation on meme mutations (`web/src/lib/queries.ts`):** `useCompleteMeme()` and `useDeleteMeme()` only invalidated `["memes"]`, leaving `memesUsed` / `Sisa kuota` stale in `["subscription"]`.
4. **Unauthenticated polling (`web/src/lib/queries.ts`):** `useSubscriptionStatus()` ran unconditionally even when `useMe().data` was `undefined`, causing `2× 401` + `3× 400` refresh-token churn on public pages.

---

## 3. Changes Made

- **[`web/src/lib/query-keys.ts`](../../../web/src/lib/query-keys.ts) (new):** Extracted `queryKeys` (`me`, `plans`, `subscription`, `memes`) into a leaf module to prevent circular imports between `auth.ts` and `queries.ts`.
- **[`web/src/lib/auth.ts`](../../../web/src/lib/auth.ts):** Added `invalidateAuthQueries(qc)` invalidating `queryKeys.me`, `queryKeys.subscription`, and `["memes"]`; updated `useMe()`, `useRegister()`, and `useLogin()`.
- **[`web/src/lib/queries.ts`](../../../web/src/lib/queries.ts):** Re-exported `queryKeys`; added `subscriptionStatusQueryOptions(enabled)` with `staleTime: 0`, `refetchOnWindowFocus: true`, `retry: false`, and `enabled: Boolean(user)` in `useSubscriptionStatus()`; added `invalidateSubscriptionQuery(qc)` and `invalidateMemeQueries(qc)` used by `useConfirmPayment()`, `useCompleteMeme()`, and `useDeleteMeme()`.
- **[`web/src/app/studio/page.tsx`](../../../web/src/app/studio/page.tsx):** Updated loading guard to `me.isLoading || (me.data && status.isPending)` to prevent a single-frame paywall flash when `enabled` flips `false -> true`.
- **[`web/src/lib/subscription-cache.test.ts`](../../../web/src/lib/subscription-cache.test.ts) (new):** Added 5 unit/regression tests covering `subscriptionStatusQueryOptions`, `invalidateAuthQueries`, `invalidateSubscriptionQuery`, and `invalidateMemeQueries`.

---

## 4. Proof of Completion (Before vs. After)

- **Before ([before-browser-repro.txt](./evidence/before-browser-repro.txt)):**
  - Unauthenticated `/` fired `2× GET /api/v1/subscription/status (401)` + `3× POST /api/v1/auth/refresh (400)`.
  - After checkout + payment confirmation, SPA navigation `/pricing -> /studio -> / -> /pricing` and window focus fired **zero** `GET /api/v1/subscription/status` requests; `/studio` remained blocked and navbar showed `"Belum berlangganan"`.
- **After ([after-browser-repro.txt](./evidence/after-browser-repro.txt)):**
  - Unauthenticated `/` and `/pricing` fired **zero** `GET /api/v1/subscription/status` requests.
  - All 5 automated regression tests in `web/src/lib/subscription-cache.test.ts` pass, verifying `staleTime: 0`, `refetchOnWindowFocus: true`, `enabled` gating, and `queryKeys.subscription` invalidation across auth, payment, and meme mutations.

---

## 5. Regression Checks

- `cd web && bun test`: **5 pass, 0 fail**
- `cd web && bun run check` (`tsc --noEmit`): **Pass (exit 0)**
- `cd web && bun run lint` (`next lint`): **Pass (exit 0)**
- `cd api && go vet ./...`: **Pass (exit 0)**
- `cd api && go test ./...`: **Pass (exit 0)** *(note: `-race` requires `CGO_ENABLED=1` / gcc on Windows)*

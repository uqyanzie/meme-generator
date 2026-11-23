# TKT-106 Fix Plan: Unresponsive "Pay Now" Button Without Feedback

- **Ticket:** [TKT-106](./TKT-106.md)
- **Layer:** Frontend (+ Backend verification tests)
- **Phase:** EDD Phase 1 — Plan Ready for Review

---

## Root Cause Analysis

1. **Silent Error Swallowing (`web/src/app/checkout/[reference]/page.tsx`, lines 33–40):**
   - `onPay` wraps `await confirm.mutateAsync(reference)` in a `try { ... } catch {}` block with an empty `catch` (explicitly marked `// BUG: error sengaja ditelan — gagal bayar tidak menampilkan apa-apa.`).
   - Any failure from `POST /api/v1/subscription/payments/{reference}/confirm` (HTTP `402 Payment Required` for invalid/already-paid references, `5xx`, or network/connection errors) is silently discarded, and `<CardContent>` (lines 51–61) renders no UI for `confirm.isError` / `confirm.error`.

2. **Missing Loading State & Duplicate-Click Guard (`web/src/app/checkout/[reference]/page.tsx`, lines 33–40, 63–65):**
   - `<Button className="flex-1" onClick={onPay}>` has no `disabled` prop and no pending label.
   - Clicking rapidly fires multiple concurrent `POST .../confirm` requests. Because backend `ConfirmPayment` (`api/internal/modules/subscription/repository.go`, lines 160–199) strictly updates only `WHERE reference = $1 AND status = 'pending'`, a second click while in-flight or during the 1.2s post-success redirect window triggers `402 ErrPaymentFailed` and can clobber the mutation state.
   - `setTimeout(() => router.push("/pricing"), 1200)` (line 36) is not cleaned up on component unmount, risking a stale redirect if the user clicks "Batal" before the timer fires.

3. **Missing HTTP `402` Label in `describeError` (`web/src/lib/errors.ts`, lines 14–27):**
   - The `label` map covers `400`, `401`, `403`, `404`, `405`, `409`, `413`, `422`, `429`, `500`, `502`, `503`, but omits `402`, causing `402` payment failures to display raw `"HTTP 402: pembayaran gagal"` instead of a localized label.

4. **Backend Verification (`api/internal/modules/subscription/handler.go` L71–L86, `service.go` L80–L83, `repository.go` L160–L199):**
   - `ConfirmPayment` is already atomic in a single transaction: it transitions `payments.status` from `'pending'` to `'paid'` (`paid_at = now()`), activates `subscriptions` (`status = 'active'`, `starts_at = now()`, `ends_at = now() + duration_days`), and returns `ErrPaymentFailed` (`402 Payment Required`, `code: 40004`) if no pending row matches. No backend production code defect was found, but `ConfirmPayment` / `Handler.Confirm` currently have no automated regression tests.

---

## Before Reproduction Evidence

- Raw verbatim capture: [evidence/before-browser-repro.txt](./evidence/before-browser-repro.txt)
- **Summary of observed defect:**
  - Opened `http://localhost:3000/checkout/nonexistent-ref-tkt106`.
  - Clicked `"Bayar Sekarang (Simulasi)"` 3 times in quick succession.
  - All 3 clicks fired duplicate `POST http://localhost:8080/api/v1/subscription/payments/nonexistent-ref-tkt106/confirm` requests (all failing with `net::ERR_CONNECTION_REFUSED`).
  - The button never disabled (`disabled: false` throughout), never changed text to a loading state, and zero error or alert UI was rendered in `<main>`.

---

## Agreed Decisions

Recorded in [TKT-106.md](./TKT-106.md):

1. **Backend `Confirm` contract:** Keep `POST /api/v1/subscription/payments/{reference}/confirm` strict (`402 ErrPaymentFailed` when reference is missing or not `pending`). Do not alter the backend API contract; add automated regression tests covering happy path, double-confirm, unknown reference, and concurrent confirms.
2. **Frontend UX & post-confirm navigation:**
   - Render inline error feedback via `describeError(confirm.error)` with `role="alert"`.
   - Disable the pay button while `confirm.isPending || Boolean(confirm.data)` with `"Memproses..."` while pending and `"Lunas"` once succeeded; keep the button enabled for retry on error alongside the `/pricing` back link.
   - Keep the 1.2s auto-redirect to `/pricing` on success, with proper `useEffect` unmount cleanup for the timer.

---

## Minimal Fix Design

### 1. `web/src/app/checkout/[reference]/page.tsx`
- Import `useEffect`, `useRef` from `"react"` and `describeError` from `"@/lib/errors"`.
- Track the redirect timeout in `redirectTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null)` and clear it in a `useEffect` cleanup on unmount.
- Update `onPay`:
  - Guard early: `if (confirm.isPending || confirm.data) return;`.
  - Call `confirm.mutate(reference, { onSuccess: () => { ... } })` instead of `try { await confirm.mutateAsync(reference); } catch {}` so TanStack Query cleanly records `confirm.isError` / `confirm.error` without swallowing errors or producing unhandled rejections.
  - Schedule `router.push("/pricing")` after `1200ms` via `redirectTimerRef.current`.
- Render inline error alert inside `<CardContent>` when `confirm.isError` is true:
  ```tsx
  {confirm.isError ? (
    <p
      role="alert"
      className="rounded-md border border-destructive/30 bg-destructive/10 px-3 py-2 text-sm text-destructive"
    >
      {describeError(confirm.error)}
    </p>
  ) : null}
  ```
- Update primary `<Button>` in `<CardFooter>`:
  - `disabled={confirm.isPending || Boolean(confirm.data)}`
  - `aria-busy={confirm.isPending}`
  - Label: `confirm.data ? "Lunas" : confirm.isPending ? "Memproses..." : "Bayar Sekarang (Simulasi)"`
- Keep `<Button asChild variant="ghost"><Link href="/pricing">Batal</Link></Button>` (or `"Kembali ke Pricing"` when errored/paid) so users always have an explicit escape hatch back to `/pricing`.

### 2. `web/src/lib/errors.ts`
- Add `402: "Pembayaran tidak dapat diproses"` to the `label` map in `describeError` so HTTP `402` responses from `POST /api/v1/subscription/payments/{reference}/confirm` render `"Pembayaran tidak dapat diproses: pembayaran gagal"` instead of `"HTTP 402: pembayaran gagal"`.

### 3. Backend Regression Tests (No Production Code Changes)
- `api/internal/modules/subscription/repository_test.go`:
  - `TestConfirmPayment_ActivatesSubscriptionAndRejectsDoubleConfirm`: verifies first confirm transitions payment to `'paid'` and subscription to `'active'` with valid `starts_at`/`ends_at`, and second confirm on the same reference returns `ErrPaymentFailed`.
  - `TestConfirmPayment_RejectsUnknownReference`: verifies unknown reference returns `ErrPaymentFailed`.
  - `TestConfirmPayment_ConcurrentConfirms_OnlyOneSucceeds`: launches 10 concurrent goroutines confirming the same pending reference; asserts exactly 1 succeeds and 9 return `ErrPaymentFailed` (verified under `go test -race`).
- `api/internal/modules/subscription/mount_test.go`:
  - `TestRegisterRoutes_ConfirmPayment_HTTPStatusCodes`: verifies HTTP `200 OK` (`CodePaymentConfirmed`) on valid pending reference and HTTP `402 Payment Required` (`CodePaymentFailed`) on duplicate/unknown reference.

---

## Edge Cases & Regression Risks

- **Rapid double-click during in-flight request:** Mitigated by `disabled={confirm.isPending || Boolean(confirm.data)}` plus early return in `onPay`.
- **Click during the 1.2s post-success redirect window:** Mitigated by `Boolean(confirm.data)` keeping the button disabled so a late click cannot trigger a `402` and overwrite the success banner.
- **Navigation away before redirect fires:** Mitigated by clearing `redirectTimerRef.current` on unmount.
- **Retry after transient error:** On error, `confirm.isPending` resets to `false` and `confirm.data` remains `undefined`, so the button re-enables for retry; clicking again starts a fresh mutation and clears the previous error once pending/succeeded.
- **Stale subscription state on `/pricing` after redirect:** `useConfirmPayment` in `web/src/lib/queries.ts` (L87) already invalidates `queryKeys.subscription` in `onSuccess` (fixed in TKT-104).

---

## Verification Plan

### 1. Automated Regression Tests (Red → Green in `edd-implement`)
- Add the 4 Go tests in `api/internal/modules/subscription/repository_test.go` and `api/internal/modules/subscription/mount_test.go`.
- Run:
  ```powershell
  cd api && go vet ./... && go test -race ./... && go build -o bin/api ./cmd/server
  ```

### 2. Frontend Typecheck & Lint Gates
- Run:
  ```powershell
  cd web && bun run check && bun run lint
  ```

### 3. Live "After" Verification (Mirroring Step 2)
- **Failure path (invalid/already-paid reference or API down):**
  - Open `http://localhost:3000/checkout/nonexistent-ref-tkt106` and click `"Bayar Sekarang (Simulasi)"`.
  - Verify button disables and shows `"Memproses..."` while in-flight, renders a visible `role="alert"` message from `describeError` on failure, and re-enables for retry alongside the `/pricing` link.
- **Happy path (full stack running with Docker Postgres + Go API + Next.js Web):**
  - Complete a checkout from `/pricing` → `/checkout/<reference>`.
  - Click `"Bayar Sekarang (Simulasi)"` rapidly: verify only 1 `POST .../confirm` (`200 OK`) fires, button transitions `"Memproses..."` → `"Lunas"` (disabled), success banner appears, and page redirects to `/pricing` showing the active subscription.
  - Re-visit `/checkout/<reference>` and click `"Bayar Sekarang (Simulasi)"`: verify `402` response renders `"Pembayaran tidak dapat diproses: pembayaran gagal"` in the `role="alert"` banner.
- Save raw "After" evidence under `docs/issues/TKT-106/evidence/after-browser-repro.txt` (and `after-api-repro.txt` if full stack is up).

# TKT-106 Fix Report: Unresponsive "Pay Now" Button Without Feedback

- **Ticket:** [TKT-106](./TKT-106.md)
- **Fix Plan:** [TKT-106-plan.md](./TKT-106-plan.md)
- **Layer:** Frontend (+ Backend regression tests)
- **Status:** Resolved

---

## Summary (Non-Engineer Friendly)

Previously, clicking **"Bayar Sekarang (Simulasi)"** on the checkout page gave no visual feedback: the button stayed enabled, showed no loading indicator, and silently ignored any payment errors—leaving users unsure whether their payment was being processed or had failed.

Now, clicking **"Bayar Sekarang (Simulasi)"** immediately disables the button and shows **"Memproses..."** to prevent duplicate submissions, displays **"Lunas"** with a confirmation banner and redirects to `/pricing` on success, and renders a clear, localized error alert if the payment fails (while keeping the button enabled for retry and providing a **"Batal"** link back to `/pricing`).

---

## Root Cause (Technical)

1. **Silent Error Swallowing (`web/src/app/checkout/[reference]/page.tsx`):** `onPay` wrapped `await confirm.mutateAsync(reference)` in an empty `try { ... } catch {}` block, discarding all `402`, `5xx`, and network errors, and `<CardContent>` had no UI for `confirm.isError`.
2. **Missing Loading State & Duplicate-Click Guard (`web/src/app/checkout/[reference]/page.tsx`):** `<Button>` had no `disabled` prop or pending label, allowing rapid clicks to fire duplicate `POST /api/v1/subscription/payments/{reference}/confirm` requests, and `setTimeout` was not cleaned up on unmount.
3. **Missing HTTP `402` Label (`web/src/lib/errors.ts`):** `describeError` lacked a `402` entry in its `label` map.

---

## Changes Made

- **`web/src/app/checkout/[reference]/page.tsx`:**
  - Replaced `try { await confirm.mutateAsync(reference); } catch {}` with `confirm.mutate(reference, { onSuccess })` guarded by `if (confirm.isPending || confirm.data) return;`.
  - Added `redirectTimerRef` with `useEffect` unmount cleanup for the 1.2s post-success redirect to `/pricing`.
  - Rendered inline `role="alert"` error banner using `describeError(confirm.error)` when `confirm.isError` is true.
  - Added `disabled={confirm.isPending || Boolean(confirm.data)}`, `aria-busy={confirm.isPending}`, and `"Memproses..."` / `"Lunas"` button states.
- **`web/src/lib/errors.ts`:**
  - Added `402: "Pembayaran tidak dapat diproses"` to `describeError`.
- **`api/internal/modules/subscription/repository_test.go` & `mount_test.go`:**
  - Added 4 Go regression tests (`TestConfirmPayment_ActivatesSubscriptionAndRejectsDoubleConfirm`, `TestConfirmPayment_RejectsUnknownReference`, `TestConfirmPayment_ConcurrentConfirms_OnlyOneSucceeds`, `TestRegisterRoutes_ConfirmPayment_HTTPStatusCodes`).

---

## Proof of Completion (Before vs. After)

- **Before** ([evidence/before-browser-repro.txt](./evidence/before-browser-repro.txt)):
  - Clicking `"Bayar Sekarang (Simulasi)"` 3 times on `/checkout/nonexistent-ref-tkt106` fired 3 duplicate `POST .../confirm` requests, kept the button enabled, and rendered zero error/alert UI in `<main>`.
- **After** ([evidence/after-browser-repro.txt](./evidence/after-browser-repro.txt)):
  - Clicking `"Bayar Sekarang (Simulasi)"` on `/checkout/nonexistent-ref-tkt106` rendered a `role="alert"` banner (`"Tidak bisa terhubung ke server API — pastikan backend berjalan"`) and kept the button + `"Batal"` link available for retry/escape.

---

## Regression Checks

- **Backend:** `go vet ./...`, `go test ./...` (including all 4 new `TKT-106` tests in `internal/modules/subscription`), and `go build -o bin/api ./cmd/server` — **all passed**.
- **Frontend:** `bun run check` (`tsc --noEmit`) and `bun run lint` (`next lint`) — **all passed** (exit code 0).

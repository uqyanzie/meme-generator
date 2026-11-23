# E2E Smoke Verification Report

- **Commit (`HEAD`):** `2c63732`
- **Database:** PostgreSQL on `localhost:5433` (`meme_portal`), migrated to version `6` (`000001`–`000006`)
- **API:** `http://localhost:8080/api/v1` (`/health` → `200 ok`)
- **Web:** `http://localhost:3000` (Next.js dev server)

## 1. Browser E2E Verification (`http://localhost:3000`)

| Step | Ticket(s) | Observed Result | Status |
| :--- | :--- | :--- | :--- |
| Register (`/register`) | `TKT-103`, `TKT-104` | Registered `e2e-browser-1763926660@example.com`; navbar immediately showed `Info akun` and `Keluar` | Pass |
| Checkout (`/pricing`) | `TKT-105`, `TKT-106` | Clicked `Berlangganan` on `Premium 1 Hari`; button showed `Memproses...` and redirected to `/checkout/ca961707abbdee612ac2f5a7` | Pass |
| Payment (`/checkout/[reference]`) | `TKT-106` | Clicked `Bayar Sekarang (Simulasi)`; button changed to `Lunas` (disabled), success banner displayed, redirected to `/pricing` | Pass |
| Immediate UI sync (no hard refresh) | `TKT-104` | `/pricing` immediately showed `Premium 1 Hari` + `Kuota meme: 0 / 10 (sisa 10)`; `/studio` showed `Sisa kuota: 10 meme` & `Buat Meme` form | Pass |
| Logout (`Keluar`) | `TKT-103` | Cleanly navigated to `/` with `Masuk` and `Daftar` restored and no `/login` ↔ `/` redirect loop | Pass |

## 2. API E2E Smoke Verification (`http://localhost:8080/api/v1`)

| Check | Ticket(s) | Observed Result | Status |
| :--- | :--- | :--- | :--- |
| DB migrations `000001`–`000006` | `TKT-105`, `TKT-109` | `migrate version` = `6` (`no change`) | Pass |
| `GET /api/v1/health` | — | `{"code":0,"message":"ok","success":true}` | Pass |
| `GET /api/v1/subscription/status` headers | `TKT-102` | `Cache-Control: private, no-store`, `Vary: Cookie`, `Vary: Authorization` | Pass |
| `POST /api/v1/subscription/checkout` deduplication | `TKT-105` | Same & different `Idempotency-Key` returned identical `reference` | Pass |
| `POST /api/v1/auth/logout` & revoked token | `TKT-103` | Logout returned `HTTP 200`; reusing old cookie returned `HTTP 401` | Pass |

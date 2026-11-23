# Laporan Perbaikan — Meme Portal

Ringkasan terpadu untuk tim engineering maupun non-engineering terkait isu produksi yang telah diselesaikan (`TKT-101` – `TKT-109`).

---

## TKT-103: Alur Logout Rusak dan Infinite Redirect Loop

- **Status:** Selesai
- **Laporan Detail:** [docs/issues/TKT-103/TKT-103-fix-report.md](./docs/issues/TKT-103/TKT-103-fix-report.md)
- **Lapisan:** Backend + Frontend

### Ringkasan
Mengklik **Keluar (Logout)** sebelumnya gagal menghapus cookie autentikasi (menggunakan `Path=/api/v1/auth` alih-alih `/`), membiarkan access token tetap valid hingga kedaluwarsa, dan membiarkan cookie non-`HttpOnly` dapat dibaca oleh JavaScript. Digabungkan dengan halaman `/login` & `/register` yang melakukan redirect berdasarkan keberadaan `document.cookie` mentah serta `/` yang mengalihkan pengguna yang belum terautentikasi ke `/login`, pengguna terjebak dalam infinite redirect loop `/login` ↔ `/`.

### Solusi
- **Backend:** Mengatur `HttpOnly: true` pada `cookie.Set`; menghapus kedua cookie autentikasi pada `Path: "/"` melalui `cookie.Clear`; menambahkan klaim `jti` unik serta denylist access token di Redis (`auth:revoked:access:<jti>`) yang diperiksa pada `VerifyAccess(ctx, raw)`; menghapus `authMW` dari `POST /api/v1/auth/logout` agar proses logout bersifat idempoten dan selalu menghapus cookie.
- **Frontend:** Menghapus redirect paksa ke `/login` dari `/`; mengganti redirect berbasis `document.cookie` di `/login` dan `/register` dengan pengecekan `useMe()` yang aman; melakukan invalidasi `["me"]` pada `useRegister`; memindahkan pembersihan state pada `useLogout` ke `onSettled`.
- **Verifikasi:** Menambahkan uji regresi Go (`cookie_test.go`, `jwt_test.go`, `handler_test.go`), lulus `go vet`, `go test`, `go build`, `bun run check`, `bun run lint`, serta memverifikasi perilaku API secara langsung Sebelum vs. Sesudah perbaikan (`docs/issues/TKT-103/evidence/after-api-repro.txt`).

---

## TKT-102: Status Langganan Tidak Konsisten dan Kebocoran Data Antar-Pengguna

- **Status:** Selesai
- **Laporan Detail:** [docs/issues/TKT-102/TKT-102-fix-report.md](./docs/issues/TKT-102/TKT-102-fix-report.md)
- **Lapisan:** Backend

### Ringkasan
`GET /api/v1/subscription/status` sebelumnya mengirimkan header `Cache-Control: public, max-age=86400` tanpa membedakan respons berdasarkan kredensial autentikasi (hanya `Vary: Origin`). Hal ini memungkinkan shared cache (seperti proxy Wi-Fi kantor/CDN) membocorkan status langganan dan kuota milik satu pengguna ke pengguna lain—termasuk pengunjung yang belum login—serta menyebabkan browser pengguna menyajikan respons usang `"subscribed": false` dari HTTP disk cache meskipun pengguna sudah membayar dan membuka ulang tab.

### Solusi
- **Backend:** Menghapus pembungkus inline `Cache-Control: public, max-age=86400` dari `GET /api/v1/subscription/status` pada `api/internal/modules/subscription/mount.go`; menerapkan `Cache-Control: private, no-store` dan menambahkan `Vary: Cookie` serta `Vary: Authorization` di awal `middleware.Auth` (`api/internal/middleware/auth.go`), sehingga melindungi seluruh endpoint terautentikasi (termasuk respons `401`) sambil mempertahankan `Vary: Origin` dari `middleware.CORS` dan tidak memengaruhi rute publik.
- **Verifikasi:** Menambahkan uji regresi Go (`api/internal/middleware/auth_test.go`, `api/internal/modules/subscription/mount_test.go`), lulus `go vet`, `go test`, `go build`, `bun run check`, `bun run lint`, serta memverifikasi perilaku API secara langsung Sebelum vs. Sesudah perbaikan (`docs/issues/TKT-102/evidence/after-api-repro.txt`).

---

## TKT-104: Status Langganan Usang di Navbar dan Halaman Paket Hingga Hard Refresh

- **Status:** Selesai
- **Commit:** `263fdfd`
- **Laporan Detail:** [docs/issues/TKT-104/TKT-104-fix-report.md](./docs/issues/TKT-104/TKT-104-fix-report.md)
- **Lapisan:** Frontend

### Ringkasan
`useSubscriptionStatus` menetapkan `staleTime: Number.POSITIVE_INFINITY` dan tetap berjalan tanpa syarat meskipun pengguna sudah logout; selain itu, `useLogin`, `useRegister`, `useCompleteMeme`, dan `useDeleteMeme` tidak melakukan invalidasi terhadap `queryKeys.subscription`.

### Solusi
- **Frontend:** Memisahkan `queryKeys` ke `web/src/lib/query-keys.ts`, mengonfigurasi `useSubscriptionStatus` dengan `staleTime: 0`, `refetchOnWindowFocus: true`, `retry: false`, dan `enabled: Boolean(user)`, menambahkan invalidasi `queryKeys.subscription` pada mutasi autentikasi dan meme, serta menambahkan guard pada `/studio` menggunakan `me.isLoading || (me.data && status.isPending)`.
- **Verifikasi:** Menambahkan `web/src/lib/subscription-cache.test.ts` (5/5 lulus), lulus `bun run check`, `bun run lint`, serta memverifikasi state browser secara langsung Sebelum vs. Sesudah perbaikan (`docs/issues/TKT-104/evidence/after-browser-repro.txt`).

---

## TKT-101: Pembuatan Meme Tetap Diizinkan Setelah Langganan Kedaluwarsa

- **Status:** Selesai
- **Laporan Detail:** [docs/issues/TKT-101/TKT-101-fix-report.md](./docs/issues/TKT-101/TKT-101-fix-report.md)
- **Lapisan:** Backend

### Ringkasan
`Repository.ActiveByUser` sebelumnya menjalankan kueri `WHERE s.user_id = $1 AND s.status = 'active'` tanpa memeriksa `s.ends_at > now()`. Di antara waktu kedaluwarsa hingga eksekusi `expireJanitor` (`MarkExpired`) per jam berikutnya, langganan yang sudah berakhir masih dianggap aktif—sehingga pembuatan meme tetap diizinkan, API melaporkan `subscribed: true`, dan checkout perpanjangan terblokir dengan `409 Conflict`.

### Solusi
- **Backend:** Menambahkan kondisi `AND s.ends_at > now()` pada `Repository.ActiveByUser` di `api/internal/modules/subscription/repository.go`.
- **Verifikasi:** Menambahkan uji regresi Go (`api/internal/modules/subscription/repository_test.go`, `api/internal/server/v1/v1_test.go`), lulus `go vet`, `go test`, `go build`, `bun run check`, `bun run lint`, serta memverifikasi perilaku API secara langsung Sebelum vs. Sesudah perbaikan (`docs/issues/TKT-101/evidence/after-api-repro.txt`).

---

## TKT-105: Duplikasi Checkout / Pembuatan Tagihan Ganda

- **Status:** Selesai
- **Laporan Detail:** [docs/issues/TKT-105/TKT-105-fix-report.md](./docs/issues/TKT-105/TKT-105-fix-report.md)
- **Lapisan:** Backend + Frontend

### Ringkasan
`useCheckout()` menghasilkan `Idempotency-Key` baru pada setiap klik, antarmuka halaman Pricing tidak menonaktifkan pengiriman yang sedang berlangsung, `Service.Checkout` melakukan pengecekan idempotensi secara non-atomik (menyebabkan `500` saat key yang sama dikirim bersamaan) serta tidak memiliki deduplikasi tagihan `pending` per `(user_id, plan_id)`, dan CORS preflight belum menyertakan `Idempotency-Key`.

### Solusi
- **Frontend:** Menambahkan penggunaan ulang `Idempotency-Key` per paket serta guard tombol/navigasi `isBusy` pada `web/src/app/pricing/page.tsx` & `web/src/lib/queries.ts`.
- **Backend:** Menambahkan `Idempotency-Key` ke `Access-Control-Allow-Headers` pada `api/internal/middleware/cors.go`; menambahkan partial unique index `subscriptions_user_plan_pending_idx` (`000006`), metode `PendingCheckoutByUserAndPlan`, serta pemulihan replay `SQLSTATE 23505` di `api/internal/modules/subscription/`.
- **Verifikasi:** Menambahkan uji regresi Go (`api/internal/middleware/cors_test.go`, `api/internal/modules/subscription/repository_test.go`), lulus `go vet`, `go test -p 1`, `go build`, `bun run check`, `bun run lint`, serta memverifikasi perilaku API & browser secara langsung Sebelum vs. Sesudah perbaikan (`docs/issues/TKT-105/evidence/after-api-repro.txt`, `docs/issues/TKT-105/evidence/after-browser-repro.txt`).

---

## TKT-106: Tombol "Bayar Sekarang" Tidak Responsif Tanpa Umpan Balik

- **Status:** Selesai
- **Laporan Detail:** [docs/issues/TKT-106/TKT-106-fix-report.md](./docs/issues/TKT-106/TKT-106-fix-report.md)
- **Lapisan:** Frontend (+ Uji Regresi Backend)

### Ringkasan
Mengklik **Bayar Sekarang (Simulasi)** pada `/checkout/[reference]` sebelumnya menelan seluruh error dalam blok `catch {}` kosong, tidak memiliki state `disabled` maupun indikator pemuatan, dan belum mencantumkan kode status `402` di `describeError`.

### Solusi
- **Frontend:** Mengganti `mutateAsync` + `catch {}` kosong dengan `confirm.mutate` + pembersihan timer pada `onSuccess`, menambahkan `disabled={confirm.isPending || Boolean(confirm.data)}` beserta status `"Memproses..."` / `"Lunas"`, menampilkan umpan balik error `role="alert"` melalui `describeError(confirm.error)`, dan menambahkan `402: "Pembayaran tidak dapat diproses"` di `web/src/lib/errors.ts`.
- **Verifikasi:** Menambahkan 4 uji regresi Go di `api/internal/modules/subscription/` (`repository_test.go`, `mount_test.go`), lulus `go vet`, `go test`, `go build`, `bun run check`, `bun run lint`, serta memverifikasi tampilan error `role="alert"` secara langsung (`docs/issues/TKT-106/evidence/after-browser-repro.txt`).

---

## TKT-107: Akses dan Penghapusan Meme Milik Pengguna Lain Secara Tidak Sah (IDOR)

- **Status:** Selesai
- **Laporan Detail:** [docs/issues/TKT-107/TKT-107-fix-report.md](./docs/issues/TKT-107/TKT-107-fix-report.md)
- **Lapisan:** Backend

### Ringkasan
Pengguna terautentikasi mana pun sebelumnya dapat menimpa URL hasil meme milik pengguna lain (`POST /api/v1/memes/{id}/complete`) atau menghapus meme milik pengguna lain (`DELETE /api/v1/memes/{id}`) karena `Repository.GetByID`, `Repository.SetResultURL`, dan `Repository.Delete` mengabaikan `userID`. Selain itu, `GetByID` memilih 10 kolom ke dalam 9 argumen `Scan`, sehingga mematahkan `POST /api/v1/memes/{id}/result-upload` dengan error `500` untuk semua pengguna.

### Solusi
- **Backend:** Menerapkan `WHERE id = $1 AND user_id = $2` (serta `$3` untuk `SetResultURL`) di ketiga metode repositori, memperbaiki `Scan` 9 kolom pada `GetByID`, memetakan UUID yang tidak valid (`22P02`) ke `404 Not Found`, dan menambahkan pembersihan objek Garage S3 secara best-effort di `Service.Delete`.
- **Verifikasi:** Menambahkan uji regresi Go (`api/internal/modules/meme/repository_test.go`, `api/internal/modules/meme/service_test.go`), lulus `go vet`, `go test`, `go build`, `bun run check`, `bun run lint`, serta memverifikasi perilaku API dua pengguna dan pembersihan S3 secara langsung Sebelum vs. Sesudah perbaikan (`docs/issues/TKT-107/evidence/after-api-repro.txt`).

---

## TKT-108: Unggahan File Sembarang / Berbahaya ke Penyimpanan S3

- **Status:** Selesai
- **Laporan Detail:** [docs/issues/TKT-108/TKT-108-fix-report.md](./docs/issues/TKT-108/TKT-108-fix-report.md)
- **Lapisan:** Backend + Frontend

### Ringkasan
Pelanggan sebelumnya dapat mengunggah file sembarang, berukuran berlebih, atau berbahaya (`.html`, `.exe`, `.zip`, blob 6 MiB+) langsung ke Garage S3 karena `Service.PrepareUpload` tidak pernah memanggil `allowedExt`, memercayai `contentType` mentah dari klien, dan baik `PrepareUpload` maupun `ResultUpload` tidak memvalidasi `fileSize` ataupun menandatangani `ContentLength` ke dalam presigned PUT URL S3.

### Solusi
- **Backend:** Menerapkan daftar izin ekstensi kanonis (`.jpg`, `.jpeg`, `.png`, `.gif`, `.webp`), pencocokan ketat ekstensi-ke-MIME, dan batas ukuran file 5 MiB (`5 << 20`) pada `PrepareUpload` maupun `ResultUpload` (dipetakan ke `400` / `30002` dan `413` / `30003`, divalidasi sebelum insert DB), serta menandatangani `ContentLength` dan `ContentType` ke dalam presigned PUT URL Garage S3.
- **Frontend:** Menambahkan validasi sisi klien yang selaras serta payload `contentType`/`fileSize` pada `web/src/lib/queries.ts` dan `web/src/app/studio/page.tsx`.
- **Verifikasi:** Menambahkan uji regresi Go (`api/internal/modules/meme/handler_test.go`, `api/internal/modules/meme/service_test.go`), lulus `go vet`, `go build`, `bun run check`, `bun run lint`, serta memverifikasi perilaku API dan penolakan manipulasi SigV4 Garage S3 secara langsung Sebelum vs. Sesudah perbaikan (`docs/issues/TKT-108/evidence/after-api-repro.txt`).

---

## TKT-109: Pelampauan Kuota Melalui Permintaan Bersamaan (Race Condition)

- **Status:** Selesai
- **Commit:** `2c63732`
- **Laporan Detail:** [docs/issues/TKT-109/TKT-109-fix-report.md](./docs/issues/TKT-109/TKT-109-fix-report.md)
- **Lapisan:** Backend

### Ringkasan
Permintaan `PrepareUpload` yang dikirim bersamaan sebelumnya dapat melampaui kuota meme per paket akibat race condition TOCTOU antara pengecekan langganan aktif/kuota dengan penyisipan baris meme, serta `ResultUpload`/`Complete` terblokir pada meme terakhir yang diizinkan.

### Solusi
- **Backend:** Memeriksa langganan aktif dan kuota meme pengguna di dalam transaksi PostgreSQL menggunakan `FOR UPDATE OF s` secara atomik bersama `INSERT INTO memes` pada `PrepareUpload`; memisahkan `meme.Gate` agar `ResultUpload` dan `Complete` hanya memverifikasi langganan aktif (`HasActiveSubscription`).
- **Verifikasi:** Lulus `go vet ./...`, `go test ./...` (termasuk `TestMemeQuota_ConcurrentPrepareUploadEnforcesLimit`, `TestMemeQuota_FinalAllowedMemeCanResultUploadAndComplete`, `TestMemeQuota_UnlimitedPlanAllowsBeyondStandardLimits`), `go build`, `bun run check`, `bun run lint`, serta reproduksi API langsung Sebelum/Sesudah (`10/20` HTTP 201 pada unggahan bersamaan, `HTTP 200` pada `result-upload` & `complete` untuk meme ke-10).

---

## Verifikasi Terintegrasi Akhir

Diverifikasi pada `HEAD` terintegrasi (`2c63732`) — keluaran lengkap tercatat di [docs/verification/automated-suite.txt](./docs/verification/automated-suite.txt):

- **Backend (`api/`):**
  - `go vet ./...` — lulus
  - `go test -count=1 ./...` — lulus (`middleware`, `auth`, `meme`, `subscription`, `cookie`, `token`, `v1`)
  - `go build -o bin/api ./cmd/server` — lulus
- **Frontend (`web/`):**
  - `bun test` — lulus (5/5 pada `src/lib/subscription-cache.test.ts`)
  - `bun run check` (`tsc --noEmit`) — lulus
  - `bun run lint` (`next lint`) — lulus (1 peringatan `unused-var` bawaan pada `formatIdr` di `src/app/checkout/[reference]/page.tsx`)
  - `bun run build` (`next build`) — lulus (seluruh 7 rute berhasil dikompilasi dan dibangun)

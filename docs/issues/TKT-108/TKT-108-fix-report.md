# TKT-108 Fix Report: Arbitrary / Malicious File Uploads in S3 Storage

## Summary (Non-Engineer Friendly)

Previously, any logged-in subscriber could upload dangerous or massive non-image files (such as `.html` scripts, `.exe` executables, `.zip` archives, or multi-gigabyte files) straight into our S3 file storage. This created both a security risk (hosting malicious pages or files on our storage domain) and a storage cost risk.

We have now locked down the upload flow on both the server and the web studio:
- Only valid raster images (`.jpg`, `.jpeg`, `.png`, `.gif`, `.webp`) whose file type matches their extension are accepted.
- Every upload (both the original image and the rendered meme PNG) is capped at **5 MB**.
- The upload links issued by the server now cryptographically lock both the exact file size and the file type, so even if someone bypasses the web app and tries to tamper with the upload directly to S3, storage immediately rejects it with `403 AccessDenied`.

---

## Root Cause (Technical)

Four compounding defects allowed arbitrary and oversized files into Garage S3:
1. **Unused extension allowlist (`api/internal/modules/meme/service.go`):** `allowedExt(ext)` was defined but never called in `Service.PrepareUpload`, allowing any extension (`.html`, `.exe`, `.zip`, `.svg`, or empty) into `objectKey`.
2. **Unvalidated `contentType` (`api/internal/modules/meme/service.go`):** Client-supplied `contentType` was passed verbatim to `s3.PutObjectInput.ContentType` without allowlist or extension-to-MIME consistency checks.
3. **No file size limit or signed `Content-Length` (`api/internal/modules/meme/handler.go`, `api/internal/modules/meme/service.go`):** Neither `PrepareUpload` nor `ResultUpload` accepted `fileSize` or set `PutObjectInput.ContentLength`. Presigned URLs only signed `content-type;host`, permitting arbitrarily large uploads directly to Garage S3.
4. **Incomplete frontend validation & payload (`web/src/lib/queries.ts`, `web/src/app/studio/page.tsx`):** `usePrepareUpload` omitted `contentType` and `fileSize`, `ResultUpload` sent no body, and `<Input type="file" accept="image/*" />` lacked extension/MIME/size checks.

---

## Changes Made

- **Backend (`api/internal/modules/meme/`):**
  - **`consts.go`:** Added `maxUploadBytes = int64(5 << 20)` (5 MiB).
  - **`errors.go`:** Added `ErrFileTooLarge` and `ErrInvalidUploadToken`.
  - **`upload_token.go`:** Added HMAC-SHA256 signed, expiring capability tokens encoding `objectKey`, canonical `MIME`, `maxBytes` (5 MiB), optional exact `fileSize`, and `exp`.
  - **`service.go`:**
    - Added canonical `extToMIME` mapping, `validateOptionalFileSize`, and `validateImageUpload` (normalizing extension & MIME via `mime.ParseMediaType`, enforcing `allowedExt`, inferring canonical MIME when `contentType` is omitted while rejecting mismatched/disallowed MIME types, and enforcing `0 < fileSize <= 5 MiB` whenever `fileSize` is supplied).
    - Updated `PrepareUpload` and `ResultUpload` to validate before DB writes and issue signed backend upload URLs (`/api/v1/memes/upload-object?token=...`), preserving backward compatibility for API clients that omit `fileSize`, `contentType`, or request body on `ResultUpload` while still strictly enforcing the 5 MiB cap and MIME match at upload time.
    - Added `VerifyUploadToken` and `PutObject` to stream validated payloads into Garage S3 with explicit `ContentLength` and canonical `ContentType`.
  - **`handler.go` & `mount.go`:**
    - Extended `prepareUploadDTO` and `resultUploadDTO` with optional `fileSize` / `size` / `file_size`, and allowed empty body (`io.EOF`) on `ResultUpload`.
    - Added `PUT /api/v1/memes/upload-object` (`Handler.UploadObject`) that verifies the HMAC token, rejects mismatched `Content-Type` (`403`), rejects oversized `Content-Length` or streamed bodies via `http.MaxBytesReader` (`413` / `response.CodeUploadTooLarge`) even when `fileSize` was omitted during prepare (blocking 11 MB uploads before touching S3), and rejects empty payloads (`400`).
    - Mapped `ErrFileTooLarge` in `writeError` to `HTTP 413 Request Entity Too Large` (`response.CodeUploadTooLarge = 30003`) and `ErrInvalidFile` to `HTTP 400` (`response.CodeInvalidImage = 30002`).
  - **`service_test.go` & `handler_test.go`:**
    - Added `TestService_PrepareUpload_Validation`, `TestService_ResultUpload_Validation`, and `TestHandler_UploadErrorStatusMapping` covering disallowed extensions, MIME spoofing, invalid/oversized `fileSize`/`size`, orphan-row prevention, exact 5 MiB boundary acceptance, backward-compatible payloads, and `PUT /api/v1/memes/upload-object` enforcement (11 MB payload with omitted `fileSize`, chunked `> 5 MiB`, tampered `Content-Type`, tampered size, forged token, empty body, and valid upload forwarding to S3).

### Frontend (`web/`)
- **`web/src/lib/queries.ts`:** Updated `usePrepareUpload` input type to `{ title, filename, contentType, fileSize }`.
- **`web/src/app/studio/page.tsx`:**
  - Added `MAX_UPLOAD_BYTES` (5 MiB), `EXT_TO_MIME`, and `validateSelectedFile(file)` invoked on both `onChange` and `onSubmit`.
  - Tightened file picker `accept` attribute to `.jpg,.jpeg,.png,.gif,.webp,image/jpeg,image/png,image/gif,image/webp`.
  - Updated `putToS3(uploadUrl, blob, contentType)` to send the exact signed `Content-Type` header.
  - Updated `onSubmit` to send `{ title, filename, contentType, fileSize }` to `PrepareUpload`, validate rendered PNG `blob.size <= 5 MiB`, and send `{ fileSize: blob.size }` to `ResultUpload`.

---

## Proof of Completion (Before vs. After)

### Before ([`evidence/before-api-repro.txt`](./evidence/before-api-repro.txt))
- `POST /api/v1/memes/upload` (`payload.html`, `text/html`) → `HTTP 201`, `PUT` to Garage S3 → **`HTTP 200`** (stored).
- `POST /api/v1/memes/upload` (`GTA.V.Full.Installer.zip`, `application/zip`) → **`HTTP 201`**.
- `POST /api/v1/memes/upload` (`malware.exe`, `application/octet-stream`) → `HTTP 201`, `PUT` to Garage S3 → **`HTTP 200`** (stored).
- `POST /api/v1/memes/upload` (`huge.png`, 6 MiB) → `HTTP 201` (`X-Amz-SignedHeaders=content-type;host`), `PUT` 6 MiB → **`HTTP 200`**.
- `POST /api/v1/memes/{id}/result-upload` → `HTTP 200` (`X-Amz-SignedHeaders=content-type;host`), `PUT` 6 MiB → **`HTTP 200`**.

### After ([`evidence/after-api-repro.txt`](./evidence/after-api-repro.txt))
- `POST /api/v1/memes/upload` (`payload.html`, `text/html`, 64 B) → **`HTTP 400`** (`code: 30002`, `"file gambar tidak valid"`).
- `POST /api/v1/memes/upload` (`GTA.V.Full.Installer.zip`, `application/zip`, 1024 B) → **`HTTP 400`** (`code: 30002`).
- `POST /api/v1/memes/upload` (`malware.exe`, `application/octet-stream`, 1024 B) → **`HTTP 400`** (`code: 30002`).
- `POST /api/v1/memes/upload` (`spoof.png`, `text/html`, 1024 B) → **`HTTP 400`** (`code: 30002`).
- `POST /api/v1/memes/upload` (`huge.png`, `image/png`, 6 MiB) → **`HTTP 413`** (`code: 30003`, `"ukuran file melebihi batas maksimum 5 MB"`).
- `POST /api/v1/memes/upload` (`ok.png`, `image/png`, 70 B) → **`HTTP 201`** (`X-Amz-SignedHeaders=content-length;content-type;host`):
  - Tampered `PUT` (200 B, `image/png`) to Garage S3 → **`HTTP 403 AccessDenied` (`Forbidden: Invalid signature`)**.
  - Tampered `PUT` (70 B, `text/html`) to Garage S3 → **`HTTP 403 AccessDenied` (`Forbidden: Invalid signature`)**.
  - Valid `PUT` (70 B, `image/png`) to Garage S3 → **`HTTP 200`**.
- `POST /api/v1/memes/{id}/result-upload` (6 MiB) → **`HTTP 413`** (`code: 30003`).
- `POST /api/v1/memes/{id}/result-upload` (70 B) → **`HTTP 200`** (`X-Amz-SignedHeaders=content-length;content-type;host`):
  - Tampered `PUT` (200 B, `image/png`) to Garage S3 → **`HTTP 403 AccessDenied`**.
  - Valid `PUT` (70 B, `image/png`) to Garage S3 → **`HTTP 200`**.
- Cleanup `DELETE /api/v1/memes/{id}` → **`HTTP 200`**.

---

## Regression Checks

- `cd api && go vet ./...` — **PASS**
- `cd api && go test -count=1 ./...` — **PASS** (all packages including `meme` and `v1` pass; note: `-race` requires CGO/gcc which is not installed on this Windows host).
- `cd api && go build -o bin/api.exe ./cmd/server` — **PASS**
- `cd web && bun run check` (`tsc --noEmit`) — **PASS**
- `cd web && bun run lint` (`next lint`) — **PASS** (0 errors; only pre-existing warning in `checkout/[reference]/page.tsx`)

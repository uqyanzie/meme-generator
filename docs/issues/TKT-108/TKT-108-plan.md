# TKT-108 Fix Plan: Arbitrary / Malicious File Uploads in S3 Storage

## 1. Root Cause Analysis

An audit of the backend upload pipeline (`api/internal/modules/meme/`) and frontend studio uploader (`web/src/lib/queries.ts`, `web/src/app/studio/page.tsx`) identified **four compounding defects** that allow arbitrary, oversized, and malicious files (`.html`, `.exe`, `.zip`, multi-GB blobs) to be stored in Garage S3:

1. **Missing extension validation (`api/internal/modules/meme/service.go`, L36–L45 & L167–L173):**
   - `allowedExt(ext)` is defined at L167–L173 (`.jpg`, `.jpeg`, `.png`, `.gif`, `.webp`), but `Service.PrepareUpload` **never calls it**. Any file extension (`.html`, `.exe`, `.zip`, or empty) is interpolated directly into `objectKey := fmt.Sprintf("base/%s%s", ...)`.
2. **Unvalidated client `contentType` (`api/internal/modules/meme/service.go`, L55–L61):**
   - `PrepareUpload` passes client-supplied `contentType` straight into `s3.PutObjectInput.ContentType` without checking an allowlist or verifying that it matches the file extension. An attacker can request `filename: "payload.html"` with `contentType: "text/html"` (stored XSS risk on public S3 endpoints) or omit `contentType` entirely.
3. **No file size validation or signed `Content-Length` (`api/internal/modules/meme/handler.go` L23–L67, `service.go` L36–L89):**
   - `prepareUploadDTO` does not accept `fileSize`, and `ResultUpload` accepts no body at all.
   - Neither `PrepareUpload` nor `ResultUpload` sets `PutObjectInput.ContentLength`. The resulting presigned URLs only sign `X-Amz-SignedHeaders=content-type;host`, allowing any payload size (e.g., 6 MiB or 90 GB) to be uploaded directly to Garage S3.
4. **Incomplete frontend validation & payload (`web/src/lib/queries.ts` L101–L108, `web/src/app/studio/page.tsx` L104–L111, L167–L186, L288–L303):**
   - `usePrepareUpload` only sends `{ title, filename }`, omitting `contentType` and `fileSize`.
   - `StudioPage` relies solely on HTML `accept="image/*"` (easily bypassed and allows non-allowlisted types like `image/svg+xml`) and performs no client-side size/type check before calling `PrepareUpload` or `ResultUpload`.

---

## 2. Before Reproduction Evidence

Live reproduction against the unmodified Go API (`http://localhost:8080`) and Garage S3 (`v2.4.1`, `http://localhost:3900`) is saved verbatim in [`evidence/before-api-repro.txt`](./evidence/before-api-repro.txt):

- **Malicious HTML (`.html` + `text/html`):** `POST /api/v1/memes/upload` → `HTTP 201` (object key `base/<hex>.html`), `PUT` HTML `<script>` payload to Garage S3 → **`HTTP 200`**.
- **Disallowed archive & executable (`.zip`, `.exe`):**
  - `POST /api/v1/memes/upload` for `GTA.V.Full.Installer.zip` (`application/zip`) → **`HTTP 201`**.
  - `POST /api/v1/memes/upload` for `malware.exe` (`application/octet-stream`) → **`HTTP 201`**, `PUT` MZ header bytes to Garage S3 → **`HTTP 200`**.
- **Oversized uploads (6 MiB > 5 MiB) on both endpoints:**
  - `PrepareUpload` presigned URL only signs `X-Amz-SignedHeaders=content-type;host`; `PUT` 6 MiB payload → **`HTTP 200`**.
  - `ResultUpload` presigned URL only signs `X-Amz-SignedHeaders=content-type;host`; `PUT` 6 MiB payload → **`HTTP 200`**.

### Garage v2.4.1 SigV4 Enforcement Verification
Probing the live Garage `v2.4.1` instance with a presigned `PutObjectInput` containing both `ContentLength` (`10`) and `ContentType` (`image/png`) confirmed `X-Amz-SignedHeaders=content-length;content-type;host` is strictly enforced at upload time:
- Exact match (`10 B`, `image/png`) → `HTTP 200`.
- Oversized/mismatched body (`20 B`, `image/png`) → **`HTTP 403 AccessDenied` (`Forbidden: Invalid signature`)**.
- Mismatched `Content-Type` (`10 B`, `text/html`) → **`HTTP 403 AccessDenied` (`Forbidden: Invalid signature`)**.

---

## 3. Agreed Decisions

Recorded in [`TKT-108.md`](./TKT-108.md):

1. **Upload enforcement architecture:** **Signed Presigned PUT (`ContentLength` + `ContentType`)** — keep the direct-to-S3 presigned PUT architecture; validate extension, MIME allowlist + extension-to-MIME match, and `0 < fileSize <= maxUploadBytes` on the backend, and sign both `ContentLength` and `ContentType` into the S3 presigned PUT URLs for both `PrepareUpload` and `ResultUpload`.
2. **Maximum file size limit:** **5 MiB (`5 << 20` = 5,242,880 bytes)** for both base image uploads and rendered PNG result uploads.

---

## 4. Minimal Fix Design

Scope is strictly limited to `api/internal/modules/meme/` and the Studio upload client in `web/src/lib/queries.ts` & `web/src/app/studio/page.tsx`. No DB migrations are required.

### Backend (`api/internal/modules/meme/`)

1. **`api/internal/modules/meme/consts.go`**
   - Add `const maxUploadBytes int64 = 5 << 20` (5 MiB).
2. **`api/internal/modules/meme/errors.go`**
   - Add `ErrFileTooLarge = errors.New("ukuran file melebihi batas maksimum 5 MB")`.
3. **`api/internal/modules/meme/service.go`**
   - Add canonical extension-to-MIME mapping (`extToMIME`) covering `.jpg` → `image/jpeg`, `.jpeg` → `image/jpeg`, `.png` → `image/png`, `.gif` → `image/gif`, `.webp` → `image/webp`.
   - Add `validateImageUpload(ext, contentType string, fileSize int64) (normExt, normMIME string, err error)`:
     - Normalize `ext := strings.ToLower(strings.TrimSpace(ext))` and require `allowedExt(ext)` (else `ErrInvalidFile`).
     - Parse/normalize `contentType` via `mime.ParseMediaType` (or lowercase trim) and require it matches `extToMIME[ext]` exactly (else `ErrInvalidFile`). Rejects `text/html`, `image/svg+xml`, `application/zip`, empty MIME, and extension/MIME spoofing (e.g., `.png` + `text/html`).
     - Validate `fileSize`: `<= 0` → `ErrInvalidFile`; `> maxUploadBytes` → `ErrFileTooLarge`.
   - Add `validateFileSize(fileSize int64) error`: `<= 0` → `ErrInvalidFile`; `> maxUploadBytes` → `ErrFileTooLarge`.
   - Update `PrepareUpload(ctx context.Context, userID, title, ext, contentType string, fileSize int64) (UploadResponse, error)`:
     - Check `s.gate.CanCreate(ctx, userID)`.
     - Call `validateImageUpload(ext, contentType, fileSize)` **before** `s.repo.Create(...)` so rejected uploads never create orphan DB rows or consume quota.
     - Set both `ContentType: &normMIME` and `ContentLength: &fileSize` on `&s3.PutObjectInput{...}` before `PresignPutObject`.
   - Update `ResultUpload(ctx context.Context, userID, id string, fileSize int64) (UploadResponse, error)`:
     - Check `s.gate.CanCreate(ctx, userID)`.
     - Call `validateFileSize(fileSize)` before DB/S3 work.
     - Verify ownership via `s.repo.GetByID(ctx, id, userID)`.
     - Set both `ContentType: aws.String("image/png")` and `ContentLength: &fileSize` on `&s3.PutObjectInput{...}` before `PresignPutObject`.
4. **`api/internal/modules/meme/handler.go`**
   - Extend `prepareUploadDTO` with `FileSize int64` (`json:"fileSize"`).
   - Add `resultUploadDTO` with `FileSize int64` (`json:"fileSize"`).
   - In `PrepareUpload`: decode `prepareUploadDTO` and pass `dto.FileSize` to `h.service.PrepareUpload(...)`.
   - In `ResultUpload`: decode `resultUploadDTO` (returning `400` / `CodeInvalidRequestBody` on invalid JSON) and pass `dto.FileSize` to `h.service.ResultUpload(...)`.
   - In `writeError`: map `errors.Is(err, ErrFileTooLarge)` → `413 Request Entity Too Large` with existing `response.CodeUploadTooLarge` (`30003`). Keep `ErrInvalidFile` → `400 Bad Request` (`response.CodeInvalidImage = 30002`).

### Frontend (`web/`)

5. **`web/src/lib/queries.ts`**
   - Update `usePrepareUpload` input type to `{ title: string; filename: string; contentType: string; fileSize: number }`.
6. **`web/src/app/studio/page.tsx`**
   - Define `MAX_UPLOAD_BYTES = 5 * 1024 * 1024` and an extension-to-MIME allowlist matching the backend (`.jpg`, `.jpeg`, `.png`, `.gif`, `.webp` ↔ `image/jpeg`, `image/png`, `image/gif`, `image/webp`).
   - Tighten `<Input id="image" type="file" accept=".jpg,.jpeg,.png,.gif,.webp,image/jpeg,image/png,image/gif,image/webp" />`.
   - Add a client-side `validateSelectedFile(file: File)` helper called in both `onChange` and `onSubmit` to give immediate feedback for disallowed types or files > 5 MiB (and resolve canonical `contentType` if a browser leaves `file.type` empty).
   - Update `putToS3(uploadUrl: string, blob: Blob, contentType: string)` so the `Content-Type` header always matches the signed MIME type.
   - In `onSubmit`:
     - Send `{ title, filename: file.name, contentType, fileSize: file.size }` to `prepare.mutateAsync(...)` and call `putToS3(prepared.uploadUrl, file, contentType)`.
     - Validate rendered PNG `blob.size <= MAX_UPLOAD_BYTES`, send `{ fileSize: blob.size }` to `POST /api/v1/memes/${prepared.id}/result-upload`, and call `putToS3(result.uploadUrl, blob, "image/png")`.

---

## 5. Edge Cases & Regression Risks

- **Header mismatch between browser `fetch` and SigV4:** Browser `fetch` automatically sets `Content-Length` from `blob.size`, while `Content-Type` comes from our explicit header. Ensuring `putToS3` sends the exact `contentType` submitted to `PrepareUpload` (and `"image/png"` for `ResultUpload`) avoids accidental `403 SignatureDoesNotMatch` on valid uploads.
- **MIME parameters / case sensitivity:** Clients sending `IMAGE/PNG` or `image/png; charset=binary` or uppercase `.JPG` should normalize cleanly to `.jpg` / `image/jpeg` without false rejections.
- **Zero / negative `fileSize`:** Explicitly rejected with `400` (`ErrInvalidFile`) rather than `413`, preventing callers from omitting `fileSize` (JSON default `0`) and bypassing size enforcement.
- **Exact boundary (`5 * 1024 * 1024` bytes):** `fileSize == maxUploadBytes` must be **allowed**; `fileSize == maxUploadBytes + 1` must be **rejected** with `413` (`CodeUploadTooLarge`).
- **Orphan DB records on rejected uploads:** Validating extension, MIME, and size **before** `repo.Create` guarantees rejected requests neither pollute the `memes` table nor consume subscription quota.
- **SVG exclusion:** `.svg` / `image/svg+xml` is intentionally excluded from `allowedExt` because SVG can contain embedded `<script>` (stored XSS).
- **Response envelope preserved:** Uses existing `response.CodeInvalidImage` (`30002`) and `response.CodeUploadTooLarge` (`30003`).

---

## 6. Verification Plan

### A. Automated Regression Tests (Red → Green in `edd-implement`)
Add unit & handler tests in `api/internal/modules/meme/service_test.go` (and `handler_test.go` if helpful):
1. **`TestService_PrepareUpload_Validation`:**
   - Rejects disallowed extensions (`.html`, `.exe`, `.zip`, `.svg`, `""`) with `ErrInvalidFile`.
   - Rejects disallowed or mismatched MIME types (`text/html` with `.png`, `image/png` with `.jpg`, empty `contentType`) with `ErrInvalidFile`.
   - Rejects `fileSize <= 0` with `ErrInvalidFile` and `fileSize = 5 MiB + 1` with `ErrFileTooLarge`.
   - Accepts valid combinations (`.jpg`/`image/jpeg`, `.JPEG`/`IMAGE/JPEG`, `.png`/`image/png`, `.gif`/`image/gif`, `.webp`/`image/webp`) at boundary `fileSize = 5 MiB`, and verifies the returned presigned URL includes `content-length` and `content-type` in `X-Amz-SignedHeaders`.
2. **`TestService_ResultUpload_Validation`:**
   - Rejects `fileSize <= 0` with `ErrInvalidFile` and `fileSize = 5 MiB + 1` with `ErrFileTooLarge`.
   - Accepts `fileSize <= 5 MiB` for owned meme and verifies presigned URL signs both `content-length` and `content-type` (`image/png`).
3. **`TestHandler_UploadErrorStatusMapping`:**
   - Verifies HTTP `400` (`code: 30002`) for `ErrInvalidFile` and HTTP `413` (`code: 30003`) for `ErrFileTooLarge` on both `/api/v1/memes/upload` and `/api/v1/memes/{id}/result-upload`.

### B. Static Analysis, Test & Build Gates
```powershell
cd api; go vet ./...; go test -race ./...; go build -o bin/api ./cmd/server
cd web; bun run check; bun run lint
```

### C. Live "After" Reproduction (Mirroring Step 2)
Run against the rebuilt Go API + Garage S3 and save verbatim output to `docs/issues/TKT-108/evidence/after-api-repro.txt`:
1. `POST /api/v1/memes/upload` with `payload.html` (`text/html`, 64 B) → **`HTTP 400`** (`code: 30002`).
2. `POST /api/v1/memes/upload` with `GTA.V.Full.Installer.zip` (`application/zip`, 1024 B) → **`HTTP 400`** (`code: 30002`).
3. `POST /api/v1/memes/upload` with `malware.exe` (`application/octet-stream`, 1024 B) → **`HTTP 400`** (`code: 30002`).
4. `POST /api/v1/memes/upload` with `spoof.png` (`text/html`, 1024 B) → **`HTTP 400`** (`code: 30002`).
5. `POST /api/v1/memes/upload` with `huge.png` (`image/png`, 6 MiB) → **`HTTP 413`** (`code: 30003`).
6. `POST /api/v1/memes/upload` with valid `ok.png` (`image/png`, 68 B) → **`HTTP 201`** (presigned URL signs `content-length;content-type;host`).
   - Tampered `PUT` with larger body (200 B) to Garage S3 → **`HTTP 403 AccessDenied`**.
   - Tampered `PUT` with `Content-Type: text/html` to Garage S3 → **`HTTP 403 AccessDenied`**.
   - Valid `PUT` (68 B, `image/png`) to Garage S3 → **`HTTP 200`**.
7. `POST /api/v1/memes/{id}/result-upload` with `fileSize: 6 MiB` → **`HTTP 413`** (`code: 30003`).
8. `POST /api/v1/memes/{id}/result-upload` with `fileSize: 68` → **`HTTP 200`**, valid `PUT` (68 B, `image/png`) → **`HTTP 200`**, tampered `PUT` (200 B) → **`HTTP 403 AccessDenied`**.
9. Clean up test meme via `DELETE /api/v1/memes/{id}` → `HTTP 200`.

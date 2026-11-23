# TKT-107 Fix Plan: Unauthorized Viewing and Deletion of Other Users' Memes (IDOR)

## 1. Root Cause Analysis

An audit of all routes in `api/internal/modules/meme/mount.go` (L18–L24), handlers in `api/internal/modules/meme/handler.go`, service methods in `api/internal/modules/meme/service.go`, and SQL queries in `api/internal/modules/meme/repository.go` identified **three IDOR defects** and **one latent SQL scan bug** in the repository layer:

1. **`Repository.GetByID(ctx, id, userID)` — `api/internal/modules/meme/repository.go` (L36–L46):**
   - Accepts `userID` as a parameter but **ignores it completely**, querying `WHERE id = $1` without `AND user_id = $2`.
   - **Latent scan bug:** The `SELECT` list fetches **10 columns** (including `updated_at`), while `row.Scan(...)` only provides **9 destination pointers** (matching `Meme` in `model.go`). Because `pgx` rejects the mismatch with `number of field descriptions must equal number of destinations, got 10 and 9`, `POST /api/v1/memes/{id}/result-upload` currently returns `500 Internal Server Error` for **both** the legitimate owner and an attacker.
2. **`Repository.SetResultURL(ctx, id, userID, resultURL)` — `api/internal/modules/meme/repository.go` (L48–L64):**
   - Accepts `userID` as a parameter but **ignores it completely**, executing `UPDATE memes SET result_url = $2 WHERE id = $1 RETURNING ...` without `AND user_id = $3`.
   - **Impact:** Any subscribed user can call `POST /api/v1/memes/{victim_id}/complete` to overwrite another user's `result_url` **and** read back the victim's full `Meme` record (`userId`, `title`, `baseUrl`, `resultUrl`, etc.) in the `200 OK` response.
3. **`Repository.Delete(ctx, id, userID)` — `api/internal/modules/meme/repository.go` (L93–L102):**
   - Accepts `userID` as a parameter but **ignores it completely**, executing `DELETE FROM memes WHERE id = $1` without `AND user_id = $2`.
   - **Impact:** Any authenticated user can call `DELETE /api/v1/memes/{victim_id}` and permanently delete another user's meme.
4. **Route Audit (`api/internal/modules/meme/mount.go` L18–L24):**
   - `POST /api/v1/memes/upload` (`PrepareUpload`): creates a new row bound to `userID` from auth context — safe.
   - `GET /api/v1/memes` (`List` → `ListByUser`): already filters `WHERE user_id = $1` on both `COUNT(*)` and `SELECT` — safe.
   - `POST /api/v1/memes/{id}/result-upload`, `POST /api/v1/memes/{id}/complete`, `DELETE /api/v1/memes/{id}`: all vulnerable via the three repository methods above.
   - No `GET /api/v1/memes/{id}` route exists.

---

## 2. Before Reproduction Evidence

Live reproduction against the unmodified Go API (`http://localhost:8080`) with two registered & subscribed users (**Victim A** and **Attacker B**) is saved verbatim in [`evidence/before-api-repro.txt`](./evidence/before-api-repro.txt):

- **Bug 1 (`ResultUpload` / `GetByID`):**
  - `POST /api/v1/memes/{victim_meme_id}/result-upload` as **Victim A (owner)** → `HTTP 500` (`err="number of field descriptions must equal number of destinations, got 10 and 9"`).
  - `POST /api/v1/memes/{victim_meme_id}/result-upload` as **Attacker B** → `HTTP 500` (same scan error after matching Victim A's row without `user_id` filtering).
- **Bug 2 (`Complete` / `SetResultURL` IDOR):**
  - `POST /api/v1/memes/{victim_meme_id}/complete` as **Attacker B** with `{"resultUrl":"http://evil.example/hijacked.png"}` → **`HTTP 200`**, overwriting `resultUrl` and returning Victim A's full `Meme` JSON (`userId` = Victim A, `title` = `"Victim Secret Meme"`, `baseUrl` = Victim A's Garage URL).
- **Bug 3 (`Delete` IDOR):**
  - `DELETE /api/v1/memes/{victim_meme_id}` as **Attacker B** → **`HTTP 200`** (`meme dihapus`).
  - Subsequent `GET /api/v1/memes` as **Victim A** → `{"items":[],"total":0}` (Victim A's meme was permanently deleted).

---

## 3. Agreed Decisions

Recorded in [`TKT-107.md`](./TKT-107.md):

1. **HTTP status for unauthorized resource access:** Return `404 Not Found` (`ErrNotFound` / `response.CodeMemeNotFound = 30001`) via `WHERE id = $1 AND user_id = $2`. Single atomic query, reuses existing error mapping in `handler.go`, and prevents resource ID enumeration.
2. **S3 object cleanup on delete:** Enforce atomic DB ownership (`DELETE FROM memes WHERE id = $1 AND user_id = $2 RETURNING ...`) **plus** best-effort S3 cleanup (`s3.DeleteObject` for `base_url` and `result_url` keys in Garage S3 after DB deletion, logging errors without failing the HTTP response).
3. **Single-meme `GET /api/v1/memes/{id}` scope:** Keep existing routes only; do not add a new endpoint.

---

## 4. Minimal Fix Design

Scope is strictly limited to `api/internal/modules/meme/`. No frontend changes and no DB schema migrations are required.

### `api/internal/modules/meme/repository.go`
- Add a small helper `isInvalidUUID(err error) bool` checking `pgconn.PgError` SQLSTATE `22P02` so malformed `{id}` path values return `ErrNotFound` (`404`) instead of `500`.
- **`GetByID(ctx, id, userID)`:**
  - Fix column list to the 9 scanned columns (remove `updated_at`).
  - Add `AND user_id = $2` and pass `id, userID`.
  - Map `pgx.ErrNoRows` (and `22P02`) → `ErrNotFound`; wrap other errors consistently.
- **`SetResultURL(ctx, id, userID, resultURL)`:**
  - Change query to:
    ```sql
    UPDATE memes SET result_url = $2, updated_at = now()
    WHERE id = $1 AND user_id = $3
    RETURNING id, user_id, title, base_url, result_url, top_text, bottom_text, font_size, created_at
    ```
    passing `id, resultURL, userID`.
  - Map `pgx.ErrNoRows` (and `22P02`) → `ErrNotFound`.
- **`Delete(ctx, id, userID)`:**
  - Change signature to `Delete(ctx context.Context, id, userID string) (Meme, error)`.
  - Execute atomic ownership-checked delete returning the deleted row's URLs:
    ```sql
    DELETE FROM memes
    WHERE id = $1 AND user_id = $2
    RETURNING id, user_id, title, base_url, result_url, top_text, bottom_text, font_size, created_at
    ```
    passing `id, userID`.
  - Map `pgx.ErrNoRows` (and `22P02`) → `ErrNotFound`.

### `api/internal/modules/meme/service.go`
- **`Delete(ctx, userID, id)`:**
  - Call `deleted, err := s.repo.Delete(ctx, id, userID)`. Return `err` immediately on failure (guarantees unauthorized callers never trigger S3 operations).
  - If `s.s3 != nil`, perform best-effort `s3.DeleteObject` for each valid object key extracted from `deleted.BaseImageURL` and (if non-nil) `*deleted.ResultURL`.
  - Log any S3 delete failure via `slog.Warn("meme service: s3 delete gagal", "meme_id", id, "key", key, "err", err)` without failing the `Delete` return value.
- **`objectKeyFromURL(rawURL string) (string, bool)`:**
  - When `s.baseURL != ""`, require `strings.HasPrefix(rawURL, strings.TrimRight(s.baseURL, "/") + "/")`, strip the prefix, clean the path, and reject empty or `..`-prefixed keys.
  - When `s.baseURL == ""`, allow cleaned relative keys under `base/` or `result/`.
  - Any external URL (e.g., mismatched host) returns `("", false)` and is safely skipped.

### `api/internal/modules/meme/handler.go` & `mount.go`
- No changes required: `writeError` already maps `ErrNotFound` → `404 Not Found` (`response.CodeMemeNotFound = 30001`), and all meme routes remain behind `authMW`.

---

## 5. Edge Cases & Regression Risks

- **Owner `ResultUpload` flow restored:** Fixing the 10-column vs 9-arg scan bug unblocks `POST /api/v1/memes/{id}/result-upload` for legitimate owners (prerequisite for TKT-108 & TKT-109).
- **TOCTOU avoided on `Delete`:** Using `DELETE ... WHERE id = $1 AND user_id = $2 RETURNING ...` ensures ownership verification and deletion happen atomically in one statement.
- **Partial/incomplete memes on `Delete`:** If a meme is deleted before `Complete` (`result_url IS NULL`), only `base_url` is cleaned up in S3. If the base object was never uploaded to Garage S3 (abandoned `PrepareUpload`), best-effort cleanup logs a warning (if Garage errors) and still returns `200 OK`.
- **Non-UUID path parameter:** Malformed IDs (e.g., `/api/v1/memes/not-a-uuid`) return `404 Not Found` (`ErrNotFound`) instead of leaking a `500` Postgres `22P02` error.
- **Response envelope preserved:** All status codes and `response.Body` fields (`CodeMemeNotFound`, `CodeUploadPrepared`, `CodeMemeUpdated`, `CodeMemeDeleted`) remain unchanged.

---

## 6. Verification Plan

### A. Automated Regression Tests (Red → Green in `edd-implement`)
Add `api/internal/modules/meme/repository_test.go` (and `service_test.go` for S3 cleanup & URL-to-key parsing):
1. **`TestRepository_GetByID_OwnershipAndScan`:**
   - Owner (`userA`) fetches own meme → succeeds with all 9 fields intact (fails Red before fix due to 10-vs-9 scan bug).
   - Non-owner (`userB`) fetches `userA`'s meme → returns `ErrNotFound` (fails Red before fix).
   - Malformed UUID → returns `ErrNotFound`.
2. **`TestRepository_SetResultURL_Ownership`:**
   - Non-owner (`userB`) attempts to update `userA`'s meme → returns `ErrNotFound`, and DB row remains unmodified (`result_url` still `NULL`).
   - Owner (`userA`) updates own meme → succeeds and persists `result_url`.
3. **`TestRepository_Delete_Ownership`:**
   - Non-owner (`userB`) attempts to delete `userA`'s meme → returns `ErrNotFound`, and `userA`'s meme still exists.
   - Owner (`userA`) deletes own meme → succeeds, returns deleted `Meme` metadata, and row is removed.
4. **`TestService_Delete_BestEffortS3Cleanup`:**
   - Using an `httptest.NewServer` stub S3 endpoint (and unit tests for `objectKeyFromURL`):
     - Verifies `DELETE` sends S3 `DELETE` requests for both `base/...` and `result/...` keys when owned.
     - Verifies external/non-matching `resultUrl` is skipped safely.
     - Verifies S3 failure (e.g., HTTP 500 from S3) does **not** fail `Service.Delete` after DB deletion succeeds.
     - Verifies unauthorized delete (`ErrNotFound`) sends **zero** S3 requests.

### B. Static Analysis, Test & Build Gates
```powershell
cd api; go vet ./...; go test -race ./...; go build -o bin/api ./cmd/server
cd web; bun run check; bun run lint
```

### C. Live "After" Reproduction (Mirroring Step 2)
Run the same two-user live API flow against the rebuilt Go API and save verbatim output to `docs/issues/TKT-107/evidence/after-api-repro.txt`:
1. Victim A creates a meme (`POST /api/v1/memes/upload`) and uploads a dummy base PNG to the presigned Garage URL.
2. **Attacker B** calls `POST /api/v1/memes/{memeId}/result-upload` → **`HTTP 404`** (`code: 30001`).
3. **Victim A (owner)** calls `POST /api/v1/memes/{memeId}/result-upload` → **`HTTP 200`** (returns presigned result URL; unblocked!), uploads dummy result PNG.
4. **Attacker B** calls `POST /api/v1/memes/{memeId}/complete` → **`HTTP 404`** (`code: 30001`).
5. **Victim A (owner)** calls `POST /api/v1/memes/{memeId}/complete` → **`HTTP 200`**.
6. **Attacker B** calls `DELETE /api/v1/memes/{memeId}` → **`HTTP 404`** (`code: 30001`), and Victim A's `GET /api/v1/memes` still shows `total: 1`.
7. **Victim A (owner)** calls `DELETE /api/v1/memes/{memeId}` → **`HTTP 200`** (`meme dihapus`), Victim A's `GET /api/v1/memes` shows `total: 0`, and direct `GET`/`HEAD` on the deleted Garage S3 base & result URLs returns `404` (confirming best-effort S3 cleanup).

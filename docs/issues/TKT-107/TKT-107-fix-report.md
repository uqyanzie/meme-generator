# TKT-107 Fix Report: Unauthorized Viewing and Deletion of Other Users' Memes (IDOR)

## Summary (Non-Engineer Friendly)

Previously, any logged-in user who guessed or knew another user's meme ID could overwrite that meme's final image or permanently delete it, and the endpoint for uploading a rendered meme image was broken for everyone due to a database query mismatch.

We fixed all three database operations so they strictly verify that the meme belongs to the requesting user, repaired the broken query, and added automatic cleanup of the stored image files when a meme is deleted. Unauthorized requests now receive a `404 Not Found` response without revealing whether the meme exists.

---

## Root Cause (Technical)

1. **`Repository.GetByID(ctx, id, userID)` (`api/internal/modules/meme/repository.go`):**
   - Ignored `userID` (`WHERE id = $1` without `AND user_id = $2`).
   - Selected 10 columns (including `updated_at`) into 9 `Scan` destinations, causing `POST /api/v1/memes/{id}/result-upload` to fail with `500 Internal Server Error` for both owners and attackers.
2. **`Repository.SetResultURL(ctx, id, userID, resultURL)` (`api/internal/modules/meme/repository.go`):**
   - Ignored `userID` (`WHERE id = $1` without `AND user_id = $3`), allowing any subscribed user to overwrite another user's `result_url` and read back the victim's full `Meme` record.
3. **`Repository.Delete(ctx, id, userID)` (`api/internal/modules/meme/repository.go`):**
   - Ignored `userID` (`WHERE id = $1` without `AND user_id = $2`), allowing any authenticated user to delete another user's meme, and did not clean up S3 objects.

---

## Changes Made

- **`api/internal/modules/meme/repository.go`:**
  - Added `isInvalidUUID(err error) bool` (SQLSTATE `22P02`) so malformed `{id}` values map to `ErrNotFound` (`404`) instead of `500`.
  - Fixed `GetByID` to select the 9 `Meme` columns and filter `WHERE id = $1 AND user_id = $2`.
  - Fixed `SetResultURL` to filter `WHERE id = $1 AND user_id = $3` and update `updated_at = now()`.
  - Updated `Delete` to `DELETE FROM memes WHERE id = $1 AND user_id = $2 RETURNING ...` and return `(Meme, error)`.
- **`api/internal/modules/meme/service.go`:**
  - Updated `Service.Delete` to perform best-effort `s3.DeleteObject` cleanup for `BaseImageURL` and `ResultURL` after atomic DB deletion, logging warnings on S3 errors without failing the HTTP response.
  - Added `objectKeyFromURL` to safely extract and validate Garage S3 object keys.
- **`api/internal/modules/meme/repository_test.go` & `api/internal/modules/meme/service_test.go`:**
  - Added regression tests covering ownership enforcement, 9-column scan, malformed UUID handling, and best-effort S3 cleanup.

---

## Proof of Completion (Before vs. After)

- **Before (`docs/issues/TKT-107/evidence/before-api-repro.txt`):**
  - `POST /api/v1/memes/{victim_id}/result-upload` (Victim A & Attacker B) → `HTTP 500` (10-vs-9 scan mismatch).
  - `POST /api/v1/memes/{victim_id}/complete` (Attacker B) → **`HTTP 200`**, overwriting `resultUrl` and returning Victim A's meme.
  - `DELETE /api/v1/memes/{victim_id}` (Attacker B) → **`HTTP 200`**, permanently deleting Victim A's meme (`total: 0`).
- **After (`docs/issues/TKT-107/evidence/after-api-repro.txt`):**
  - Attacker B `POST /memes/{id}/result-upload` → **`HTTP 404`**
  - Victim A `POST /memes/{id}/result-upload` → **`HTTP 200`** (unblocked)
  - Attacker B `POST /memes/{id}/complete` → **`HTTP 404`**
  - Victim A `POST /memes/{id}/complete` → **`HTTP 200`**
  - Attacker B `DELETE /memes/{id}` → **`HTTP 404`**, Victim A `GET /memes` → `total=1`
  - Victim A `DELETE /memes/{id}` → **`HTTP 200`**, Victim A `GET /memes` → `total=0`, S3 `HEAD` on both `baseUrl` and `resultUrl` → **`HTTP 404`**

---

## Regression Checks

- `cd api; go vet ./...`: Passed
- `cd api; go test ./...`: Passed (`internal/modules/meme` 0.566s; `-race` skipped on this host because `gcc` is not in `%PATH%`)
- `cd api; go build -o bin/api ./cmd/server`: Passed
- `cd web; bun run check`: Passed (`tsc --noEmit`)
- `cd web; bun run lint`: Passed (`next lint`)

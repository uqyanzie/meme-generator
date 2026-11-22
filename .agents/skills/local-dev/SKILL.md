---
name: local-dev
description: >-
  Windows PowerShell runbook for starting, migrating, verifying, and resetting
  the local Meme Portal stack (Postgres, Redis, Garage S3, Go API, Next.js Web).
  Use whenever you need to run the app locally, execute DB migrations, inspect
  Postgres/Redis/S3 state, or reproduce live issues on Windows.
---

# Local Development Runbook (Windows PowerShell)

The root `Makefile` assumes `/bin/sh`, and `process-compose`/`air`/`sqlc` may not be installed on Windows. Use these direct PowerShell commands instead.

## 1. Environment File

Ensure `.env` exists at repo root (copied from `.env.example`):

```powershell
if (!(Test-Path .env)) { Copy-Item .env.example .env }
```

Helper snippet to load `.env` into the current PowerShell session (required before running the Go API, because `api/internal/config/config.go` reads process env vars directly and defaults `S3_ACCESS_KEY`/`S3_SECRET_KEY`/`S3_PUBLIC_BASE_URL` to empty strings):

```powershell
Get-Content .env | Where-Object { $_ -and $_ -notmatch '^\s*#' } | ForEach-Object {
  $k, $v = $_ -split '=', 2
  [Environment]::SetEnvironmentVariable($k.Trim(), $v.Trim(), 'Process')
}
$env:REDIS_ADDR = "$($env:REDIS_HOST):$($env:REDIS_PORT)"
```

## 2. Start Infrastructure (Docker Compose)

```powershell
docker compose up -d --wait postgres redis garage
docker compose ps
```

Service endpoints:
- **Postgres:** `localhost:5432` (user `meme`, pass `meme`, db `meme_portal`)
- **Redis:** `localhost:6379`
- **Garage S3 API:** `http://localhost:3900` (bucket `meme-portal`)
- **Garage S3 Web:** `http://meme-portal.web.garage.localhost:3902`

## 3. Run Database Migrations

```powershell
$dbUrl = "postgres://meme:meme@localhost:5432/meme_portal?sslmode=disable"
migrate -path api/migrations -database $dbUrl up
migrate -path api/migrations -database $dbUrl version
```

> Note: `000004_idempotency_key.up.sql` includes an explicit `INSERT INTO schema_migrations ... ON CONFLICT DO NOTHING`, and `000005_plan_quotas.up.sql` sets real per-plan meme quotas (`premium_1d=10`, `premium_3d=25`, `premium_30d=100`, `premium_60d=250`, `premium_90d=0/unlimited`).

## 4. Run Backend API (`:8080`) & Frontend Web (`:3000`)

**Backend (Go API):**
```powershell
# Run in a background daemon or separate terminal after loading .env
Push-Location api
go run ./cmd/server
Pop-Location
```

Verify health:
```powershell
Invoke-RestMethod http://localhost:8080/api/v1/health
```

**Frontend (Next.js via Bun):**
```powershell
Push-Location web
if (!(Test-Path node_modules)) { bun install }
bun run dev
Pop-Location
```

## 5. Useful Inspection & Reset Commands

**Query Postgres via Docker:**
```powershell
docker compose exec -T postgres psql -U meme -d meme_portal -c "SELECT code, name, duration_days, max_memes FROM plans;"
```

**Simulate an expired subscription (for TKT-101 repro):**
```powershell
docker compose exec -T postgres psql -U meme -d meme_portal -c "UPDATE subscriptions SET ends_at = now() - interval '1 hour' WHERE status = 'active';"
```

**Inspect Redis keys (auth refresh tokens):**
```powershell
docker compose exec -T redis redis-cli KEYS "auth:*"
```

**Full clean reset (wipes volumes):**
```powershell
docker compose down -v
docker compose up -d --wait postgres redis garage
migrate -path api/migrations -database "postgres://meme:meme@localhost:5432/meme_portal?sslmode=disable" up
```

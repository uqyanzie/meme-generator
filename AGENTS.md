# AGENTS.md — Meme Portal

Project-level instructions and guardrails for AI coding agents working on this repository.

## Project Overview

Subscription-based meme generator being hardened by resolving 9 user-reported production issues (`TKT-101` through `TKT-109`) using **Error-Driven Development (EDD)**.

- **Backend (`api/`):** Go, stdlib `net/http`, `pgx/v5`, Redis (`go-redis`), S3 (`aws-sdk-go-v2` → Garage S3), JWT (`golang-jwt`), `golang-migrate`.
- **Frontend (`web/`):** Next.js 15 (App Router), React 19, TanStack Query, TanStack Form, shadcn/ui, axios, `bun`.
- **Infrastructure:** Docker Compose (`postgres:17`, `redis:7`, `dxflrs/garage:v2.4.1`).

## Workflow & Engineering Rules

1. **Follow 2-Phase EDD (Error-Driven Development):**
   - **Phase 1 — Investigate, Reproduce & Plan (`edd-plan`):** Activate `.agents/skills/edd-plan/SKILL.md` to read `docs/issues/TKT-XXX/TKT-XXX.md`, align on any **Pending Decisions**, capture verbatim **Before** reproduction evidence under `docs/issues/TKT-XXX/evidence/`, and write `TKT-XXX-plan.md` — **without editing application code**. Always hold for plan review before moving to Phase 2.
   - **Phase 2 — Implement, Verify, Report & Commit (`edd-implement`):** Activate `.agents/skills/edd-implement/SKILL.md` to execute an approved `TKT-XXX-plan.md` using Red→Green regression tests, run verification gates & live **After** checks, write `TKT-XXX-fix-report.md` (synced to `FIX_REPORT.md` and `docs/Issue.md`), **pause for a quick diff review**, and then create the atomic `fix(TKT-XXX): ...` commit.
2. **Respect Ticket Scope & Commit Hygiene:**
   - **One atomic commit per issue** for bugfixes (`fix(TKT-XXX): ...`), with a body summarizing root cause and solution.
   - Do not fold fixes for unrelated tickets into the current ticket's diff.
   - Workflow/docs setup commits are kept separate (`chore:` / `docs:`).
3. **Preserve Contracts & Keep Fixes Minimal:**
   - Preserve the standard backend response envelope (`api/internal/response/response.go`).
   - Prefer handwritten repository methods consistent with existing modules unless a schema change requires a new sequential SQL migration (`api/migrations/`).
   - Add automated Go tests (`go test ./...`, including `-race` for concurrency issues) whenever a backend bug can be codified as a regression test.
4. **Verification Gate Before Commit:**
   - Backend: `cd api && go vet ./... && go test -race ./... && go build -o bin/api ./cmd/server`
   - Frontend: `cd web && bun run check && bun run lint`
5. **Windows Local Environment:**
   - Use the `local-dev` skill (`.agents/skills/local-dev/SKILL.md`) for running Docker infra, migrations, API, and Web on Windows PowerShell.
   - Remember that the Go API reads env vars from the shell environment (not `.env` directly); load `.env` before running `go run ./cmd/server`.
6. **Documentation Outputs:**
   - Each resolved ticket gets `docs/issues/TKT-XXX/TKT-XXX-fix-report.md` (clear for both engineers and non-engineers).
   - Keep `docs/Issue.md` updated with ticket status and commit SHA.
   - Sync resolved ticket reports into the required root `FIX_REPORT.md`.

---
name: edd-resolve
description: >-
  Guides the agent through the Error-Driven Development (EDD) workflow for
  investigating, reproducing, planning, fixing, verifying, documenting, and
  committing any TKT-XXX issue in this repository. Use whenever the user asks
  to work on, plan, reproduce, or resolve an issue (TKT-101 through TKT-109).
---

# EDD Issue Resolution Workflow (`TKT-XXX`)

Follow these steps strictly in order for every ticket.

## Step 1: Load Context & Check Pending Decisions

1. Read `docs/issues/<TKT>/<TKT>.md` and `docs/Issue.md`.
2. Inspect the **Pending Decisions** section in `<TKT>.md`.
   - If any decision is still marked **Pending**, pause and discuss it with the user before writing production code. Record the agreed decision in `<TKT>.md` or `<TKT>-plan.md`.

## Step 2: Reproduce First (Capture "Before" Evidence)

1. Ensure local stack is running if live repro is needed (see `.agents/skills/local-dev/SKILL.md`).
2. Reproduce the exact bug **before changing any application code**:
   - **Backend issues:** Prefer writing a failing Go test (unit/integration/concurrency) or running an exact `curl`/PowerShell repro command that demonstrates the defect.
   - **Frontend issues:** Reproduce via browser flow / network inspection and capture exact observable behavior (or screenshots/logs under `docs/issues/<TKT>/evidence/`).
3. Save the raw **Before** output/observations so they can be quoted verbatim in `<TKT>-fix-report.md`.

## Step 3: Write the Fix Plan

Create `docs/issues/<TKT>/<TKT>-plan.md` containing:
- **Root Cause Analysis:** Exact file paths, line ranges, and mechanism (not just symptoms).
- **Agreed Decisions:** Summary of resolved Pending Decisions.
- **Minimal Fix Design:** Exact changes per file (backend & frontend), keeping scope strictly limited to `<TKT>`.
- **Edge Cases & Regression Risks:** Anything adjacent that could break (e.g., quota off-by-one, cache headers, auth cookies).
- **Verification Plan:** Automated tests + live repro steps to confirm resolution.

## Step 4: Implement the Minimal Fix

1. Apply the changes scoped strictly to `<TKT>`.
2. Add or finalize automated regression test(s) covering the root cause (e.g., `go test -race` for concurrency bugs).
3. Avoid modifying files belonging to other tickets unless strictly required and explicitly documented.

## Step 5: Verify (Capture "After" Evidence)

1. Re-run the exact reproduction steps/tests from Step 2 and capture the **After** output proving the defect is gone.
2. Run project checks:
   ```powershell
   # Backend checks
   Push-Location api; go vet ./...; go test -race ./...; go build -o bin/api ./cmd/server; Pop-Location

   # Frontend checks
   Push-Location web; bun run check; bun run lint; Pop-Location
   ```
   All checks must pass before committing.

## Step 6: Generate Fix Report & Update Trackers

1. Create `docs/issues/<TKT>/<TKT>-fix-report.md` using this structure:
   - **Summary (Non-Engineer Friendly):** Plain-language explanation of what went wrong and how it is fixed.
   - **Root Cause (Technical):** Exact code/architecture cause with file references.
   - **Changes Made:** Concise list of modified/added files and what changed.
   - **Proof of Completion (Before vs. After):** Concrete repro steps + captured Before/After outputs or evidence links.
   - **Regression Checks:** Test/lint/build verification results.
2. Update `docs/Issue.md` status for `<TKT>` to `Resolved`.
3. Sync the `<TKT>` section into root `FIX_REPORT.md` (create `FIX_REPORT.md` if it does not exist yet).

## Step 7: Atomic Commit (1 Commit per Issue)

Stage only `<TKT>` code/test changes (and its ticket report/docs if agreed) and commit with a clear message explaining root cause and solution:

```text
fix(<TKT>): <concise summary of fix>

Root cause: <1-2 sentences explaining why the bug happened>
Solution: <1-2 sentences explaining the minimal fix and verification>
```

Record the resulting commit SHA in `docs/Issue.md`.

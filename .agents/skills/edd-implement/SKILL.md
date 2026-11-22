---
name: edd-implement
description: >-
  Phase 2 of the Error-Driven Development (EDD) workflow — executes an approved
  TKT-XXX fix plan using Red-to-Green regression tests, captures "After"
  verification evidence, generates the fix report, pauses for diff review, and
  creates the atomic fix commit. Use after edd-plan has produced an approved
  <TKT>-plan.md.
---

# EDD Phase 2: Implement, Verify, Report & Commit (`TKT-XXX`)

Follow these steps strictly in order to execute an approved `<TKT>-plan.md`.

## Step 1: Verify Preconditions

1. Read `docs/issues/<TKT>/<TKT>.md`, `docs/issues/<TKT>/<TKT>-plan.md`, and the raw files under `docs/issues/<TKT>/evidence/before-*`.
2. Confirm all preconditions are met:
   - `docs/issues/<TKT>/<TKT>-plan.md` exists and covers the planned changes.
   - All **Pending Decisions** in `<TKT>.md` are finalized (`- [x]`).
   - **Before** reproduction evidence exists (unless the user explicitly waived live repro).
3. If any precondition is missing, **stop** and ask the user to complete `edd-plan` first.

## Step 2: Red → Green Minimal Implementation

1. **Red (Regression Tests First, where applicable):**
   - Write the automated Go regression test(s) defined in `<TKT>-plan.md` (using `go test -race` for concurrency issues).
   - Run the new test(s) against the unfixed code and confirm they **fail** for the expected root cause.
2. **Green (Minimal Fix):**
   - Apply the exact backend and/or frontend changes scoped strictly to `<TKT>` as specified in `<TKT>-plan.md`.
   - Avoid modifying files belonging to other tickets unless strictly required and explicitly documented.
   - Re-run the regression test(s) and confirm they now **pass**.

## Step 3: Verify & Capture "After" Evidence

1. Run the full project verification gate (all checks must pass):
   ```powershell
   # Backend checks
   Push-Location api; go vet ./...; go test -race ./...; go build -o bin/api ./cmd/server; Pop-Location

   # Frontend checks
   Push-Location web; bun run check; bun run lint; Pop-Location
   ```
2. Re-run the exact live reproduction steps from `edd-plan` against the updated build (ensure local API/Web processes are running the newly built code) and save verbatim **After** evidence under `docs/issues/<TKT>/evidence/after-*.txt`.

## Step 4: Generate Fix Report & Update Trackers

1. Create `docs/issues/<TKT>/<TKT>-fix-report.md` using this structure:
   - **Summary (Non-Engineer Friendly):** Plain-language explanation of what went wrong and how it is fixed.
   - **Root Cause (Technical):** Exact code/architecture cause with file references.
   - **Changes Made:** Concise list of modified/added files and what changed.
   - **Proof of Completion (Before vs. After):** Concrete repro steps + quoted/linked Before and After outputs.
   - **Regression Checks:** Automated test (`go test -race`), `go vet`, build, `bun run check`, and `bun run lint` results.
2. Update `docs/Issue.md` for `<TKT>` (mark status `Resolved` and Pending Decisions as `Resolved`).
3. Sync the `<TKT>` section into root `FIX_REPORT.md` (create `FIX_REPORT.md` if it does not exist yet).

## Step 5: Pause for Quick Diff Review

Before committing, present a concise review summary to the user:
- `git status --short` and `git diff --stat`
- Key behavioral changes & verification summary (automated checks + Before vs. After highlights)
- Proposed atomic commit message

**Hold here** until the user approves the diff and gives the go-ahead to commit.

## Step 6: Atomic Commit (1 Commit per Issue)

Once approved, stage only `<TKT>` changes (code, regression tests, `docs/issues/<TKT>/`, `docs/Issue.md`, and `FIX_REPORT.md`) and commit:

```text
fix(<TKT>): <concise summary of fix>

Root cause: <1-2 sentences explaining why the bug happened>
Solution: <1-2 sentences explaining the minimal fix and verification>
```

After committing, note the resulting short commit SHA so it can be recorded in `docs/Issue.md` (in a subsequent tracker/docs update rather than mutating the fix commit).

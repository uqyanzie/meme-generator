---
name: edd-plan
description: >-
  Phase 1 of the Error-Driven Development (EDD) workflow — investigates a TKT-XXX
  issue, aligns pending decisions, captures raw "Before" reproduction evidence,
  and writes a reviewable fix plan without editing application code. Use whenever
  starting, investigating, reproducing, or planning any TKT-101 through TKT-109
  ticket.
---

# EDD Phase 1: Investigate, Reproduce & Plan (`TKT-XXX`)

Follow these steps strictly in order. **Do NOT modify application code (`api/`, `web/`) during this skill.** The output of this skill is verified "Before" evidence and a reviewable fix plan (`<TKT>-plan.md`) that hands off cleanly to `edd-implement`.

## Step 1: Load Context & Align Pending Decisions

1. Read `docs/issues/<TKT>/<TKT>.md` and `docs/Issue.md`.
2. Inspect the **Pending Decisions** section in `<TKT>.md`.
   - If any decision is still marked **Pending**, pause and align with the user before finalizing the plan.
   - Once agreed, record finalized decisions (`- [x]`) in `docs/issues/<TKT>/<TKT>.md`.
3. Explore the relevant backend and/or frontend code paths to pinpoint the exact root cause (file paths, line ranges, and mechanism).

## Step 2: Reproduce First (Capture "Before" Evidence)

1. Ensure the local stack is running if live reproduction is needed (see `.agents/skills/local-dev/SKILL.md`).
2. Reproduce the exact defect **before changing any application code**:
   - **Backend issues:** Run exact `curl`/PowerShell/DB/Redis commands that prove the defect and save raw output verbatim under `docs/issues/<TKT>/evidence/before-api-repro.txt` (or descriptive `before-*.txt`).
   - **Frontend issues:** Reproduce via live browser flow / network inspection and save verbatim observations (plus optional logs/screenshots) under `docs/issues/<TKT>/evidence/before-browser-repro.txt`.
3. Never leave modified or untracked application code (`api/`, `web/`) behind from reproduction.

## Step 3: Write the Fix Plan & Hold

Create `docs/issues/<TKT>/<TKT>-plan.md` containing:

- **Root Cause Analysis:** Exact file paths, line ranges, and mechanism (not just symptoms).
- **Before Reproduction Evidence:** Concise summary referencing the raw files in `docs/issues/<TKT>/evidence/`.
- **Agreed Decisions:** Summary of all finalized decisions from `<TKT>.md`.
- **Minimal Fix Design:** Exact changes per file (backend & frontend), keeping scope strictly limited to `<TKT>`.
- **Edge Cases & Regression Risks:** Adjacent behavior that could break (e.g., cookie attributes, cache headers, quota off-by-one, race conditions).
- **Verification Plan:**
  - Exact automated regression tests to add (Red → Green in `edd-implement`).
  - Static analysis / test / lint / build gates.
  - Exact live "After" reproduction steps mirroring Step 2.

### Hard Stop (Review Gate)

Stop after writing `<TKT>-plan.md` and present a concise summary with links to the plan and Before evidence. Do **not** edit application code until the user reviews the plan and invokes `edd-implement` (or explicitly approves implementation).

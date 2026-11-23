# Issue Tracker — Meme Generator

Master tracker for all 9 production issues from the **Take-Home Assignment (PT Lintas Cakra Cipta)**.

We resolve tickets using **Repro-Test-First EDD (Error-Driven Development)** in dependency-aware order. Each ticket is co-located under `docs/issues/<TKT>/` (`<TKT>.md`, `<TKT>-plan.md`, `<TKT>-fix-report.md`, and optional `evidence/`).

---

## Execution Order & Status

| Order | Ticket | Title | Layer | Depends On / Unblocks | Pending Decisions | Status | Commit |
| :---: | :--- | :--- | :--- | :--- | :--- | :--- | :--- |
| 1 | [TKT-103](./issues/TKT-103/TKT-103.md) | Broken Logout Flow & Infinite Redirect Loop | Backend + Frontend | Unblocks multi-user testing | Resolved | Resolved | `128daba` |
| 2 | [TKT-102](./issues/TKT-102/TKT-102.md) | Inconsistent Subscription Status & Cross-User Data Leakage | Backend | Unblocks clean cache repros for 101 & 104 | Resolved | Resolved | `1e4ba58` |
| 3 | [TKT-104](./issues/TKT-104/TKT-104.md) | Stale Subscription State Until Hard Refresh | Frontend | Paired with TKT-102 | Resolved | Resolved | `263fdfd` |
| 4 | [TKT-101](./issues/TKT-101/TKT-101.md) | Meme Creation Permitted After Subscription Expiration | Backend | Cleaner after TKT-102 & TKT-104 | Resolved | Resolved | `85014c9` |
| 5 | [TKT-105](./issues/TKT-105/TKT-105.md) | Duplicate Checkout / Double Billing Creation | Frontend + Backend | Checkout flow | Resolved | Resolved | `89919c2` |
| 6 | [TKT-106](./issues/TKT-106/TKT-106.md) | Unresponsive "Pay Now" Button Without Feedback | Frontend (+ Backend verify) | Checkout flow | Resolved | Resolved | `522d217` |
| 7 | [TKT-107](./issues/TKT-107/TKT-107.md) | Unauthorized Viewing & Deletion of Other Users' Memes (IDOR) | Backend | Unblocks `ResultUpload` (`GetByID` scan bug) | Resolved | Resolved | `ff64864` |
| 8 | [TKT-108](./issues/TKT-108/TKT-108.md) | Arbitrary / Malicious File Uploads in S3 Storage | Backend + Frontend | Meme upload flow | Resolved | Resolved | `e33bfdf` |
| 9 | [TKT-109](./issues/TKT-109/TKT-109.md) | Quota Bypass via Concurrent Requests (Race Condition) | Backend | Final meme creation flow | Resolved | Resolved | `2c63732` |

---

## Directory Convention

```text
docs/
├── Issue.md                         # This master tracker
└── issues/
    └── TKT-XXX/
        ├── TKT-XXX.md               # Formal problem statement & pending decisions
        ├── TKT-XXX-plan.md          # Root cause analysis & EDD fix plan
        ├── TKT-XXX-fix-report.md    # Generated post-fix report (synced to root FIX_REPORT.md)
        └── evidence/                # Optional before/after logs or screenshots
```

Final deliverable report compiled at repository root: `FIX_REPORT.md`.

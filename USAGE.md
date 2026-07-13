# Usage Guide

`calibre-ai-auditor` v1.0 gives you two ways to manage your Calibre library metadata:

1.  **WebUI** — interactive review with the v1.0 per-field verdict rendering
2.  **CLI** — automatable for batch runs and cron jobs

This guide walks through the canonical workflows for each.

---

## 1. WebUI Workflow (Recommended)

The WebUI is the primary interface for v1.0 content-ground verification.
Designed for homelab use with multi-host inference.

### Dashboard

Start at the **Dashboard** to see:
- System health (Calibre CLI, Tika, Qdrant)
- Homelab inference host discovery (3090 + Unraid Ollama + remote)
- Aggregated v1.0 verdict counters (no_change / suggest_fix / needs_review / defer)
- Review queue size, duplicates count, applied fixes

### Verify (v1.0) page — the new core workflow

The **Verify** page is where you trigger the v1.0 content-ground engine
across your library.

1. Navigate to **Verify (v1.0)** in the sidebar.
2. Set **Limit** (e.g. `50` books for a pilot run, `0` for the full library).
3. Toggle **Use LLM witness** if you want ambiguous fields sent to the
   configured LLM for adjudication. Leave off for fast deterministic-only runs.
4. Click **Run v1.0 Verify**. The progress bar updates every 1.5s.
5. When the run completes, click the row in **Recent verify runs** to drill
   into per-book verdicts.

**Action semantics**:
- `no_change` — declared metadata matches observed content; no fix needed
- `suggest_fix` — declared metadata disagrees with content; engine proposes a fix
- `needs_review` — high-risk flag (`author_swap`, `isbn_conflict`, etc.) requires human eyes
- `defer` — insufficient signal to decide deterministically; LLM witness should resolve

**Auto-apply semantics**: books marked `auto_apply_eligible: true` in the
verdict are queued for the next `bookaudit apply` run.

### Review page — per-field verdicts

When you click a book in the Review queue, the v1.0 per-field verdict rendering
shows:

- **Green chip** — `Confirmed` (declared matches content)
- **Red chip** — `Mismatch` (declared disagrees with content); shows `declared` vs `observed` side-by-side
- **Amber chip** — `Missing` (declared is None but observed has a value)
- **Purple chip** — `Ambiguous` (deterministic engine couldn't decide; LLM witness will resolve)
- **Risk badges** — `author_swap`, `isbn_conflict`, `wrong_book`, `series_mismatch`, `publisher_mismatch`

The **auto-apply ready** badge appears when the book passes the conservative
auto-apply gate. **manual review** means human approval is required.

### Other pages

- **Scan Library** — Triggers a Calibre DB scan (legacy v0.9 path; v1.0 uses
  Verify instead)
- **Inspect File** — Single-file inspection; useful before adding to library
- **Duplicates** — Qdrant-backed semantic duplicate detection
- **Changes & Undo** — v1.0 restore points + API-queued run revert
- **Settings** — Provider config, privacy filters, local API key

---

## 2. CLI Workflow

The CLI is ideal for batch jobs, CI, and offline single-file inspection.

### Sanity checks

```bash
# Verify all dependencies are reachable
bookaudit doctor

# Discover homelab inference hosts (3090 + Unraid Ollama)
bookaudit hosts
```

### v1.0 content-ground verification

```bash
# Pilot: 100 books, no LLM, text output
bookaudit verify --limit 100

# Pilot with LLM witness (slower, more accurate on ambiguous fields)
bookaudit verify --limit 100 --use-llm

# Full library as JSON (parseable, ~8k books/sec on dev container)
bookaudit verify --limit 0 --format json > v1_audit.json

# Apply auto-eligible fixes
bookaudit apply --safe-only

# Bulk undo a run
bookaudit undo <run_id>
```

### Single-file inspection (no Calibre library required)

```bash
# Extract metadata from one file
bookaudit inspect --path "/path/to/book.epub"

# Skip provider fetching (deterministic only)
bookaudit inspect --path "/path/to/book.epub" --no-providers
```

### Library management (legacy v0.9 paths, still work)

```bash
# Scan a Calibre library into the auditor DB
bookaudit scan --limit 100

# Run the v0.9 audit engine (one-shot LLM call per book)
bookaudit audit --run latest

# Apply approved fixes with OPF backup
bookaudit apply --run latest

# Roll back via OPF backup
bookaudit undo <change_id>
```

---

## 3. Real-world calibration on Unraid

Before relying on auto-apply in production, calibrate the thresholds
against your actual library. The full procedure lives in
[docs/calibration/v1.0_calibration_runbook.md](docs/calibration/v1.0_calibration_runbook.md).

Summary:
1. Run a 100-book pilot: `bookaudit verify --limit 100 --format json > pilot.json`
2. Manually classify 20 books: precision = TP / (TP + FP)
3. If precision < 95%, tune `AUTO_APPLY_MIN_CONFIDENCE` in `verification/verdict.py`
4. Re-run full library: `bookaudit verify --limit 0 --format json > full.json`
5. Update [tests/benchmarks/BASELINE.md](tests/benchmarks/BASELINE.md) with your real numbers

# Usage Guide

`calibre-ai-auditor` provides a Manifestation V2 CLI/API path plus the legacy
V1 WebUI workflow:

1. **CLI/API (V2 default)** — exact-edition, all-format, sealed audits and
   supervised per-package correction
2. **WebUI (legacy V1 shape)** — interactive per-field `BookVerdict` review

This guide walks through the canonical workflows for each.

---

## 1. Manifestation V2 workflow (recommended)

Run a shadow pilot; this never modifies Calibre:

```bash
bookaudit verify --pipeline v2 --limit 100 --use-ocr --format json > v2-pilot.json
```

For each book, V2 snapshots the current Calibre metadata, hashes and inspects
every attached format, optionally OCRs PDF front matter, queries Google Books
and Open Library by one exact checksum-valid ISBN, and persists a checksummed Tier
A/B/C package. One book failure is recorded and the next book continues.

- **Tier A**: exact internal and structured external manifestation evidence,
  complete core-field agreement; review the canonical patch.
- **Tier B**: missing/incomplete evidence, a single OCR/vision candidate, or an
  unreadable format; human research is required.
- **Tier C**: conflicting formats or exact provider records; do not apply.

LLM and cover vision are recognition aids, not authorities. Use remote consent
flags only after enabling the corresponding global privacy setting. All allowed
egress produces a receipt in the sealed package.

To correct a Tier A book, authorize its exact evidence ID through
`POST /api/review/v2/{evidence_id}/authorize`, then queue it with
`POST /api/apply/v2` and `force=true`. The sole writer verifies that Calibre
metadata, attached-format membership, paths, and ebook hashes have not changed,
creates rollback artifacts, applies canonical fields, and reads the result back.
See [docs/API.md](docs/API.md) for request bodies.

## 2. WebUI workflow (legacy V1 review shape)

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

**Historical auto-apply flag**: `auto_apply_eligible` is still rendered for V1
review compatibility, but no public legacy apply path consumes it.

### Review page — per-field verdicts

When you click a book in the Review queue, the v1.0 per-field verdict rendering
shows:

- **Green chip** — `Confirmed` (declared matches content)
- **Red chip** — `Mismatch` (declared disagrees with content); shows `declared` vs `observed` side-by-side
- **Amber chip** — `Missing` (declared is None but observed has a value)
- **Purple chip** — `Ambiguous` (deterministic engine couldn't decide; LLM witness will resolve)
- **Risk badges** — `author_swap`, `isbn_conflict`, `wrong_book`, `series_mismatch`, `publisher_mismatch`

The historical **auto-apply ready** badge reflects the V1 rule result only; it
does not authorize a write. **manual review** means human approval is required.

### Other pages

- **Scan Library** — Triggers a Calibre DB scan (legacy v0.9 path; v1.0 uses
  Verify instead)
- **Inspect File** — Single-file inspection; useful before adding to library
- **Duplicates** — Qdrant-backed semantic duplicate detection
- **Changes & Undo** — v1.0 restore points + API-queued run revert
- **Settings** — Provider config, privacy filters, local API key

---

## 3. Additional CLI workflows

The CLI is ideal for batch jobs, CI, and offline single-file inspection.

### Sanity checks

```bash
# Verify all dependencies are reachable
bookaudit doctor

# Discover homelab inference hosts (3090 + Unraid Ollama)
bookaudit hosts
```

### Manifestation V2 verification

```bash
# Pilot: 100 books, local extraction/OCR, text output
bookaudit verify --pipeline v2 --limit 100 --use-ocr

# Add bounded LLM transcription evidence (still non-authoritative)
bookaudit verify --limit 100 --use-llm

# Full library as JSON; resume with the emitted run_id if interrupted
bookaudit verify --limit 0 --format json > manifestation-v2-audit.json
```

`bookaudit apply` is retired and exits nonzero; `POST /api/apply` returns HTTP
410. Use the V2 authorization and queue endpoints. `bookaudit undo` accepts one numeric
`change_id`, not a run ID.

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

# Roll back via OPF backup
bookaudit undo <change_id>
```

---

## 4. Real-world calibration

Use calibration to measure the pipeline against your actual library. It does
not enable automatic writes. The full procedure lives in
[docs/calibration/manifestation-v2-runbook.md](docs/calibration/manifestation-v2-runbook.md).

Summary:
1. Run a shadow pilot and review exact manifestations, not just works.
2. Label at least 100 packages, including at least 50 Tier A decisions.
3. Generate an integrity-checksummed advisory report with `bookaudit calibrate-v2`.
4. Require zero false hypothetical automatic writes and the configured
   false-positive rate before considering any future authenticated gate.
5. Repeat whenever policy, extractors, providers, models, or the representative
   library distribution changes.

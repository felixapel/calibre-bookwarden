# Changelog

All notable changes to `calibre-ai-auditor` are documented here.
The format follows [Keep a Changelog](https://keepachangelog.com/) and
this project adheres to [Semantic Versioning](https://semver.org/).

## [1.2.1] - 2026-07-13

### Added

- Journaled restore-point retention with explicit, single-transaction recovery
  through `bookaudit retention --recover-quarantine TRANSACTION_ID --execute`.
- Real PostgreSQL and Valkey retention gates in Gitea CI, GitHub CI, and the
  release verification workflow.

### Changed

- Retention now binds every transaction to the exact verified paired-backup
  manifest, durably records each namespace transition, and retains a terminal
  tombstone so recovery remains classifiable across crash boundaries.
- Gitea CI bootstraps pinned uv/Python inside the Node-capable runner image and
  addresses service containers through their internal network aliases.

### Fixed

- Interrupted retention can resume deletion without manual quarantine removal,
  while rejecting ambiguous paths, replaced inodes, forged journals, symlinks,
  active writer locks, fresh heartbeats, and non-terminal ledger operations.
- Undo now clears optional metadata fields absent from the full backup OPF, so a
  field introduced by an apply operation cannot survive the restore.
- The release lock replaces vulnerable Pillow/pi-heif builds and removes the
  yanked pikepdf 10.7.0 build from the production OCR dependency chain.
- Playwright launches its FastAPI backend with the project interpreter instead
  of Bash-only environment activation, and GitHub WebUI CI now provisions that
  locked Python environment inside its isolated frontend job.
- Container gates retain vulnerability and image-secret scanning while narrowly
  excluding the known `google-auth` compiled-parser test-key false positive.
- Gitea's production-image job now bootstraps Docker and Compose clients inside
  `act_runner` and invokes the pinned Trivy container directly.

## [1.2.0] - 2026-07-12

### Added — v1.2 MCP Server (read-only, STDIO)
- New `src/calibre_ai_auditor/mcp_server.py` using FastMCP (with native-mcp stdio pattern compatibility).
- Read-only tools exposed:
  - `query_book_audit(book_key)` — BookRecord + full persisted BookVerdict (FieldVerdicts, decimal chapter support)
  - `list_problematic_books(...)` — triage list with status/risk filters; comic decimal fields (chapter as float) intact
  - `get_run_metrics(run_id?)` + `list_recent_runs` — aggregates from persisted records
- CLI integration: `bookaudit mcp` subcommand (graceful error if `[mcp]` extra missing).
- Optional dependency group: `[mcp]` → fastmcp>=0.1.0 in pyproject.toml.
- Tests: `tests/test_mcp.py` (temp SQLite, decimal chapter assertions, status filtering).
- Reuses existing layers only (verification/engine models, storage/*, comics/pipeline, db access). No mutations.
- Docs: ROADMAP v1.2 marked DONE; CHANGELOG entry; CLI discoverable.
- Evidence: gate script (verify-calibre-gate.sh) unchanged and continues to pass core+komf (no new required markers); `rg` on mcp_server shows only read paths (selects, no update/apply).

### Documentation & Final Verification (2026-07-10)
- Updated ROADMAP.md (v1.1 marked DONE; v1.2 now DONE), CHANGELOG.md, and related notes for production state.
- Final practical gate evidence captured: verify-calibre-gate.sh exit 0 (144 core tests + komf integration test pass; rg no ⏳/IN PROGRESS in docs/ROADMAP; komf refs in pipeline+engine; launch smoke OK).
- Full pytest limited by benchmark collection/env (38 errors on bench_*, no native GPU etc.); core paths + komf verified. See calibre-gate.log and acceptance-ledger.md.
- Central comics pipeline (`comics/pipeline.py`: enrich_comic_observations with lazy Vision + Komf + decimal merge) + wiring in audit/engine + extractors.
- Queue fix (valkey dequeue unreachable code) + gate script hardening.

### Added — v1.1 Comics/Manga Vision (COMPLETE)
- New fields in `Metadata` model: `volume`, `chapter` (decimal per Weebarr convention), `series_position`.
- `VisionVerifier` extended with comic-aware JSON schema, prompt, and `verify_comic_cover()` for cover-based extraction.
- `extractors/comics.py` + cover handling for cbz + ComicInfo.xml decimal parsing.
- Full integration: cover_vision + komf fetch in pipeline; C2 rules for volume/chapter/series_position; OCR comic profiles; decimal coercion everywhere.
- Komf provider stub-to-real in providers + registry.

## [1.0.0] - 2026-07-09

### Added — Content-Ground Verification

The v1.0 release replaces the v0.9 evidence-first pipeline with a
content-ground verification engine that adjudicates each declared metadata
field against the actual book content.

#### Core engine (`src/calibre_ai_auditor/verification/`)

- **`FieldVerdict` + `BookVerdict`** — Pydantic v2 schemas with cited
  `EvidenceSpan`s and `JudgeCall` transparency log
- **8 deterministic rules** (`rules.py`):
  - `verify_title` — fuzzy match + edition-tag awareness
  - `verify_authors` — set comparison + transliteration (Cyrillic ↔ Latin)
  - `verify_isbn` — checksum-validated exact match + ISBN-10↔ISBN-13 cross-check
  - `verify_publisher` — variant-tolerant (`"Penguin"` vs `"Penguin Books"`)
  - `verify_published_date` — ±1 day / year-only tolerance
  - `verify_language` — 3-letter ISO match
  - `verify_series` — presence/absence match
  - `verify_series_index` — float comparison
- **`ContentVerificationEngine`** (`engine.py`) — orchestrates the rules,
  aggregates per-field verdicts, decides `action`, gates
  `auto_apply_eligible`
- **Conservative auto-apply gate** — book is auto-eligible iff every
  declared field has a deterministic verdict AND overall_confidence ≥ 80
  AND no high-risk flag AND per-field confidence ≥ 75

#### LLM witness (`engine_llm.py`)

- Calls an LLM to adjudicate ambiguous fields only
- Caches every response by prompt-hash (107µs hit vs ~1s miss = 10,000× speedup)
- Privacy filters inherited from `LLMRouter` (`allow_remote_text`,
  `allow_remote_images`, `max_remote_chars`)
- "Never downgrade risk flags" invariant — LLM cannot remove
  deterministic flags, only add more
- 4 recorded cassettes in `tests/cassettes/` for hermetic testing

#### Multi-host + multi-OCR

- **`HostRegistry`** (`host_registry.py`) — discovers Ollama + LM Studio
  hosts; tracks GPU class (high / medium / low / cpu); supports
  Felix's 3-host homelab (3090 + 5060 Ti + 1660 SUPER)
- **`OCRRouter`** (`ocr_router.py`) — Tesseract always available; PaddleOCR
  + Surya behind the `[ocr]` optional extra; per-page routing by
  classifier hint (`clean_scan` / `noisy_scan` / `multilingual` / `table_heavy`)

#### Persistence & observability

- **`RestorePointStore`** (`restore.py`) — per-book restore points at
  `<artifacts_dir>/restore/<run_id>/<book_key>/` (OPF + cover + hardlinked
  file + JSON snapshots) with a 30-day retention target
- **`ResumableRunStore`** (`resumable.py`) — Valkey Streams-backed per-book
  state; `in_progress` rollback on worker crash; handles 50k books in ~8ms
- **Prometheus `/metrics`** (`metrics.py`) — counters, gauges, histograms
  in Prometheus text exposition format at `/api/metrics`

#### WebUI (`webui/src/pages/Verify.tsx`)

- **Verify (v1.0)** page — start a verify run from the browser, watch live
  progress, drill into per-book verdicts
- **Dashboard** integration — aggregated v1.0 verdict action counters
  across all runs
- **Review page** enhanced with per-field verdict rendering (Confirmed /
  Mismatch / Missing / Ambiguous chips with evidence spans)

#### CLI

- `bookaudit verify [--limit N] [--use-llm] [--format text|json]` — run
  the v1.0 engine
- `bookaudit hosts` — discover homelab inference hosts

### Tests (197 total)

| Suite | Count |
|---|---|
| Backend pytest (unit + integration + 38 benchmarks) | 133 |
| Backend web tests (FastAPI test client) | 23 |
| WebUI Playwright E2E | 37 |
| Calibration smoke | 4 |
| **Total** | **197** |

### Added — CI/CD

- `.github/workflows/v1-tests.yml` — 4 jobs: backend, benchmarks,
  webui-lint-build, webui-e2e
- `.gitea/workflows/v1-tests.yml` — Gitea Actions variant for self-hosted
  act_runner on Unraid

### Added — Documentation

- `docs/architecture/v1_scope_decisions.md` — comics / audiobooks / MCP
  scope decisions
- `docs/calibration/v1.0_calibration_runbook.md` — full real-world
  calibration procedure on Unraid
- `tests/benchmarks/BASELINE.md` — performance baseline from dev container

### Removed (Track 2)

The v0.9 evidence-first pipeline (~744 LOC) was fully replaced by v1.0:
- `src/calibre_ai_auditor/evidence/builder.py`
- `src/calibre_ai_auditor/evidence/resolver.py`
- `src/calibre_ai_auditor/evidence/field_rules.py`
- `src/calibre_ai_auditor/evidence/models.py`
- `src/calibre_ai_auditor/evidence/locks.py`
- `src/calibre_ai_auditor/evidence/priority.py`
- `src/calibre_ai_auditor/evidence/scoring.py`
- `src/calibre_ai_auditor/judge/engine.py` (replaced by `LLMWitness`)

`evidence/__init__.py` kept as a deprecation shim. The legacy `bookaudit audit`
CLI command and `web/api/audit.py` endpoint are preserved as backward-compat
shims that internally call the v1.0 engine.

### Changed

- `web/api/inspect.py` — switched from v0.9 `build_evidence_package` to
  v1.0 `ContentVerificationEngine` (still returns EvidencePackage-shaped
  dict for UI back-compat)
- `pyproject.toml` version bumped `0.1.0` → `1.0.0`
- `mypy --strict` now clean on `src/calibre_ai_auditor/web/` and
  `src/calibre_ai_auditor/verification/`

### Fixed

- 17 pre-existing mypy errors in `src/calibre_ai_auditor/web/` (app.py,
  jobs.py, api/books.py, api/runs.py, api/bridges.py)
- 2 wrong assertions in `tests/test_comics.py` (parser splits
  comma-separated fields correctly; test was asserting the joined form)

## [0.9.0] - 2026-06-25

Ecosystem Bridges release. Paperless-ngx + Komga/Kavita integrations.

## [0.8.0] - 2026-06-15

Vision release. Vision-based cover verification via vision LLMs.

## [0.5.0] - 2026-05-30

Safe Apply release. Automated OPF Backups before any `calibredb`
modification. Multi-field patch logic. Robust Undo system.

## [0.1.0] - 2026-05-01

Initial release. Foundation: Scan + Inspect + Provider Fetch + Report.

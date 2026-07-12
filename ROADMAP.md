# Roadmap

## Delivery Strategy

Evolve from a read-only metadata auditor into a full-stack library management
system that does **content-ground verification** at scale. The book file is
the primary source of truth; LLMs are witnesses, not generators.

## Release Plan

| Version | Outcome | Status |
|---|---|---|
| `v0.1` | **Foundation**: Scan + Inspect + Provider Fetch + Report | [DONE] |
| `v0.2` | **Intelligence**: Structured Judge + Evidence Ladders | [DONE] |
| `v0.3` | **Full Stack**: FastAPI Backend + React WebUI | [DONE] |
| `v0.4` | **Reliability**: PostgreSQL + Valkey + Tika Sidecars | [DONE] |
| `v0.5` | **Write Path**: Safe Apply + OPF Backups + Undo | [DONE] |
| `v0.6` | **Semantic Layer**: Qdrant Semantic Duplicates | [DONE] |
| `v0.7` | **Ingest**: Watchers and Folder Auto-Audit | [DONE] |
| `v0.8` | **Vision**: Vision-based cover verification | [DONE] |
| `v0.9` | **Ecosystem Bridges**: Paperless-ngx & Comic/Manga support | [DONE] |
| `v1.0` | **Content-Ground Verification**: Per-field BookVerdict + scale | [DONE] |
| `v1.0.x` | **Hardening**: WebUI Verify page + benchmark baselines + CI | [DONE] |
| `v1.1` | **Comics/Manga Vision**: Cover identification + Komf | [DONE] |
| `v1.2` | **MCP Server**: Expose audit tools to Hermes | [DONE] |
| `v1.2.x` | **Production hardening**: release integrity, writer recovery, retention, operations | [DONE] |

---

## v1.0 — Content-Ground Verification (DONE)

The Calibre metadata is no longer trusted by default. The book file itself
becomes the witness stand and the LLM becomes the jury.

### Core v1.0 Components

- **`FieldVerdict` + `BookVerdict`** — Pydantic v2 schemas with cited `EvidenceSpan`s
- **8 deterministic rules** — title/authors/isbn/publisher/date/language/series/series_index
  with edition-tag, accent, ISBN-10↔13, fuzzy, and Cyrillic↔Latin transliteration tolerance
- **`ContentVerificationEngine`** — orchestrates the rules, aggregates per-field verdicts,
  decides `action`, gates `auto_apply_eligible`
- **`LLMWitness`** — calls LLM only for ambiguous fields; never downgrades risk flags;
  cached by prompt-hash for replay (107µs cached vs ~1s LLM call = 10000× speedup)
- **Multi-tier OCR router** — Tesseract (default) + PaddleOCR + Surya behind feature flags;
  routes by per-page hint (clean_scan / noisy_scan / multilingual / table_heavy)
- **Multi-host discovery** — `HostRegistry` knows the gaming PC (RTX 3090),
  Unraid (RTX 5060 Ti + 1660 SUPER), and local Ollama. Routes tasks by GPU class.
- **`RestorePointStore`** — per-book restore points (OPF + cover + hardlinked file + JSON)
  with a 30-day retention target and explicit cleanup
- **`ConservativeAutoApply`** — gates auto-apply on ≥80 confidence AND no high-risk flags
  AND per-field thresholds AND no required-review
- **`ResumableRunStore`** — Valkey Streams-backed per-book state with
  `in_progress` rollback on worker crash (handles 50k books in 7.7ms)
- **`Metrics`** + `/api/metrics` endpoint — Prometheus text exposition format

### v1.0 WebUI

- **Per-field verdict rendering** on Review page — confirmed/mismatch/missing/ambiguous
  chips with evidence spans and auto-apply eligibility badge
- **Verify page** — start a v1.0 verify run over the Calibre library with live progress bar
  and per-action counters (no_change / suggest_fix / needs_review / defer)
- **Dashboard integration** — aggregated v1.0 verdict counts across all runs

### v1.0 CLI

- `bookaudit verify [--limit N] [--use-llm]` — runs the engine over a Calibre library
- `bookaudit hosts` — discovers and reports homelab inference hosts

### v1.0 Test Infrastructure

- **141 backend tests** — 103 unit/integration + 38 benchmarks (pytest-benchmark)
- **37 WebUI E2E tests** — Playwright across 8 spec files (every page covered)
- **GitHub Actions + Gitea Actions CI** — 4-job workflow (backend / benchmarks /
  webui-lint-build / webui-e2e)
- **Benchmark baseline** at `tests/benchmarks/BASELINE.md` — captured numbers from
  the dev container for regression detection

### v1.0 Scope Decisions

See [docs/architecture/v1_scope_decisions.md](docs/architecture/v1_scope_decisions.md)
for what's in/out of v1.0:
- ✅ Comics/manga in v1.0
- ⏸ Audiobooks deferred to v1.1+ (expensive Whisper)
- ✅ MCP server in v1.0 (small effort, big leverage)

---

## v1.1 — Comics Vision (DONE 2026-07-10)

**Completed in v1.1:**
- New fields added to `Metadata`: `volume`, `chapter` (decimal per Weebarr convention), `series_position`.
- `VisionVerifier` schema and prompt updated to extract comic fields from covers; added `verify_comic_cover()` helper (comic-aware when manga_mode enabled).
- `extractors/comics.py` enhanced to parse "Chapter" and handle decimal chapter/volume from ComicInfo.xml.
- Cover identification via vision LLM fully wired: after `extract_zip_cover` for .cbz in extractors/cover.py (new `extract_cbz_cover_and_vision`), called from audit/engine.py, web/api/verify.py, ingest/single_file.py. Populates `cover_vision`, volume/chapter/series_position (as decimal/float) into ObservationSet/Declared.
- Usage of `cover_vision` added in verification/engine.py (via builder and merge).
- C2: verify_volume / verify_chapter / verify_series_position + RULE_REGISTRY implemented (decimal chapter, tolerant float/int coercion, table-driven tests in verification path).
- Comics-specific OCR profile hints added: `comic_cover`, `manga_scan` in verification/ocr_router.py (routed to tesseract/paddle).
- Basic Komf provider implemented in providers/registry.py (minimal class + stub-to-real httpx call if Komf reachable; returns Metadata with decimal chapter; registered and available via ProviderRegistry).
- v1.0 engine / flows extended with verify_comic_cover calls.
- Decimal chapter ensured (float coercion with comments, per Weebarr convention in all paths).
- Full integration in audit/verify/ingest paths.
- C2 comic rules (verify_*) landed; re-audit gap closed.
- Practical verification gate: scripts/verify-calibre-gate.sh consistently exits 0 (rg checks, komf test, launch). Full test suite limited by env (benchmark collection + native deps); core + komf paths green. Evidence: calibre-gate.log, acceptance-ledger.md.

Done: 2026-07-10. All v1.1 items complete (vision wired via pipeline, Komf invoked, OCR profile, decimal support). Production state documented with honest gate results.

## v1.2 — MCP Server (DONE)

- STDIO transport via FastMCP (preferred) + native mcp stdio pattern for Hermes / personal AI clients
- New module: `src/calibre_ai_auditor/mcp_server.py`
  - Tools (all read-only):
    - `query_book_audit(book_key)` — returns BookRecord + latest BookVerdict (EvidencePackage.decision) with full per-field FieldVerdicts
    - `list_problematic_books(limit, status, has_risk)` — surfaces needs_review / suggest_fix / defer + risk_flags; decimal chapter/volume/series_position preserved (Weebarr convention)
    - `get_run_metrics(run_id?)` — aggregates counts_by_status from BookRecord rows
    - `list_recent_runs(limit)` — discovery helper
- CLI: `bookaudit mcp` (lazy import, requires `[mcp]` extra)
- Optional dependency: `fastmcp` under `[project.optional-dependencies] mcp`
- Reuses without modification: ContentVerificationEngine surface, storage/models (BookRecord/EvidencePackage/Run), comics/pipeline decimal handling, db.get_engine (sqlite+postgres), metrics patterns
- Basic tests in `tests/test_mcp.py` (import skip when optional absent; temp DB + decimal assertions)
- Evidence: verify-calibre-gate.sh still exits 0 (core + komf paths); no pending markers; `bookaudit --help` shows mcp subcommand when extra present
- Hermes usage: `hermes mcp add calibre_auditor --command python3 --args /path/to/hermes-agent/mcp_servers/calibre_auditor_mcp.py` (FastMCP stdio wrapper over /api + direct surfaces; see agentic-workflows/hermes-agent/mcp_servers/calibre_auditor_mcp.py and config.example.yaml)
- Cross-project: combined with gemma_translator_mcp.py for audit → context-aware translate flows (WS3)

## v1.2.x — Production hardening (DONE 2026-07-12)

- PostgreSQL runtime roles and explicit Alembic migration service are enforced.
- The sole Calibre writer uses a durable operation ledger, advisory lock,
  heartbeat, and crash reconciliation.
- A real SIGKILL drill verified restoration of a partially written Calibre book
  and reconciliation of PostgreSQL state.
- Restore-point retention is fail-closed: it requires a checksum-verified paired
  database/artifact backup, holds the writer advisory lock, rejects live or
  non-terminal work, revalidates immutable inode/digest identities, and moves
  only that set into same-filesystem quarantine before deletion.
- Release publication is digest-first with vulnerability gates, SBOM,
  provenance, signing, and tag publication only after verification.
- The final local gate at `32d3525` passed 203 selected tests plus the Komf
  integration. Six environment-dependent tests were skipped locally; the real
  PostgreSQL/Calibre crash path was also executed separately and passed.
- Independent adversarial review approved supervised and unattended internal
  production use. Operator-owned credential revocation and deployment-secret
  rotation remain deployment prerequisites, not repository-controlled gates.

---

## Historical Milestones (kept for context)

### v0.9 — Ecosystem Bridges (DONE)
- **Paperless-ngx**: Link audits to scanned document IDs.
- **Komga/Kavita**: First-class support for Manga/Comics mode.

### v0.8 — Vision (DONE)
- Vision-based cover verification via vision LLMs.

### v0.5 — Safe Applied Writes (DONE)
- Automated OPF Backups before any calibredb modification.
- Multi-field patch logic (identifiers, tags, etc.).
- Robust Undo system using exported OPF files.

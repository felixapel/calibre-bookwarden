# Roadmap

## Delivery Strategy

Deliver a local, supervised Calibre metadata auditor that identifies the exact
ebook manifestation when evidence permits, explains uncertainty, and applies
only a reviewed, reversible correction. It does not aim to replace Calibre or
perform unattended bulk writes. See
[ADR-003](docs/decisions/ADR-003-supervised-local-auditor-scope.md).

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
| `v1.1` | **Comics/Manga Vision**: cover and ComicInfo support; experimental Komf adapter | [PARTIAL] |
| `v1.2` | **MCP Server**: legacy V1 read model for Hermes | [PARTIAL] |
| `v1.2.x` | **Production hardening**: release integrity, writer recovery, retention, operations | [DONE] |
| `Manifestation V2` | **Exact edition audit**: all formats, evidence tiers, supervised correction | [IMPLEMENTED; PILOT REQUIRED] |
| `Remote Content Server` | **Read-only intake**: aggregate inventory and source-bound shadow audit | [IMPLEMENTED; LIVE USE REQUIRES OPERATOR SCOPE] |

---

## Manifestation V2 development head

The V2 backend is implemented behind a versioned contract and defaults to
shadow verification. It freezes local or Content Server membership, processes
one book at a time, hashes and inspects every attached format, runs bounded recognition,
queries structured providers only by one checksum-valid ISBN, resolves Tier
A/B/C identity, and persists a sealed evidence package.

Supervised correction is implemented through exact package authorization and
the sole writer with OPF, custom-column, and cover rollback evidence. Unattended
operation is outside the accepted product scope; calibration reports measure a
reviewed corpus but cannot authorize writes. External Calibre, PostgreSQL,
Valkey, OCR, and browser gates must also pass in the target environment.

Current deliberate limits:

- Google Books and Open Library are the implemented structured adapters.
- Vision and LLM observations are review context, never Tier A authority.
- Provider cover URLs are not downloaded automatically; a V2 cover patch must
  reference a locally materialized, hashed artifact bound to the exact ISBN.
- The WebUI has a V2-native review/apply surface. V1 records remain
  historical/read-only.

See [ADR-002](docs/decisions/ADR-002-exact-manifestation-v2.md) and the
[V2 calibration runbook](docs/calibration/manifestation-v2-runbook.md).

---

## Current critical path

1. **Read-only inventory data:** the capability-limited Content Server adapter
   and disposable local gate are implemented. With explicit operator
   authorization, use the verified loopback tunnel/account/library identity to
   collect only aggregate format, language, valid/missing ISBN, multi-format,
   and incomplete-field distributions from the intended library. Then review a
   single `--limit 1` shadow audit. Do not write or widen scope implicitly.
2. **Reviewed corpus:** build a stratified sample of approximately 100 books
   whose exact manifestation and proposed patch are labeled by a human. Measure
   Tier A/B/C distribution, exact-identity precision, patch precision, provider
   conflicts, latency, egress, and review time.
3. **Durable audit execution:** replace in-process WebUI background tasks with
   a restart-safe PostgreSQL lease/worker contract before treating long audits
   as operationally reliable.
4. **Evidence coverage:** prioritize PDF/front-matter and no-ISBN workflows only
   after the inventory and corpus identify the dominant gaps. Discovery without
   exact evidence remains review-only.
5. **Canonical V2 model:** migrate UI, MCP, persistence, and historical reads
   toward one V2 run/evidence contract; deprecate legacy write surfaces rather
   than extending them.
6. **Promotion:** repeat the disposable drill with the immutable image, then a
   restored-library clone rehearsal. Live canaries remain a separate explicit
   operator decision, serial and limited to five.

Do not start unattended apply, Paperless/Qdrant expansion, ingest automation,
audiobooks, Komga/Kavita writes, or advanced manga matching before the reviewed
corpus establishes product value and accuracy.

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
- **Multi-tier OCR router** — Tesseract (default) + optional PaddleOCR;
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

- Backend unit, integration, security, migration, and benchmark suites; exact
  counts are reported by each run rather than treated as a contract.
- Playwright coverage across the user-facing pages and V2 review workflow.
- **Gitea Actions CI** — canonical backend, real-service, benchmark, WebUI, and
  production-image jobs; GitHub workflow files are compatibility assets only
- **Benchmark baseline** at `tests/benchmarks/BASELINE.md` — captured numbers from
  the dev container for regression detection

### v1.0 Scope Decisions

See [docs/architecture/v1_scope_decisions.md](docs/architecture/v1_scope_decisions.md)
for what's in/out of v1.0:
- ✅ Comics/manga in v1.0
- ⏸ Audiobooks deferred to v1.1+ (expensive Whisper)
- ✅ MCP server in v1.0 (small effort, big leverage)

---

## v1.1 — Comics Vision (PARTIAL)

**Completed in v1.1:**
- New fields added to `Metadata`: `volume`, `chapter` (decimal per Weebarr convention), `series_position`.
- `VisionVerifier` schema and prompt updated to extract comic fields from covers; added `verify_comic_cover()` helper (comic-aware when manga_mode enabled).
- `extractors/comics.py` enhanced to parse "Chapter" and handle decimal chapter/volume from ComicInfo.xml.
- Cover identification via vision LLM fully wired: after `extract_zip_cover` for .cbz in extractors/cover.py (new `extract_cbz_cover_and_vision`), called from audit/engine.py, web/api/verify.py, ingest/single_file.py. Populates `cover_vision`, volume/chapter/series_position (as decimal/float) into ObservationSet/Declared.
- Usage of `cover_vision` added in verification/engine.py (via builder and merge).
- C2: verify_volume / verify_chapter / verify_series_position + RULE_REGISTRY implemented (decimal chapter, tolerant float/int coercion, table-driven tests in verification path).
- Comics-specific OCR profile hints added: `comic_cover`, `manga_scan` in verification/ocr_router.py (routed to tesseract/paddle).
- Experimental Komf adapter implemented in `providers/registry.py`. It is
  disabled by default and fails closed on unavailable, malformed, or empty
  responses. Its endpoint contract still requires validation against a pinned
  Komf deployment before it can be considered a supported integration.
- v1.0 engine / flows extended with verify_comic_cover calls.
- Decimal chapter ensured (float coercion with comments, per Weebarr convention in all paths).
- Full integration in audit/verify/ingest paths.
- C2 comic rules (verify_*) landed; re-audit gap closed.
- The local deterministic gate runs executable lint, type, test, and launch
  checks. Required Calibre/PostgreSQL/Valkey and browser/image contracts run in
  Gitea Actions.

The comic field, ComicInfo, cover-vision, OCR-profile, and decimal-position
paths are implemented. Komf remains experimental and does not make v1.1 a
complete supported provider integration.

## v1.2 — MCP Server (PARTIAL)

The current MCP tools read the legacy V1 `Run`/`EvidencePackage.decision`
shape. They do not yet expose Manifestation V2 as the canonical read model;
adapt or deprecate this surface before calling it V2-complete.

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
- The local gate proves the optional command can be discovered when the MCP
  extra is installed; V2 read-model coverage remains outstanding.
- Hermes usage: `hermes mcp add calibre_auditor --command python3 --args /path/to/hermes-agent/mcp_servers/calibre_auditor_mcp.py` (FastMCP stdio wrapper over /api + direct surfaces; see agentic-workflows/hermes-agent/mcp_servers/calibre_auditor_mcp.py and config.example.yaml)
- Cross-project: combined with gemma_translator_mcp.py for audit → context-aware translate flows (WS3)

## v1.2.x — Production hardening (DONE 2026-07-13)

- PostgreSQL runtime roles and explicit Alembic migration service are enforced.
- The sole Calibre writer uses a durable operation ledger, advisory lock,
  heartbeat, and crash reconciliation.
- A real SIGKILL drill verified restoration of a partially written Calibre book
  and reconciliation of PostgreSQL state.
- Restore-point retention is fail-closed: it requires a checksum-verified paired
  database/artifact backup, holds the writer advisory lock, rejects live or
  non-terminal work, revalidates immutable inode/digest identities, and moves
  only that set into same-filesystem quarantine before deletion.
- Interrupted retention is journaled across atomic publication, rename, partial
  deletion, and terminal boundaries. Recovery explicitly selects one transaction,
  requires the same verified backup manifest, and resumes deletion under the
  production guards.
- Release publication is digest-first with vulnerability gates, SBOM,
  provenance, signing, and tag publication only after verification.
- CI and release verification force the real PostgreSQL/Valkey retention gate;
  the PostgreSQL/Calibre crash path is also executed with its real dependencies.
- The historical `v1.2.1` release-candidate checks and the real
  PostgreSQL/Valkey retention gate passed against disposable services; exact
  test counts belong to the corresponding run evidence.
- A historical V1 review considered internal unattended operation. That review
  does not approve Manifestation V2, does not override ADR-003, and is not
  authorization to write to a live library. Operator-owned credential
  revocation and deployment-secret rotation also remain deployment
  prerequisites, not repository-controlled gates.

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

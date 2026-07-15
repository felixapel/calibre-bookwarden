# Integration Decisions

This document details the Architecture Decision Records (ADRs) for
`calibre-ai-auditor` v1.0. v0.9 ADRs are retained where still relevant; v1.0
adds three new ADRs for content-ground verification.

---

## ADR-001: Calibre Integration Layer

### Status
Accepted (reaffirmed for v1.0)

### Context
`calibre-ai-auditor` needs to retrieve book metadata from Calibre libraries
and write corrected metadata back. Calibre has an internal Python database
engine (`new_api`) as well as a CLI interface (`calibredb`).
- Directly importing Calibre's Python package requires running inside
  Calibre's custom python environment (`calibre-debug`), creating complex
  packaging issues.
- Running subprocess commands (`calibredb list`) is simple but introduces
  ~200ms per call, making library-wide scans slow.

### Decision
Hybrid integration model:
1.  **Read Operations (Scans)**: `calibredb list --for-machine --fields all`
    subprocess. Fast enough for v1.0 (~8300 books/sec on dev container);
    further optimization via `calibre-debug` Python imports is tracked for v1.0.1.
2.  **Write Operations (Applies)**: Always via the official CLI
    `calibredb set_metadata`. This ensures Calibre's database events, file
    names, and layout changes are synchronized safely without risk of
    database corruption.

### Consequences
- **Safety**: Writing via the official CLI prevents file tree
  de-synchronization and library corruption.
- **Performance**: Reading via direct CLI is acceptable for v1.0 scale.

---

## ADR-002: In-Memory Caching and Valkey Fallbacks

### Status
Accepted (reaffirmed for v1.0)

### Context
Audits involve heavy embedding lookups and LLM completions. These external
API calls are slow and can quickly exceed rate limits. While Valkey is the
target production queue and cache server, forcing local CLI developers to
spin up Valkey containers degrades developer ergonomics.

### Decision
Unified caching interface:
1.  Checks if Valkey is running. If active, uses Valkey for caching with
    strict TTL key evictions.
2.  Falls back to a thread-safe, process-level in-memory RAM dictionary
    if Valkey is offline.

### Consequences
- **Ergonomics**: Developers can run the CLI locally offline without
  deploying Docker services.
- **Resiliency**: Production deployments get shared worker caching;
  local deployments stay lightweight.

---

## ADR-003: Native LLM Provider Adapters over LiteLLM proxy

### Status
Accepted (reaffirmed for v1.0)

### Context
The original architecture proposed using LiteLLM to route tasks across
local Ollama and remote OpenAI providers.
- LiteLLM brings a heavy package footprint and complex dependency trees.
- We require precise structured JSON output and vision handling (such as
  Gemini's native structured SDK features).

### Decision
Clean, native async provider clients (Ollama, OpenAI, LM Studio, Google
Gemini) directly in Python.

### Consequences
- **Control**: We can directly utilize provider-specific SDK features
  (e.g. Google Gemini's structured models and Vision input formats).
- **Simplicity**: We reduce the project's dependency load, making Docker
  builds faster and image sizes smaller.

---

## ADR-004: v1.0 Content-Ground Verification (new)

### Status
Accepted

### Context
The v0.9 evidence-first pipeline (`build_evidence_package` →
`MetadataJudge` → `apply_confidence_thresholds`) treats each book as a
single decision and relies on the LLM as the primary truth source. This
resulted in:
- Hallucinated fixes when the LLM had insufficient evidence
- No auditable per-field reasoning
- An all-or-nothing apply path (no per-field partial fixes)
- No way to know WHY the engine made a decision

### Decision
v1.0 introduces the **ContentVerificationEngine** + per-field
**FieldVerdict** + **LLMWitness**:

1.  **The book file is the ground truth.** Content extraction always runs
    first; LLMs only adjudicate, never replace.
2.  **Eight deterministic rules per field** (title / authors / isbn /
    publisher / date / language / series / series_index) run before the
    optional witness. Their coverage and precision must be measured on a
    reviewed corpus. Rules have cited `EvidenceSpan`s so any decision can be
    traced back to the source.
3.  **LLMWitness is called only for `ambiguous` fields** — never as the
    primary source. Witness responses are cached by prompt-hash
    (`WitnessCache`) for replay. The witness is forbidden from removing a
    risk flag set by the deterministic engine — it can only add more.
4.  **Conservative auto-apply gate**: a book is auto-eligible iff every
    declared field has a deterministic verdict AND overall_confidence ≥ 80
    AND no high-risk flag AND per-field confidence ≥ 75.

### Consequences
- **Auditability**: every verdict has `EvidenceSpan`s citing the page and
  text that drove the decision. Review UI shows them inline.
- **Safety**: 30 hand-crafted gold-truth fixture regressions + 11 LLM
  witness cassette tests guard against regressions in the deterministic
  rules.
- **Cost control**: cache hit ratio is the most important cost lever;
  `bookaudit hosts` + Prometheus `/metrics` expose cache hit rate.
- **Legacy path retained**: the v0.9 `audit` CLI command and `/api/audit`
  endpoint still work as a thin shim that wraps the v1.0 engine.

---

## ADR-005: Per-Book Restore Points (new)

### Status
Accepted

### Context
The v0.9 apply path wrote only an OPF backup — sufficient for metadata
rollback but insufficient for:
- Recovering the original book file if Calibre's file tree changed
- Recovering the original cover image if it was replaced
- Bulk undo across an entire run

### Decision
v1.0 introduces `RestorePointStore` that creates a per-book snapshot
before every apply, at `<artifacts_dir>/restore/<run_id>/<book_key>/`:
- `original.opf` — pre-apply metadata
- `original.<ext>` — hardlink to the original book file
- `original.cover.<ext>` — original cover (if it was changed)
- `before.json` / `after.json` — full metadata snapshots
- `restore.json` — bulk-undo metadata

30-day TTL; bulk undo by `run_id` walks every entry.

### Consequences
- **Recoverability**: any apply can be undone to the exact pre-apply state
  in one command, including file and cover.
- **Disk usage**: hardlinks make the restore point nearly free when the
  underlying file is unchanged; copies only when the file moves.
- **Bulk undo**: `POST /api/runs/{run_id}/revert` queues an entire run for the sole writer
  in one command.

---

## ADR-006: Multi-Host Inference Routing (new)

### Status
Accepted

### Context
Felix's homelab has heterogeneous GPUs:
- RTX 3090 at 192.168.0.89 (24 GB VRAM) — heavy vision + 13B+ models
- RTX 5060 Ti + GTX 1660 SUPER at 192.168.0.122 (16 GB + 6 GB) — bulk OCR + embedding
- Remote OpenAI / Gemini — fallback

v0.9 used a single Ollama endpoint. v1.0 needs to route tasks by capability
so a 13B vision LLM doesn't run on a 6 GB GPU.

### Decision
`HostRegistry` discovers Ollama + LM Studio hosts via `/v1/models` and
tracks each host's `GPUClass` (`high` / `medium` / `low` / `cpu`). Tasks
route by class:
- `heavy_vision` → `high` (3090)
- `bulk_ocr` → `medium` (5060 Ti)
- `embedding` → `medium`

Multiple hosts can be registered; `bookaudit hosts` shows health.

### Consequences
- **Capability-aware**: no more running a 13B model on a 6 GB GPU.
- **Live discovery**: `bookaudit hosts` reveals what's actually
  reachable before a run.
- **Graceful degradation**: if the high-GPU host is down, fallback to
  medium-GPU or remote.

---

## ADR-007: Conservative Auto-Apply Policy

### Status
Accepted

### Context
v0.9 had a simple "needs_review" flag — anything ambiguous went to the
queue. At scale (10k–50k books) this becomes unusable. v1.0 introduces a
quantitative gate.

### Decision
A book is `auto_apply_eligible` iff:
1. Every declared field has a `FieldVerdict` (no `ambiguous` left)
2. `overall_confidence ≥ AUTO_APPLY_MIN_CONFIDENCE` (default 80)
3. No `HIGH_RISK_FLAGS` present
4. Every per-field confidence ≥ `AUTO_APPLY_MIN_FIELD_CONFIDENCE` (75)

Threshold constants live in
`src/calibre_ai_auditor/verification/verdict.py`. Tuning happens via the
calibration runbook (`docs/calibration/v1.0_calibration_runbook.md`)
against real-world library data.

### Consequences
- **Quantitative**: no more "feels right" — the gate is data-driven
- **Tunable**: precision / recall trade-off can be adjusted per deployment
- **Auditable**: every auto-eligible book has per-field evidence cited

---

## ADR-008: OCR Router per-page-classifier

### Status
Accepted

### Context
Scanned PDFs in real libraries have heterogeneous quality. A single OCR
backend can't be optimal for all pages.

### Decision
`OCRRouter` classifies each page (`clean_scan` / `noisy_scan` /
`multilingual` / `table_heavy` / `text_present`) and dispatches to:
- Tesseract (always available, fast on clean text)
- PaddleOCR (best on clean scans + CJK; optional `[ocr]` extra)
- Surya (historical design option; not packaged by this project)

### Consequences
- **Capability-aware**: each page gets the OCR it needs
- **Optional**: Tesseract is the fallback and PaddleOCR is opt-in via
  `pip install -e .[ocr]`; Surya requires separate, unsupported installation
- **Measured**: OCR comparison benchmark in
  `tests/benchmarks/test_bench_ocr_comparison.py` lets users compare on
  their own corpus

---

## ADR-009: WebUI + CLI Single Backend Port

### Status
Accepted

### Context
v0.9 had Playwright + FastAPI as two separate processes for E2E testing.
That's fragile and slow.

### Decision
v1.0 serves both the Vite-built React SPA and the `/api/*` endpoints from
the same FastAPI process on a single port (5174 in tests, 8080 in
production). The Vite dev server proxies `/api` to FastAPI during dev.

### Consequences
- **Simpler E2E**: Playwright tests run against a real FastAPI test client
  on a fixed port.
- **Single deployable**: one Docker image serves everything.
- **WebUI bundled**: `webui/dist/` ships inside the image; no separate
  nginx config needed.

---

## ADR-010: Resumable Runs on Valkey Streams

### Status
Accepted

### Context
Workers can crash mid-run (OOM, network blip, deploy). v0.9 had no
recovery — restart re-processed every book from scratch. For 10k+ book
libraries this is unacceptable.

### Decision
`ResumableRunStore` writes per-book state transitions to Valkey Streams.
On worker startup, all `in_progress` books are rolled back to `pending`.

### Consequences
- **Fault tolerance**: any worker can pick up where another left off
- **Idempotent**: re-running a completed book is a no-op (deterministic
  cache hit)
- **Observable**: per-book progress visible in real time on the WebUI
  Verify page

---

## ADR-011: Prometheus `/metrics` (new)

### Status
Accepted

### Context
v0.9 had no metrics. Production debugging required log scraping.

### Decision
`Metrics` collector exposes counters / gauges / histograms in Prometheus
text exposition format at `/api/metrics`. Domain helpers:
- `record_book_action(action, run_id)` — per-action book counts
- `record_llm_call(provider, model, ...)` — token + latency tracking
- `record_ocr_pages(provider, count)` — per-provider OCR throughput
- `set_run_progress(run_id, total, completed)` — per-run progress

### Consequences
- **Grafana-ready**: standard Prometheus format, no special config
- **CI benchmark**: `tests/benchmarks/` uploads `.benchmarks/baseline.json`
  as a workflow artifact for regression detection
- **Cost control**: cache hit ratio, $/book, $/LLM-call all trackable

---

## ADR-012: Dead Code Cleanup (Track 2)

### Status
Completed in v1.0

### Context
v0.9 had `evidence/` and `judge/` modules that were fully superseded by
v1.0. Keeping them created import confusion.

### Decision
Delete the dead modules:
- `src/calibre_ai_auditor/evidence/{builder,resolver,field_rules,models,locks,priority,scoring}.py` (~744 LOC)
- `src/calibre_ai_auditor/judge/engine.py` (~153 LOC)

Keep `evidence/__init__.py` as a deprecation shim with a message.
Keep `audit/engine.py` as a back-compat shim that wraps the v1.0 engine.

### Consequences
- **-1,181 LOC** net
- **No behavior change** for callers (audit shim preserves CLI + API)
- **Cleaner imports** for new code

---

## ADR-013: Hardened single-host production topology (v2)

### Status
Accepted — implementation is staged behind release gates

### Date
2026-07-12

### Context
The v1 topology mixes read and write capabilities in one web process, permits
development storage fallbacks, and carries run state in process memory. A LAN
deployment still crosses a trust boundary: browsers, reverse proxies, sidecars,
and uploaded content cannot be assumed trusted. Metadata writes must also remain
recoverable if a worker or Calibre process exits partway through an operation.

### Decision
The v2 production profile uses one Compose host behind a TLS reverse proxy with:

1. Mandatory API-key authentication and fail-closed startup/readiness checks.
2. PostgreSQL as the authoritative ledger and outbox, plus Valkey for durable
   queue coordination. SQLite and in-memory queues remain development/test only.
3. A read-only API service and a dedicated writer service. Only the writer may
   mount the Calibre library read-write.
4. Staged activation: the first production gate is read-only with uploads and
   remote LLM calls disabled; write capability is enabled only after its separate
   recovery and isolation gates pass.
5. Explicit immutable operator authorization for any non-auto-eligible verdict.
   A generic `force` flag is not sufficient authorization.
6. Hybrid crash recovery: preserve a fully verified target, automatically restore
   a verified partial write, and require operator action when state is unknown.
7. Restore-point retention of 30 days and a metadata-only scale gate at 50,000
   books. Schema upgrades may use a bounded maintenance window.

Configuration follows `environment > secret file > YAML/init > dotenv`. Secrets
must be injected at runtime and must not have working defaults in version control.
Successful API responses use the stable `{ "status": "success", "data": ... }`
envelope; errors use FastAPI's `{ "detail": ... }` response.

### Alternatives considered

- **Single process with a read-write mount**: simpler, but an API compromise would
  immediately gain write access to the library. Rejected.
- **SQLite and an in-memory queue in production**: low operational overhead, but
  insufficient for durable claims, outbox delivery, and crash reconciliation.
  Retained only for local development and tests.
- **Automatic force override**: convenient for ambiguous verdicts, but destroys
  the audit distinction between policy eligibility and human authorization.
  Rejected.
- **Immediate distributed deployment**: improves host isolation but adds more
  operational failure modes than the initial homelab deployment requires.
  Deferred; the service boundary keeps that migration possible.

### Consequences

- Production cannot start until required database, queue, and authentication
  secrets are present and dependencies are ready.
- Writes require more infrastructure and an explicit authorization record, but
  the API process no longer needs filesystem write capability.
- Migrations and reconciliation become release gates rather than startup-time
  best-effort behavior.
- Optional OCR, vector, upload, and remote-LLM services stay outside the first
  read-only go-live and can be enabled independently later.

---

## Historical ADRs (v0.9 era)

The v0.9 release included the following decisions; they are retained here
for context but are superseded by v1.0:

- **v0.9-ADR-001**: Paperless-ngx bridge — webhook ingestion + audit (still active)
- **v0.9-ADR-002**: Komga/Kavita integration — manga metadata matching (planned v1.1)
- **v0.9-ADR-003**: Vision-based cover verification via Qwen2.5-VL (active, scoped to cover images)
- **v0.9-ADR-004**: Tika sidecar for advanced PDF/DOCX extraction (active)
- **v0.9-ADR-005**: Gotenberg for non-ebook format conversion (active)

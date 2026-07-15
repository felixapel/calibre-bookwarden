# calibre-ai-auditor

A safer, more scalable, **content-ground metadata verification** system for
Calibre libraries. Built around the principle that the book file is the
ground truth and LLMs are witnesses, not generators.

## Status

- **Development head**: Manifestation V2 exact-edition auditor
- **Package version**: 1.2.1; the latest repository tag is `v1.2.0` and Gitea
  currently has no published release
- **Interface**: Supervised local WebUI (React 19 / FastAPI) + CLI
- **Default verification contract**: V2, shadow/read-only, one book at a time
- **V2 writes**: Disabled by default; the development head supports only an
  explicitly enabled, serial, manually authorized pilot of at most five operations
- **Runtime**: Docker/Linux recommended; native verification works with `uv`.
  The supervised writer requires Linux `/proc` descriptor passing and memfd seals.
- **Production profile**: The current development increment is not yet approved
  for a live library. Unattended and legacy direct apply are disabled. Promotion
  requires the Gitea real-service gate, a disposable apply/undo drill, a clone
  rehearsal, and reviewed canaries. See
  [docs/production-readiness.md](docs/production-readiness.md).

Manifestation V2 inspects every format attached to each Calibre book, anchors
identity to checksum-valid edition-bearing content, performs exact-ISBN checks
against Google Books and Open Library, and seals all evidence before review.
OCR, vision, and LLM output can assist recognition but cannot identify a Tier A
book by themselves. See [ADR-002](docs/decisions/ADR-002-exact-manifestation-v2.md).

## Current V2 pipeline

1. Freeze the run's Calibre membership and snapshot the current record.
2. Hash and inspect every EPUB/PDF/CBZ or temporary MOBI/AZW3/CBR conversion.
3. OCR bounded PDF front matter when native edition evidence is absent; cover
   vision is opt-in and always non-authoritative.
4. Query structured providers only by one checksum-valid ISBN candidate.
5. Resolve Tier A (exact), B (incomplete/review), or C (conflict/defer), then
   produce only field values supported by two independent roots.
6. Persist a strict, SHA-256-checksummed evidence package and continue with the next
   book even when one book fails.
7. During an explicitly enabled supervised pilot, queue only one manually
   authorized Tier A package at a time. The writer verifies
   the exact pilot ID/release/schema/root/budget binding, the monotonic operation
   count, live metadata, and every pre-write ebook path/hash. After Calibre
   performs a metadata-driven directory move, the writer accepts new paths only
   when the unique live format/SHA-256 multiset still matches the seal; all
   other metadata remains under full readback. It then creates OPF/custom-column/
   cover rollback artifacts. Library and artifact paths are opened component by
   component beneath sealed roots; OPF and cover bytes are handed to Calibre
   through immutable descriptors rather than re-opened pathnames.

## Historical v1.0 engine

v1.0 replaces the v0.9 evidence-first pipeline with a **content-ground
verification engine** that adjudicates each declared metadata field against
the actual book content.

| Component | v0.9 | v1.0 |
|---|---|---|
| Decision unit | Single `MetadataResolution` aggregate | **Per-field `FieldVerdict`** with cited `EvidenceSpan`s |
| Adjudication | One LLM call decides everything | **8 deterministic rules** per field; **LLM witness** called only for ambiguous cases |
| Auto-apply | Manual review queue | **Conservative auto-apply gate** (≥80% confidence, no high-risk flags, per-book restore point) |
| Undo | OPF backup only | **RestorePointStore**: OPF + cover + secure file copy + JSON snapshot, 30-day retention target |
| Resume | None | **PostgreSQL ledger/outbox** with single-writer crash reconciliation |
| OCR | None | **OCR routing** with Tesseract and optional PaddleOCR |
| Inference | Single Ollama | **Multi-host discovery** (3090 + 5060 Ti + 1660 SUPER + remote) |
| Observability | Logs | **Prometheus `/metrics`** with counters, gauges, histograms |
| Scale tested | Hundreds of books | Resolver and metadata microbenchmarks up to 50k synthetic records; full-library throughput is unproven |

## Historical v1.0 design

1.  **The book is the ground truth.** Extract ISBNs, titles, authors, dates,
    publishers directly from EPUB/PDF content via deterministic rules before
    anything else.
2.  **Eight deterministic rules per field.** Title (fuzzy + edition-tag aware),
    authors (set + transliteration), ISBN-13 (checksum), publisher (variant-tolerant),
    date (year tolerance), language (ISO), series, series_index.
3.  **LLMs are witnesses, not dictators.** The LLM is invoked only when a field
    comes back `ambiguous` from the deterministic rules — never as the primary
    source of truth.
4.  **Conservative auto-apply.** A field is auto-applied only when (a) every
    declared field has a deterministic verdict, (b) overall confidence ≥80,
    (c) no high-risk flag is present, (d) per-field confidence ≥75.
5.  **Every apply creates a restore point.** Per-book snapshot of OPF, cover,
    secure file copy, and JSON metadata diff. 30-day retention target; bulk undo is queued by run ID through the API.
6.  **Resumable on crash.** A durable PostgreSQL operation ledger/outbox and
    per-book locks reconcile interrupted external writes before new work begins.

## Key Features

- **Manifestation V2 review** — Review shows exact sealed evidence, all format
  hashes, source provenance, current-versus-proposed values, tier, authorization,
  and the state of one queued operation. Tier B/C cannot be queued.
- **Supervised serial apply** with a persisted max-five budget, exact image,
  schema and library binding, writer-side budget reconciliation, append-only
  acknowledgement of reviewed safe failures, and per-book restore points — see
  [docs/SAFETY.md](docs/SAFETY.md).
- **Multi-host LLM routing** — auto-discovers 3090 (high), Unraid Ollama
  (medium), and remote providers. Tasks route by GPU class.
- **OCR routing** — Tesseract is the baseline; the `[ocr]` optional extra adds
  PaddleOCR. Surya is not packaged by this project.
- **WebUI Verify page** — start a verify run from the browser, watch live
  progress, drill into per-book verdicts.
- **Privacy by default** — `allow_remote_text: false` and `allow_remote_images:
  false` block sending snippets to cloud LLMs unless explicitly enabled.
- **Read-Only by Default** — the application never modifies your library unless
  you explicitly opt in.

## Quick Start (Docker)

```bash
git clone http://192.168.0.122:3010/felix/calibre-ai-auditor.git
cd calibre-ai-auditor
cp .env.example .env
# Replace placeholders, use distinct secrets and an immutable image digest.
chmod 600 .env
./scripts/prepare-production.sh
docker compose up -d --wait postgres valkey
docker compose --profile maintenance run --rm migrate
docker compose up -d writer app
```

WebUI at <http://localhost:8080>. To add optional PaddleOCR support:

```bash
pip install -e .[ocr]
```

## CLI Quick Start

```bash
source .venv/bin/activate

# Sanity checks
bookaudit doctor          # verify calibredb, Tika, Qdrant, etc.
bookaudit hosts           # discover homelab inference hosts

# Manifestation V2 (default): shadow audit, one book at a time
bookaudit verify --pipeline v2 --limit 100 --use-ocr
bookaudit verify --pipeline v2 --limit 100 --use-ocr --use-llm
bookaudit verify --pipeline v2 --limit 0 --format json > out.json

# Emergency close of one persisted supervised pilot (stop app/writer first)
bookaudit pilot-stop pilot-YYYYMMDD --yes

# Only after stopping its pilot and reconciling a terminal failed operation
bookaudit incident-ack OPERATION_ID --actor OPERATOR \
  --reason "Verified pre-write failure and unchanged Calibre metadata" --yes

# Legacy v0.9 commands (still work as fallback)
bookaudit inspect --path "/path/to/book.epub"
bookaudit scan --limit 10
bookaudit audit --run latest
```

## Documentation

### Quick reference
- **[ROADMAP.md](ROADMAP.md)** — v0.1 through v1.2 milestones
- **[USAGE.md](USAGE.md)** — Step-by-step WebUI and CLI workflows
- **[CLI_REFERENCE.md](CLI_REFERENCE.md)** — All commands and flags

### Architecture & design
- **[ARCHITECTURE.md](ARCHITECTURE.md)** — Component overview, data flow, design principles
- **[docs/architecture/target-advanced-architecture.md](docs/architecture/target-advanced-architecture.md)** — Detailed v1.0 component breakdown
- **[docs/architecture/integration-decisions.md](docs/architecture/integration-decisions.md)** — Architecture Decision Records
- **[docs/decisions/ADR-002-exact-manifestation-v2.md](docs/decisions/ADR-002-exact-manifestation-v2.md)** — Exact-edition V2 trust and write contract
- **[docs/decisions/ADR-003-supervised-local-auditor-scope.md](docs/decisions/ADR-003-supervised-local-auditor-scope.md)** — Product scope and explicit non-goals
- **[docs/architecture/v1_scope_decisions.md](docs/architecture/v1_scope_decisions.md)** — What's in / out of v1.0
- **[docs/research/PEER_PROJECTS.md](docs/research/PEER_PROJECTS.md)** — Comparison vs `paperless-gpt`, `book-memex`, etc.

### Operations
- **[INSTALL.md](INSTALL.md)** — Native + Docker install paths
- **[DEPLOYMENT.md](DEPLOYMENT.md)** — Production deploy guide
- **[docs/HOMELAB.md](docs/HOMELAB.md)** — Homelab-specific config
- **[docs/DATABASE.md](docs/DATABASE.md)** — SQLite / PostgreSQL setup
- **[docs/API.md](docs/API.md)** — REST endpoints reference
- **[docs/SAFETY.md](docs/SAFETY.md)** — Read-only mode + restore points
- **[docs/production-readiness.md](docs/production-readiness.md)** — Current
  production gate evidence, trust boundaries, and operator prerequisites

### Quality & calibration
- **[TESTING.md](TESTING.md)** — Unit / integration / E2E / benchmark strategy
- **[tests/benchmarks/BASELINE.md](tests/benchmarks/BASELINE.md)** — Performance baseline
- **[docs/calibration/v1.0_calibration_runbook.md](docs/calibration/v1.0_calibration_runbook.md)** — Real-world calibration on Unraid
- **[docs/calibration/manifestation-v2-runbook.md](docs/calibration/manifestation-v2-runbook.md)** — V2 gold-corpus calibration and supervised rollout

## Project Principles

- **The book is the ground truth.** Content extraction always runs first;
  LLMs only adjudicate, never replace.
- **Read-Only by Default.** The application never modifies your library unless
  you explicitly opt in.
- **Deterministic before generative.** Rules run before optional model evidence;
  their real coverage and precision must be measured on a reviewed corpus.
- **Privacy-Centric.** Remote LLMs receive minimal context, capped by
  strict token limits and privacy filters.
- **Fail-safe, not fail-fast.** A failed LLM call is a `needs_review`, not
  a crash. A failed OCR is a fallback path, not an error.

## Testing & Benchmarks

The local verification script runs locked formatting, lint, typing,
hermetic backend/integration tests and a CLI smoke check. Gitea CI adds
real PostgreSQL concurrency/ACL tests, the required no-skip Calibre/Tesseract/
PostgreSQL/Valkey V2 apply-readback-undo round trip, the 50k metadata gate,
dependency audits, desktop/mobile browser tests, image scanning and the Compose
contract.

### Backend (pytest)

```bash
# Deterministic local gate (live-service tests remain in Gitea)
./scripts/verify-calibre-gate.sh

# Full benchmark suite (~45s)
pytest --benchmark-only tests/benchmarks/

# OCR comparison (requires real OCR deps)
pip install -e .[ocr]
pytest -m ocr_live tests/benchmarks/test_bench_ocr_comparison.py

# With coverage
pytest --cov=src --cov-report=html
```

Baseline numbers are tracked in [tests/benchmarks/BASELINE.md](tests/benchmarks/BASELINE.md).

### WebUI E2E (Playwright)

```bash
# From the repository root, prepare the backend used by Playwright
uv sync --python 3.12.13 --frozen --extra dev

cd webui
npm ci
npm run build

# Install Playwright browser (one-time)
npx playwright install --with-deps chromium

# Run all E2E tests
npm run e2e

# Run with UI inspector
npm run e2e:ui

# Run specific spec
npx playwright test review.spec.ts
```

The E2E suite covers every page, authentication recovery, accessibility,
responsive navigation and browser performance budgets.

### CI integration

- **Gitea Actions** is the canonical development pipeline.
  `.gitea/workflows/v1-tests.yml` runs backend/PostgreSQL, benchmarks, browser,
  and production-image gates on the self-hosted runner.
- Files under `.github/workflows/` remain mirror/release compatibility assets;
  normal development and review use the Gitea remote and Gitea Actions.

## License

GPL-3.0-or-later. See [LICENSE.md](LICENSE.md).

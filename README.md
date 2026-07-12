# calibre-ai-auditor

A safer, more scalable, **content-ground metadata verification** system for
Calibre libraries. Built around the principle that the book file is the
ground truth and LLMs are witnesses, not generators.

## Status

- **Version**: v1.2.0 (Content-Ground Verification + Comics Vision + MCP Server)
- **Interface**: Full-stack WebUI (React 19 / FastAPI) + CLI
- **Default Mode**: Read-Only
- **Runtime**: Docker (Linux/macOS recommended); native works with `uv`
- **Production profile**: Internally approved for supervised and unattended
  operation; complete the operator credential checklist before exposure. See
  [docs/production-readiness.md](docs/production-readiness.md).

## What's new in v1.0

v1.0 replaces the v0.9 evidence-first pipeline with a **content-ground
verification engine** that adjudicates each declared metadata field against
the actual book content.

| Component | v0.9 | v1.0 |
|---|---|---|
| Decision unit | Single `MetadataResolution` aggregate | **Per-field `FieldVerdict`** with cited `EvidenceSpan`s |
| Adjudication | One LLM call decides everything | **8 deterministic rules** per field; **LLM witness** called only for ambiguous cases |
| Auto-apply | Manual review queue | **Conservative auto-apply gate** (≥80% confidence, no high-risk flags, per-book restore point) |
| Undo | OPF backup only | **RestorePointStore**: OPF + cover + hardlinked file + JSON snapshot, 30-day retention target |
| Resume | None | **PostgreSQL ledger/outbox** with single-writer crash reconciliation |
| OCR | None | **Multi-provider OCR router** (Tesseract / PaddleOCR / Surya) by page hint |
| Inference | Single Ollama | **Multi-host discovery** (3090 + 5060 Ti + 1660 SUPER + remote) |
| Observability | Logs | **Prometheus `/metrics`** with counters, gauges, histograms |
| Scale tested | Hundreds of books | **10k–50k books** per run (linear scaling) |

## Core Philosophy

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
    file hardlink, and JSON metadata diff. 30-day retention target; bulk undo is queued by run ID through the API.
6.  **Resumable on crash.** A durable PostgreSQL operation ledger/outbox and
    per-book locks reconcile interrupted external writes before new work begins.

## Key Features

- **Per-field verdict rendering** — Review page shows colored chips for
  `confirmed` / `mismatch` / `missing` / `ambiguous` per field, with cited
  evidence spans.
- **Conservative auto-apply** with per-book restore points — see
  [docs/SAFETY.md](docs/SAFETY.md).
- **Multi-host LLM routing** — auto-discovers 3090 (high), Unraid Ollama
  (medium), and remote providers. Tasks route by GPU class.
- **Multi-tier OCR** — Tesseract always available; PaddleOCR + Surya behind
  the `[ocr]` optional extra.
- **WebUI Verify page** — start a verify run from the browser, watch live
  progress, drill into per-book verdicts.
- **Privacy by default** — `allow_remote_text: false` and `allow_remote_images:
  false` block sending snippets to cloud LLMs unless explicitly enabled.
- **Read-Only by Default** — the application never modifies your library unless
  you explicitly opt in.

## Quick Start (Docker)

```bash
git clone git@github.com:felixapel/calibre-ai-auditor.git
cd calibre-ai-auditor
cp .env.example .env
# Replace placeholders, use distinct secrets and an immutable image digest.
chmod 600 .env
./scripts/prepare-production.sh
docker compose up -d --wait postgres valkey
docker compose --profile maintenance run --rm migrate
docker compose up -d writer app
```

WebUI at <http://localhost:8080>. For full OCR providers:

```bash
pip install -e .[ocr]
```

## CLI Quick Start

```bash
source .venv/bin/activate

# Sanity checks
bookaudit doctor          # verify calibredb, Tika, Qdrant, etc.
bookaudit hosts           # discover homelab inference hosts

# v1.0: content-ground verification
bookaudit verify --limit 100                       # 100 books
bookaudit verify --limit 100 --use-llm            # with LLM witness
bookaudit verify --limit 0 --format json > out.json  # full library

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

## Project Principles

- **The book is the ground truth.** Content extraction always runs first;
  LLMs only adjudicate, never replace.
- **Read-Only by Default.** The application never modifies your library unless
  you explicitly opt in.
- **Deterministic before generative.** 8 deterministic rules cover ~95% of
  fields correctly. LLMs handle the remaining ~5% ambiguities.
- **Privacy-Centric.** Remote LLMs receive minimal context, capped by
  strict token limits and privacy filters.
- **Fail-safe, not fail-fast.** A failed LLM call is a `needs_review`, not
  a crash. A failed OCR is a fallback path, not an error.

## Testing & Benchmarks

The fail-closed verification script runs locked formatting, lint, typing,
backend/integration tests, Komf integration and CLI smoke checks. CI adds real
PostgreSQL concurrency/ACL tests, the 50k metadata gate, dependency audits,
desktop/mobile browser tests, image scanning and the Compose contract.

### Backend (pytest)

```bash
# Fast unit + integration tests (~5s, no benchmarks)
pytest -m "not benchmark and not ocr_live and not network"

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
cd webui

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

- **GitHub Actions**: `.github/workflows/ci.yml` plus signed release publication in `.github/workflows/release.yml`.
- **Gitea Actions**: `.gitea/workflows/v1-tests.yml` runs backend/PostgreSQL,
  benchmarks, browser and production-image gates on the self-hosted runner.
  GitHub is the canonical production release authority; Gitea never publishes
  or signs release images.

## License

GPL-3.0-or-later. See [LICENSE.md](LICENSE.md).

# calibre-ai-auditor

A safer, more scalable, evidence-first metadata review system for Calibre libraries, inspired by Paperless-ngx and modern LLM orchestration patterns.

## Status

- **Version**: v0.1 (Current)
- **Interface**: Full-stack WebUI (React/FastAPI) + CLI
- **Default Mode**: Read-Only
- **Runtime**: Docker (Linux/macOS recommended)

## Core Philosophy

`calibre-ai-auditor` evolves the concept of metadata management from simple "guessing" to a formal **Evidence-First** workflow. The system identifies matches and conflicts using deterministic extraction before leveraging AI for complex reasoning.

1.  **Deterministic Extraction First**: Extract ISBNs, bylines, and snippets directly from book files (EPUB/PDF) using fast parsers and Apache Tika.
2.  **External Verification**: Fetch candidates from OpenLibrary, Google Books, and Calibre providers.
3.  **Model Reasoning Second**: Use local (Ollama) or remote (OpenAI) LLMs to evaluate the evidence package and judge candidates.
4.  **Human Review Third**: Approve or reject changes through a structured WebUI review queue.
5.  **Safe Write Last**: Apply fixes only after an automated OPF backup, with full undo capability.

## Key Features

- **Evidence Ladder**: Visualize why a metadata suggestion was made, with clear source attribution.
- **Smart Privacy**: Strict controls on what text or images are sent to remote providers (`allow_remote_text: false` by default).
- **Sidecar Power**: Seamless integration with **Apache Tika** for document extraction, **Gotenberg** for PDF reports, and **Qdrant** for semantic duplicates.
- **Homelab Ready**: Dual-database support (PostgreSQL/SQLite) and Valkey-backed job queues.
- **Calibre CLI Native**: Uses standard `calibredb` tools for library operations, ensuring full compatibility.

## Quick Start (Docker)

The easiest way to deploy the full stack is via Docker Compose:

```bash
docker compose up -d
```

Access the WebUI at [http://localhost:8080](http://localhost:8080).

## CLI Quick Start

For standalone file inspection or library management:

```bash
source .venv/bin/activate
# Check dependencies
bookaudit doctor
# Inspect a single file
bookaudit inspect --path "/path/to/book.epub"
# Scan your library
bookaudit scan --limit 10
# Run the audit engine
bookaudit audit --run latest
```

## Documentation

- **[SAFETY.md](docs/SAFETY.md)** — Critical info on read-only mode and backups.
- **[ARCHITECTURE.md](docs/ARCHITECTURE.md)** — System design and open-source inspirations.
- **[API.md](docs/API.md)** — REST API endpoint reference.
- **[DATABASE.md](docs/DATABASE.md)** — SQLite and PostgreSQL configuration.
- **[USAGE.md](USAGE.md)** — Common workflows and tutorials.
- **[ROADMAP.md](ROADMAP.md)** — Upcoming features and milestones.
- **[MANGA_COMICS_MODE.md](docs/MANGA_COMICS_MODE.md)** — Design for future manga/comics support.

## Project Principles

- **Evidence First**: Every metadata change must be tied to collected evidence snippets.
- **Read-Only by Default**: The application will never modify your library unless explicitly configured.
- **Deterministic Truth**: The book file itself is the primary source of truth; LLMs are evaluators, not dictators.
- **Privacy-Centric**: Remote LLMs receive minimal context, capped by strict token limits and privacy filters.

## Testing & Benchmarks

### Backend (pytest)

```bash
# Fast unit + integration tests (~5s, no benchmarks)
pytest -m "not benchmark and not ocr_live and not network"

# Full benchmark suite (~45s)
pytest --benchmark-only tests/benchmarks/

# OCR comparison (requires real OCR deps)
pytest -m "ocr_live" tests/benchmarks/test_bench_ocr_comparison.py

# With coverage
pytest --cov=src --cov-report=html
```

Baseline numbers are tracked in [tests/benchmarks/BASELINE.md](tests/benchmarks/BASELINE.md).

### WebUI E2E (Playwright)

```bash
cd webui

# Install Playwright browser (one-time)
pnpm exec playwright install --with-deps chromium

# Run all E2E tests
pnpm e2e

# Run with UI inspector
pnpm e2e:ui

# Run specific spec
pnpm exec playwright test review.spec.ts
```

32 E2E tests across 7 spec files cover every page and the v1.0 verdict rendering.

### CI integration

- **GitHub Actions**: `.github/workflows/v1-tests.yml` (4 jobs: backend, benchmarks, webui-lint-build, webui-e2e)
- **Gitea Actions**: `.gitea/workflows/v1-tests.yml` (same jobs, Gitea syntax, self-hosted on Unraid)

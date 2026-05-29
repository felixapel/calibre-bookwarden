# Roadmap

## Delivery Strategy

Evolve from a read-only metadata auditor into a full-stack library management system with robust safety controls and multi-provider intelligence.

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
| `v1.0` | **Stable**: Stable API/CLI Contract + Desktop App Mode | [PLANNED] |

---

## Completed Milestones

### ✅ Architecture Modernization
- Implemented **Evidence-First** resolution engine.
- Removed LiteLLM dependency for direct **OpenAI/Ollama** providers.
- Integrated **Apache Tika** for robust PDF/Doc extraction.
- Added **PostgreSQL** and **SQLite** dual-storage support.
- Added **Valkey** for rate-limit caching and distributed locks.

### ✅ Modern WebUI
- **Dashboard**: High-level health and metrics.
- **Scan & Audit**: Direct control of the extraction pipeline.
- **Review Queue**: Evidence ladder visualization and approval workflow.
- *   **Settings**: Privacy controls and provider registry.

### ✅ Safe Applied Writes
- Automated **OPF Backups** before any `calibredb` modification.
- Multi-field patch logic (`identifiers`, `tags`, etc.).
- Robust **Undo** system using exported OPF files.

### ✅ Ecosystem Bridges (v0.9)
- **Paperless-ngx**: Link audits to scanned document IDs.
- **Komga/Kavita**: First-class support for Manga/Comics mode.

---

## Upcoming Milestones (v1.0)

- Stable API/CLI contract and versioning policy.
- Desktop / single-container “app mode” packaging.
- Richer review-packet PDF templates with cover thumbnails.

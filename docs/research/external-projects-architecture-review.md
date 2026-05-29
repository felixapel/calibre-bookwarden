# External Projects Architecture Review

This document contains a deep technical audit and architectural review of mature open-source projects relevant to the development of `calibre-ai-auditor`.

---

## Project: Calibre

### Repo
`https://github.com/kovidgoyal/calibre`

### What this project does
Calibre is the de facto standard open-source library manager, format converter, and metadata manager for ebooks. It provides a massive Python-based GUI, a REST server, and a rich set of command-line tools.

### Files/directories inspected
- `src/calibre/db/cache.py` — In-memory cached database API (`new_api`).
- `src/calibre/ebooks/metadata/epub.py` — Native metadata parser for EPUB packages.
- `src/calibre/customize/builtins.py` — Provider plugin registrations.
- `src/calibre/library/db.py` — Database connectivity and locking logic.

### Relevant architecture patterns
- **Memory-Mapped Relational Cache**: Loads SQLite structures into dicts for fast read performance.
- **Portability-Minded File Sanitization**: Force-translates accents, limits folder/file length to prevent filesystem overflows.

### Useful ideas for our app
- **Calibre direct DB integration**: Utilizing `calibre-debug` with `calibre.library.db` directly to bypass subprocess execution shell latencies for scans.
- **Atomic Writing**: Applying patches to temp files, verifying integrity, and using atomic swaps (`os.replace`) to write metadata back without corrupting user books.

### What not to copy
- **Direct metadata.db SQL writes**: Direct SQL writes bypass Calibre's in-memory cache, causing corruption or sync issues when the desktop application is running. Always use the command line wrapper `calibredb` or the official DB API.

### Integration recommendation
**Adopt (Subprocess wrappers for read-only; DB API for heavy scans; subprocess for write-back)**

### Priority
**P0**

---

## Project: Calibre-Web-Automated

### Repo
`https://github.com/crocodilestick/Calibre-Web-Automated`

### What this project does
An automated wrapper around Calibre-Web, extending it with ingestion watchers, automated format conversions, and background sync engines.

### Files/directories inspected
- `docker-compose.yml` — Container configurations.
- `scripts/` — Automated ingestion and library refresh scripts.

### Relevant architecture patterns
- **Decoupled Folder Ingestion**: Uses a directory watcher on an incoming "inbox" folder and triggers import jobs into Calibre.
- **Automated Book Conversions**: Automatically runs `ebook-convert` for raw ingested formats (e.g. PDF/TXT to EPUB).

### Useful ideas for our app
- **Consume Directory Pipelines**: Running a daemon that watches `inbox/` for new EPUBs/PDFs, matches them against Qdrant similarity, and files them into the library automatically.

### What not to copy
- **Tight container coupling**: The Docker container embeds Calibre, Python, node, and custom web layers in a monolithic configuration, making container maintenance difficult. We should keep our app services focused and lightweight.

### Integration recommendation
**Adapt (Adopt their decoupled folder ingestion pipeline model)**

### Priority
**P1**

---

## Project: Gotenberg

### Repo
`https://github.com/gotenberg/gotenberg`

### What this project does
Gotenberg is a stateless developer-friendly API for converting multiple file formats (HTML, DOCX, XLSX, TXT) into PDF documents using LibreOffice and Chromium engines.

### Files/directories inspected
- `pkg/modules/libreoffice/` — Handles office documents parsing.
- `pkg/modules/api/` — Web layer exposing conversion routes.

### Relevant architecture patterns
- **Stateless Microservice Model**: The service maintains no state; it accepts files via HTTP and responds with output.
- **Strict Process Isolations**: LibreOffice runs in a sandbox with custom resource timeouts to prevent memory leaks from crashing the host container.

### Useful ideas for our app
- **Office Document Normalization**: We can utilize a Gotenberg container in our docker-compose stack to convert raw formats (`DOCX`, `ODT`, `HTML`) into search-ready PDFs or plain text.

### What not to copy
- **Stateless design inside CLI**: While stateless is great for a microservice, our CLI is stateful. We should expose Gotenberg as an *optional* remote service rather than building LibreOffice dependencies directly into our main image.

### Integration recommendation
**Revisit later (Use as an optional microservice in production deployment for non-ebook formats)**

### Priority
**P2**

---

## Project: Paperless-ngx

### Repo
`https://github.com/paperless-ngx/paperless-ngx`

### What this project does
A document management system that scans physical documents, performs OCR, extracts text, and organizes everything using tags, correspondents, and fields.

### Files/directories inspected
- `src/documents/consumer.py` — Main directory watcher and document processing pipeline.
- `src/documents/parsers.py` — Format-specific parser routes.
- `src/documents/tasks.py` — Background workers executing long-running OCR tasks.

### Relevant architecture patterns
- **Document Lifecycle Engine**: Standardized ingestion workflow (`Consume -> Parse -> OCR -> Index -> File`).
- **Original vs Archive Storage**: Keeps a copy of the exact unmodified document alongside the processed OCR-ready version.

### Useful ideas for our app
- **Ingestion Lifecycle**: When a user adds an ebook, keep the original file in a safe backup bucket, and write the metadata-audited file as the public release.
- **Progressive OCR Logging**: Emitting clean task statuses and logs to the cache queue so frontends can display percentage bars for slow scans.

### What not to copy
- **Celery/Redis Overhead**: Celery adds a massive dependency footprint. We should continue to use our lightweight async queue or standard Redis tasks for lower deployment complexity.

### Integration recommendation
**Adapt (Adopt their document lifecycle and progress-reporting patterns)**

### Priority
**P1**

---

## Project: Qdrant

### Repo
`https://github.com/qdrant/qdrant`

### What this project does
Qdrant is a high-performance, developer-friendly vector similarity database and search engine, providing APIs to index and search high-dimensional vectors.

### Files/directories inspected
- `lib/collection/` — Payload storage and indexing logic.
- `lib/segment/` — Vector search implementation.

### Relevant architecture patterns
- **Hybrid Similarity Filtering**: Combines vector cosine similarity search with relational SQL-like metadata filtering.
- **Payload Metadata Storage**: Stores titles, authors, and unique IDs directly inside the vector record to eliminate relational DB join latency.

### Useful ideas for our app
- **Hierarchical Book Embeddings**: Instead of embedding the full text, we embed:
  1. Title + Author vector (for semantic duplicate search).
  2. Summary/Description vector (for content-based suggestions).
- **Payloaded Filtering**: Storing `calibre_id`, `isbn`, and `language` in payloads to query duplicates restricted by language.

### What not to copy
- **Exclusive Vector Search**: Never use vector similarity as the sole source of duplicate verification. Use it to *propose* duplicates, but verify with deterministic rules (ISBN, title fuzzy matches).

### Integration recommendation
**Adopt (Keep as an optional core vector engine for duplicate search)**

### Priority
**P0**

---

## Project: Ollama

### Repo
`https://github.com/ollama/ollama`

### What this project does
Ollama is a lightweight package that bundles LLM weights, runners, and setups into a single engine, exposing an OpenAI-compatible REST API.

### Files/directories inspected
- `server/` — Core HTTP API server.
- `app/` — Runner bindings.

### Relevant architecture patterns
- **OpenAI Compatibility Layer**: Exposing standard `/v1/chat/completions` API endpoints to make local models drop-in replacements for cloud endpoints.
- **Dynamic Model Swapping**: Automatically loads weights into GPU/CPU RAM and unloads them after an idle period.

### Useful ideas for our app
- **Dynamic Task Mapping**: Mapping local models based on speed/precision (e.g. `nomic-embed-text` for vectors, `qwen3:8b` for utility tasks like normalization).
- **Graceful Failbacks**: If Ollama crashes or runs out of VRAM, gracefully fall back to an API provider (OpenAI/Gemini).

### What not to copy
- **Hard GPU Assumptions**: Never assume the host system has a CUDA-compatible GPU. Our app must run cleanly in CPU-only containers (using smaller quantized models).

### Integration recommendation
**Adopt (Expose Ollama as the default local LLM routing layer)**

### Priority
**P0**

---

## Project: Valkey

### Repo
`https://github.com/valkey-io/valkey`

### What this project does
Valkey is a high-performance key-value data store, fork of Redis, designed to provide caching, message brokers, and transactional storage.

### Files/directories inspected
- `src/t_stream.c` — Message stream logic.
- `src/db.c` — Core caching and key eviction routines.

### Relevant architecture patterns
- **Memory Caching**: Ultra-fast key-value store with time-to-live (TTL) cache eviction.
- **Distributed Locks**: Utilizing basic atomic lock keys to coordinate parallel workers.

### Useful ideas for our app
- **Provider API Caching**: Cache all metadata responses from Google Books and OpenLibrary for 24 hours to prevent API rate-limiting.
- **Worker Lock Keys**: Set a `lock:book:<id>` key before running a metadata audit to prevent concurrent workers from processing the same book twice.

### What not to copy
- **Hard Dependency**: Do not crash if Valkey is down. Our app must fall back to a RAM cache dictionary when running locally without Docker.

### Integration recommendation
**Adopt (Use as the primary cache/broker backend for production deployment)**

### Priority
**P1**

---

## Project: Apache Tika

### Repo
`https://github.com/apache/tika`

### What this project does
A content detection and extraction framework, parsing metadata and text from thousands of different file types (DOCX, PPTX, PDF, EPUB).

### Files/directories inspected
- `tika-core/` — Parser registry and MIME detection.
- `tika-server/` — REST API wrapper.

### Relevant architecture patterns
- **MIME-Type Driven Parser Selection**: Identifies files using magic bytes, then selects the matching parser class.
- **Unified Metadata Schema**: Translates format-specific keys into Dublin Core metadata elements.

### Useful ideas for our app
- **Multi-Format Text Ingestion**: Employs Tika Server (`POST /tika`) to parse non-ebook formats (`DOCX`, `TXT`, `HTML`) without needing dozens of Python parsing libraries.

### What not to copy
- **Tika App CLI (JVM Overhead)**: Running the Java-based Tika command-line jar has severe startup latency (~1-2 seconds per file). Only use Tika via its stateless HTTP server (`tika-server`) container.

### Integration recommendation
**Revisit later (Integrate as an optional parsing service for raw formats)**

### Priority
**P2**

---

# Specific Comparison Matrix

| Area | Our Current Implementation | Best Reference Project | What the Reference Does Better | What We Should Adopt | Risk / Effort | Priority |
| :--- | :--- | :--- | :--- | :--- | :---: | :---: |
| **Calibre Access** | Subprocess CLI (`calibredb`) | Calibre Core (`calibre.library`) | Direct library cache imports; no execution latency. | Optional Python API binding fallback (`calibre-debug`). | Medium / Med | P1 |
| **Conversions** | Handled manually | Gotenberg | Clean sandboxed LibreOffice conversion pipeline. | Gotenberg service for DOCX conversions. | Low / Low | P2 |
| **Ingestion** | Watchdog observer | Paperless-ngx | Document original copy backups and status tracking. | "Consume folder" backup-then-process model. | Low / Low | P1 |
| **Vector Search** | Title cosine similarity | Qdrant | Hybrid search filters combined with metadata payload. | Qdrant metadata payload filters. | Low / Low | P0 |
| **LLM Caching** | Local process cache | Valkey | Shared Redis-compatible key-value cache with TTLs. | Valkey cache for provider requests. | Low / Low | P1 |

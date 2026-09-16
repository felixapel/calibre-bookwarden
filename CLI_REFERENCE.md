# CLI Reference: Calibre Bookwarden

`bookwarden` (aliased as `bookaudit` for backward compatibility) is the primary command-line tool for managing **Calibre Bookwarden**. All commands can be executed:
- Directly via `uv`: `uv run bookwarden <command> [options]`
- Ephemerally via `uvx`: `uvx --from git+https://github.com/felixapel/calibre-bookwarden.git bookwarden <command> [options]`
- Inside Docker containers: `docker compose exec app bookwarden <command> [options]`

> [!NOTE]
> **Transparent Alias Compatibility:** `bookaudit` is maintained across all subcommands, arguments, and return codes as an exact 1:1 alias for `bookwarden`.

---

## Global Options

| Option | Short | Type | Description |
|---|---|---|---|
| `--config` | `-c` | `PATH` | Path to custom `config.yml` (overrides default `config/config.yml`). |

---

## 1. 360° Forensic & Library Curation Suite (v1.3.0)

The direct SQLite engine is read-only. Performance depends on the library and host; no fixed throughput or memory guarantee is established.

### `bookwarden audit-360`
Performs a deep forensic inspection of the Calibre SQLite database, disk format parity, covers, and author sort consistency without modifying any data.

```bash
bookwarden audit-360 [--library PATH] [--json]
```

| Flag | Short | Default | Description |
|---|---|---|---|
| `--library` | `-l` | `config` | Path to the Calibre library directory (contains `metadata.db`). |
| `--json` | | `False` | Output full audit metrics as a structured JSON object. |

**Audit Checks Performed:**
- SQLite schema integrity (`PRAGMA integrity_check`).
- Foreign key constraints across `books_authors_link`, `books_tags_link`, and `data`.
- Physical vs. database parity (identifying `missing_data_files` on disk and `empty_format_records`).
- Cover diagnostics: broken covers, tiny covers (<200px), and decompression bomb risks (>30MP or >10MB).
- Author sort desynchronizations (e.g. nobility particles, patronymics).
- Scraper junk residue in titles (e.g. `[welib.org]`, `_print`, `.epub`).

---

### Retired direct mutation commands

`optimize-covers`, `sync-library`, `curate-periodicals` and `full-audit-run` remain recognized for compatibility but fail before touching the library, even when read-only configuration is disabled. Their former flags do not restore write access. Use read-only inspection and the existing, separately gated Manifestation V2 supervised workflow; there is no unattended maintenance replacement.

The experimental `CryptographicLedger` is not the implementation of `bookwarden undo`. Do not use its prototype change IDs with the CLI. Production direct CLI undo remains disabled.

---

## 2. Manifestation V2 Verification & Audit

### `bookwarden verify`
Runs an exact-manifestation audit across a Calibre library. V2 freezes the run membership, processes one book at a time, inspects all attached formats, and persists a sealed Tier A/B/C evidence package.

```bash
bookwarden verify [--pipeline v2|v1] [--limit N] [--library PATH]
                  [--use-ocr|--no-ocr] [--use-vision|--no-vision] [--use-llm|--no-llm]
                  [--allow-remote-text|--deny-remote-text]
                  [--allow-remote-images|--deny-remote-images]
                  [--run-id ID] [--format text|json]
```

| Flag | Default | Description |
|---|---|---|
| `--limit` | `50` | Maximum number of books to verify. Set to `0` for the entire library. |
| `--library` | `config` | Path to Calibre library directory. |
| `--pipeline` | `v2` | `v2` exact-manifestation contract or legacy `v1`. |
| `--use-ocr` / `--no-ocr` | `True` | OCR bounded PDF front matter when native identity evidence is absent. |
| `--use-vision` / `--no-vision` | `False` | Analyze bounded covers as non-authoritative evidence. |
| `--use-llm` / `--no-llm` | `False` | Transcribe bounded evidence with the configured model (LLMs never promote to Tier A). |
| `--allow-remote-text` / `--deny-remote-text` | `False` | Per-run consent for bounded text egress to remote models. Body text is never sent. |
| `--allow-remote-images` / `--deny-remote-images`| `False` | Per-run consent for cover image egress to remote vision models. |
| `--run-id` | generated | Resume an existing V2 run ID and frozen membership. Terminal books are skipped. |
| `--format` | `text` | `text` for human-readable per-book output; `json` for parseable output. |

---

### `bookwarden inventory`
Uses an SSH tunnel and an independently verified read-only Calibre account to inspect a live remote library, emitting aggregate counts without saving book records.

```bash
bookwarden inventory --content-server URL --library-id ID --username USER --source-identity FINGERPRINT [--output PATH] [--password-stdin]
```

| Option | Default | Description |
|---|---|---|
| `--content-server` | *(Required)* | Loopback URL for an SSH-tunneled Calibre Content Server. |
| `--library-id` | *(Required)* | Exact Content Server library identifier. |
| `--username` | *(Required)* | Read-only Content Server username. |
| `--source-identity` | *(Required)* | Stable fingerprint (e.g. `SHA256:...`) derived from SSH host key. |
| `--output` | `None` | Optional path to write JSON inventory summary. |
| `--password-stdin` | `False` | Read password from standard input rather than interactive prompt. |

---

### `bookwarden verify-content-server`
Performs remote verification against a live Calibre Content Server via SSH tunnel, downloading formats to a temporary scratch directory and recording sealed V2 evidence.

```bash
bookwarden verify-content-server --content-server URL --library-id ID --username USER --source-identity FINGERPRINT --scratch-root PATH [--limit N] [--use-ocr] [--allow-public-providers] [--run-id ID]
```

---

### `bookwarden calibrate-v2`
Generates an integrity-checksummed, advisory calibration report from human-reviewed V2 package labels.

```bash
bookwarden calibrate-v2 --corpus PATH --output PATH [--valid-days N]
```

| Flag | Default | Description |
|---|---|---|
| `--corpus` | *(Required)* | Path to JSON file containing reviewed manifestation labels. |
| `--output` | *(Required)* | Destination path for the calibration report. |
| `--valid-days` | `30` | Number of days the calibration report is considered valid. |

---

## 3. Supervised Operations & Incident Management

### `bookwarden pilot-stop`
Closes a persisted supervised V2 pilot session so that pending reservations and queued work fail closed.

```bash
bookwarden pilot-stop PILOT_ID --yes
```

---

### `bookwarden incident-ack`
Appends an immutable operator acknowledgement for a reconciled V2 operation in the `failed` state, without deleting history.

```bash
bookwarden incident-ack OPERATION_ID --actor ACTOR --reason "REASON" --yes
```

---

### `bookwarden retention`
Previews or safely deletes restore points that have exceeded their configured retention period.

```bash
# Dry run
bookwarden retention

# Execute deletion with verified paired backup manifest
bookwarden retention --execute --backup-reference /path/to/backup-manifest.json

# Resume an interrupted retention transaction
bookwarden retention --execute --recover-quarantine TRANSACTION_ID --backup-reference /path/to/backup-manifest.json
```

---

### `bookwarden undo`
Reverts an individual applied change by its `change_id`.

```bash
bookwarden undo CHANGE_ID
```

---

### `bookwarden apply` (Retired)
*Legacy command retired for safety.* Exits with status code 1 and instructs the user to use the authenticated Manifestation V2 API (`POST /api/apply/v2`).

---

## 4. Production Workers & Daemons

### `bookwarden verifier`
Runs the dedicated Certificate A verification worker process against a read-only library.

```bash
bookwarden verifier [--once]
```

### `bookwarden verifier-health`
Health check command for container orchestrators; exits 0 only if the verifier heartbeat is fresh in Valkey.

```bash
bookwarden verifier-health
```

### `bookwarden writer`
Runs the privileged metadata writer worker (Linux-only, memfd-sealed handoff).

```bash
bookwarden writer [--poll-seconds SECONDS] [--once]
```

### `bookwarden writer-health`
Health check command for writer container; exits 0 only if writer heartbeat is fresh.

```bash
bookwarden writer-health
```

### `bookwarden migrate`
Applies all pending Alembic schema migrations up to the current repository head.

```bash
bookwarden migrate
```

---

## 5. Bridges, Integrations & Services

### `bookwarden ingest-paperless`
Imports candidate ebooks or documents from a Paperless-ngx instance.

```bash
bookwarden ingest-paperless [--limit 50]
```

### `bookwarden hosts`
Discovers and tests connectivity to local homelab LLM and OCR hosts (Ollama, LM Studio).

```bash
bookwarden hosts
```

### `bookwarden mcp`
Launches the Model Context Protocol (MCP) server over standard I/O (STDIO) for integration with LLM agents (Claude Code, Hermes, Cursor).

```bash
bookwarden mcp
```

### `bookwarden web`
Starts the Uvicorn ASGI server hosting the REST API and WebUI.

```bash
bookwarden web [--host 0.0.0.0] [--port 8080] [--reload]
```

---

## 6. System Diagnostics & Inspection

### `bookwarden doctor`
Inspects installed system binaries (`calibredb`, `ebook-convert`, `tesseract`, `ocrmypdf`) and prints active library paths.

```bash
bookwarden doctor
```

### `bookwarden config`
Prints the effective runtime configuration (with secrets redacted) in JSON format.

```bash
bookwarden config
```

### `bookwarden inspect`
Standalone analysis of a single ebook file (EPUB, PDF, CBZ) without requiring a Calibre database.

```bash
bookwarden inspect --path "/path/to/book.epub" [--no-providers]
```

---

## 7. Legacy v0.9 Commands (Deprecated)

- `bookwarden scan [--limit 100]` — Legacy scan used by v0.9 pipeline.
- `bookwarden audit [--run latest] [--judge/--no-judge] [--save-evidence]` — Legacy audit engine. Prefer `verify`.

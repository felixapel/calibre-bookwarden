# CLI Reference

`bookwarden` (aliased as `bookaudit` for backward compatibility) is the primary command-line tool for managing **Calibre Bookwarden**. `verify` defaults to the Manifestation V2 engine. All commands run from the project root with `uv run bookwarden ...`, via `uvx bookwarden ...`, or inside the Docker container (`docker compose exec app bookwarden ...`). Content Server commands should normally run natively in the same network namespace as the loopback SSH tunnel.

## Global Options

| Option | Type | Description |
|---|---|---|
| `--config` / `-c` | PATH | Path to `config.yml` |

## Manifestation V2 commands (recommended)

### `bookaudit verify`

Run an exact-manifestation audit across a Calibre library. V2 freezes the run
membership, processes one book at a time, inspects all attached formats, and
persists a sealed Tier A/B/C evidence package. `--pipeline v1` selects the
legacy `BookVerdict` engine.

```bash
bookaudit verify [--pipeline v2|v1] [--limit N] [--library PATH]
                 [--use-ocr|--no-ocr] [--use-vision] [--use-llm]
                 [--allow-remote-text] [--allow-remote-images]
                 [--run-id ID] [--format text|json]
```

| Flag | Default | Description |
|---|---|---|
| `--limit` | 50 | Maximum number of books to verify. Set to `0` for the full library. |
| `--library` | configured | Override the library path. |
| `--pipeline` | `v2` | `v2` exact-manifestation contract or legacy `v1`. |
| `--use-ocr` / `--no-ocr` | enabled | OCR bounded PDF front matter when native identity evidence is absent. One engine remains non-authoritative. |
| `--use-vision` / `--no-vision` | disabled | Analyze bounded covers as non-authoritative evidence. Requires `recognition_v2.vision.enabled`. |
| `--use-llm` / `--no-llm` | disabled | Transcribe bounded evidence with the configured model. LLM observations never promote Tier A. |
| `--allow-remote-text` | denied | Per-run consent; global `privacy.allow_remote_text` must also be true. Body text is never included. |
| `--allow-remote-images` | denied | Per-run consent; global image permission and `--use-vision` are also required. |
| `--run-id` | generated | Resume the same V2 run and frozen membership. Terminal books are skipped. |
| `--format` | `text` | `text` for human-readable per-book output; `json` for parseable output (pipe to `> pilot.json`). |

**Examples**:
```bash
# Pilot: 100 books, native extraction + local OCR + exact providers
bookaudit verify --pipeline v2 --limit 100 --use-ocr

# Add a bounded LLM transcription witness
bookaudit verify --limit 100 --use-llm

# Full library as JSON for analysis
bookaudit verify --limit 0 --format json > manifestation-v2-audit.json
```

V2 `verify` never writes to Calibre. Supervised V2 changes use the authenticated
`/api/review/v2/{evidence_id}/authorize` and `/api/apply/v2` endpoints; the sole
writer performs the mutation.

### `bookaudit inventory` and `bookaudit verify-content-server`

Use an operator-created SSH tunnel and an independently verified read-only
Calibre account to inspect a live remote library. Inventory emits aggregate
counts only. Remote verification persists sealed V2 evidence with logical
source references, processes one book at a time, and remains shadow-only.

```bash
bookaudit inventory \
  --content-server http://127.0.0.1:18086 \
  --library-id EXACT_LIBRARY_ID --username READONLY_USER \
  --source-identity SHA256:VERIFIED_SSH_HOST_FINGERPRINT

bookaudit verify-content-server \
  --content-server http://127.0.0.1:18086 \
  --library-id EXACT_LIBRARY_ID --username READONLY_USER \
  --source-identity SHA256:VERIFIED_SSH_HOST_FINGERPRINT \
  --scratch-root reports/remote-scratch --limit 1 --use-ocr
```

Passwords are prompted without echo by default; `--password-stdin` is intended
for a protected secret pipe. Start with `--limit 1` and review the persisted
sample before increasing the limit. `--limit 0` means the full library. The
source identity must be stable across tunnel restarts and derived from a
separately verified SSH host fingerprint (or an equivalent immutable identity),
not from the local tunnel port. The
scratch directory must be private and is used only for short-lived exported
formats. Public metadata providers are denied unless
`--allow-public-providers` is explicit; the current remote command rejects LLM
and vision use even when the general project configuration enables them. Remote
evidence cannot be authorized by the supervised writer.

`inventory` is independent of the auditor database and does not initialize
providers, OCR, vision, or LLMs. Its optional output must be below the current
working directory and is created mode `0600`. `verify-content-server` requires
the configured database to be at the current Alembic revision; run
`bookaudit migrate` first. Its `--run-id` resumes only the same source-bound run
and frozen membership; omit it to generate a new run.

### `bookaudit pilot-stop`

Close one exact persisted supervised V2 pilot so new reservations and queued
V2 work fail closed at the writer boundary:

```bash
bookaudit pilot-stop PILOT_ID --yes
```

Stop app intake and the writer, then set the configured supervised-pilot flag
to `false` before running this command. `--yes` is mandatory. The command is
idempotent for an already stopped ID and refuses an unknown ID. It cannot cancel
a Calibre subprocess already in progress; reconcile the ledger and recovery
artifacts before restart.

### `bookaudit incident-ack`

Append an immutable operator acknowledgement for one reconciled V2 operation
whose durable state is exactly `failed`:

```bash
bookaudit incident-ack OPERATION_ID \
  --actor on-call-operator \
  --reason "Verified failure occurred before mutation and Calibre is unchanged" \
  --yes
```

The command requires a completed V2 `failed` ledger row, a failed outbox row,
no active book-write lease, and the operation's exact persisted pilot already
closed by `pilot-stop`. It locks that stopped pilot while appending evidence.
It never changes or deletes the operation or outbox evidence. It rejects
`unknown`, `restore_failed`, successful, legacy, active, open-pilot, and already
acknowledged operations. Before using it, stop intake and the writer and compare
live Calibre metadata with the ledger and hashed recovery artifacts.
Acknowledgement only clears that historical failed outbox from the next-pilot
gate and failed-operation alert; the stopped ID remains unusable and only a
distinct, separately reviewed pilot may continue. It does not repair metadata
or refund the consumed reservation.

### `bookaudit calibrate-v2`

Derive an integrity-checksummed, advisory calibration report from unique
human-reviewed V2 package labels. It cannot enable writes:

```bash
bookaudit calibrate-v2 \
  --corpus /secure/reviewed-manifestations.json \
  --output /secure/manifestation-v2-calibration.json \
  --valid-days 30
```

The command computes the corpus hash, sample/Tier A counts, false-positive
count, and false-auto-apply count. It rejects malformed/duplicate labels and
atomically writes a mode-0600 report. See the
[V2 calibration runbook](docs/calibration/manifestation-v2-runbook.md).

### `bookaudit hosts`

Discover and report the homelab inference hosts reachable via Ollama or
LM Studio. Used to verify that the v1.0 multi-host routing has the right
endpoints configured.

```bash
bookaudit hosts
```

**Example output**:
```json
{
  "total_hosts": 3,
  "healthy_hosts": 2,
  "hosts": [
    {
      "name": "gaming-pc-3090",
      "base_url": "http://192.168.0.89:1234/v1",
      "gpu_class": "high",
      "gpu_name": "RTX 3090",
      "models": ["qwen3.6-27b-mtp", ...],
      "supports_vision": false
    },
    ...
  ]
}
```

### `bookaudit apply` (retired)

This command is retained only to give old automation an explicit nonzero failure.
It never writes, in any profile. Use exact V2 package authorization and
`POST /api/apply/v2`.

```bash
bookaudit apply [--safe-only] [--yes]
```

| Flag | Default | Description |
|---|---|---|
| `--safe-only` | `True` | Retained for command-line compatibility; ignored. |
| `--yes` / `-y` | `False` | Retained for command-line compatibility; ignored. |

**Effect**: exits with status 1 and explains the supervised V2 replacement.

### `bookaudit undo`

Revert one change by `change_id`. Run-wide reverts are queued through
`POST /api/runs/{run_id}/revert`; there is no run-wide CLI flag.

```bash
# Single change undo
bookaudit undo <change_id>

```

Restore points live at `<artifacts_dir>/restore/<run_id>/<book_key>/`. The
retention target is 30 days, with explicit operator-approved cleanup.

## Sanity / introspection

### `bookaudit doctor`

Check local system dependencies, including Calibre conversion tools,
Tesseract/OCRmyPDF, and report the configured paths.

```bash
bookaudit doctor
```

### `bookaudit config`

Display the effective runtime configuration with sensitive keys redacted.

```bash
bookaudit config
```

## v1.2 — MCP Server (Hermes / agent integration)

### `bookaudit mcp`

Start the read-only MCP server over stdio. Exposes audit query tools to MCP clients
(Hermes, Claude Code, etc.).

```bash
bookaudit mcp
```

Requires the optional extra:

```bash
uv pip install -e '.[mcp]'
# or
pip install fastmcp
```

**Exposed tools** (all read-only, no DB mutation):

- `query_book_audit(book_key)` — current BookRecord + latest BookVerdict (per-field results, risk flags, proposed patch, decimal chapter/volume/series_position)
- `list_problematic_books(limit=50, status=None, has_risk=True)` — books with needs_review / suggest_fix / defer (or explicit status); comic fields preserved
- `get_run_metrics(run_id=None)` — status counts + sample for a run (latest if omitted)
- `list_recent_runs(limit=10)` — recent Run rows for discovery

Intended for workflows such as "Hermes: list my 20 worst metadata books".

See ROADMAP.md v1.2 and `src/calibre_ai_auditor/mcp_server.py` for details.

### `bookaudit web`

Start the FastAPI backend and serve the WebUI (built React SPA).

```bash
bookaudit web [--host 0.0.0.0] [--port 8080] [--reload]
```

| Flag | Default | Description |
|---|---|---|
| `--host` | `0.0.0.0` | Bind host. |
| `--port` | `8080` | Bind port. The WebUI uses this via Vite proxy. |
| `--reload` | `False` | Enable uvicorn hot-reload for development. |

## Legacy v0.9 commands (still work, deprecated for new flows)

The v0.9 commands are kept as a fallback path and for users migrating
existing workflows. New code should use `verify` + WebUI Review.

### `bookaudit scan`

Scan a Calibre library for books and register a new run. Used by the legacy
audit engine.

```bash
bookaudit scan [--limit 100]
```

| Flag | Default | Description |
|---|---|---|
| `--limit` | 100 | Maximum number of books to scan. Set to `0` for unlimited. |

### `bookaudit inspect`

Standalone inspection of a single file path. Does not require a Calibre
library. Useful for testing the engine on a single book before adding it.

```bash
bookaudit inspect --path "/path/to/book.epub" [--no-providers]
```

| Flag | Description |
|---|---|
| `--path` | Path to the ebook file (EPUB, PDF, etc.). |
| `--no-providers` | Skip fetching candidate metadata from external APIs. |

Internally, `inspect` now uses the v1.0 `ContentVerificationEngine` to
produce the response (no more v0.9 `build_evidence_package`).

### `bookaudit audit`

Legacy v0.9 audit engine. Wraps the v1.0 engine internally — prefer
`verify` for new flows.

```bash
bookaudit audit [--run latest] [--judge/--no-judge] [--save-evidence]
```

| Flag | Default | Description |
|---|---|---|
| `--run` | `latest` | Run ID or `latest`. |
| `--judge` / `--no-judge` | `judge` | Enable or skip the LLM witness. |
| `--save-evidence` | `True` | Persist the BookVerdict back into EvidencePackage.decision. |

### Run reports (API)

There is no `bookaudit report` CLI command. Export a run summary via the
web API instead: `GET /api/preview/{book_key}.pdf` renders the review-packet
PDF, and run/audit endpoints return JSON. Markdown rendering lives in
`reports/writer.py:generate_markdown_report` for API use.

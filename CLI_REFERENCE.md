# CLI Reference

`bookaudit` is the primary command-line tool for managing the
calibre-ai-auditor v1.0 engine. All commands run from the project root
with the venv activated (`source .venv/bin/activate`) or inside the Docker
container (`docker compose exec app bookaudit ...`).

## Global Options

| Option | Type | Description |
|---|---|---|
| `--config` / `-c` | PATH | Path to `config.yml` |

## v1.0 commands (recommended)

### `bookaudit verify`

Run the v1.0 content-ground verification engine across a Calibre library.
For each book, the engine produces per-field `FieldVerdict`s (title, authors,
ISBN, publisher, date, language, series, series_index) and aggregates them
into a `BookVerdict` with an action (`no_change` / `suggest_fix` /
`needs_review` / `defer`).

```bash
bookaudit verify [--limit N] [--library PATH] [--use-llm] [--format text|json]
```

| Flag | Default | Description |
|---|---|---|
| `--limit` | 50 | Maximum number of books to verify. Set to `0` for the full library. |
| `--library` | configured | Override the library path. |
| `--use-llm` | `false` | Send ambiguous fields to the configured LLM witness. Adds latency + cost but improves accuracy. |
| `--format` | `text` | `text` for human-readable per-book output; `json` for parseable output (pipe to `> pilot.json`). |

**Examples**:
```bash
# Pilot: 100 books, deterministic only
bookaudit verify --limit 100

# Pilot with LLM witness for ambiguous fields
bookaudit verify --limit 100 --use-llm

# Full library as JSON for analysis
bookaudit verify --limit 0 --format json > v1_audit.json
```

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

### `bookaudit apply` (v1.0 path)

Apply approved fixes to the Calibre library. The v1.0 conservative auto-apply
gate (≥80% confidence AND no high-risk flags AND per-book restore point
already created) decides which books can be applied without manual approval.

```bash
bookaudit apply [--safe-only] [--yes]
```

| Flag | Default | Description |
|---|---|---|
| `--safe-only` | `True` | Only apply books marked `auto_apply_eligible: true` by the v1.0 engine. |
| `--yes` / `-y` | `False` | Skip the confirmation prompt. |

**Effect**: For each auto-eligible book, the v1.0 `RestorePointStore` first
writes a per-book restore point (OPF + cover + hardlinked file + JSON
snapshot), then `calibredb set_metadata` applies the patch.

### `bookaudit undo`

Revert a change. Supports both single-change undo (v0.9 path, by `change_id`)
and bulk undo (v1.0 path, by `run_id`).

```bash
# Single change undo
bookaudit undo <change_id>

# Bulk undo of all changes from a v1.0 run
bookaudit undo --run <run_id>
```

Restore points live at `<artifacts_dir>/restore/<run_id>/<book_key>/` and
have a 7-day TTL (configurable).

## Sanity / introspection

### `bookaudit doctor`

Check system dependencies and connectivity to sidecars (Calibre CLI,
Ollama, Tika, Qdrant, homelab inference hosts).

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

### `bookaudit report`

Export a run summary in Markdown or JSON format.

```bash
bookaudit report [--run latest] [--format markdown|json] [--output PATH]
```

| Flag | Default | Description |
|---|---|---|
| `--run` | `latest` | Run ID or `latest`. |
| `--format` | `markdown` | `markdown` or `json`. |
| `--output` | stdout | Destination file path. |

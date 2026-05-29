# CLI Reference

`bookaudit` is the primary command-line tool for managing the calibre-ai-auditor engine.

## Global Options

| Option | Type | Description |
|---|---|---|
| `--config` / `-c` | PATH | Path to `config.yml` |

## Commands

### `bookaudit doctor`
Check system dependencies and connectivity to sidecars (Ollama, Tika, Qdrant).

```bash
bookaudit doctor
```

### `bookaudit scan`
Scan a Calibre library for books and register a new run.

```bash
bookaudit scan [--limit 100]
```

*   `--limit`: Maximum number of books to scan (default: 100). Set to 0 for unlimited.

### `bookaudit inspect`
Standalone inspection of a single file path. Does not require a Calibre library.

```bash
bookaudit inspect --path "/path/to/book.epub" [--no-providers]
```

*   `--path`: Path to the ebook file (EPUB, PDF, etc.).
*   `--no-providers`: Skip fetching candidate metadata from external APIs.

### `bookaudit audit`
Build evidence packages and optionally call the judge model for a specific run.

```bash
bookaudit audit [--run latest] [--judge/--no-judge] [--save-evidence]
```

*   `--run`: Run ID or `latest` (default: latest).
*   `--judge` / `--no-judge`: Enable or skip LLM judgment (default: judge).
*   `--save-evidence`: Persist evidence JSON to the database (default: True).

### `bookaudit report`
Export a run summary in Markdown or JSON format.

```bash
bookaudit report [--run latest] [--format markdown|json] [--output PATH]
```

*   `--run`: Run ID or `latest`.
*   `--format`: Output format (default: markdown).
*   `--output`: Destination file path.

### `bookaudit apply`
Apply approved fixes to the Calibre library. Requires write mode.

```bash
bookaudit apply [--run latest] [--safe-only] [--yes]
```

*   `--run`: Run ID or `latest`.
*   `--safe-only`: Only apply high-confidence 'suggest_fix' results (default: True).
*   `--yes` / `-y`: Skip the confirmation prompt.

### `bookaudit undo`
Revert an applied change using the backup OPF file.

```bash
bookaudit undo <change_id>
```

*   `<change_id>`: The ID of the change record to revert.

### `bookaudit web`
Start the FastAPI backend and serve the WebUI.

```bash
bookaudit web [--host 0.0.0.0] [--port 8080] [--reload]
```

*   `--host`: Host to bind the server to (default: 0.0.0.0).
*   `--port`: Port to bind the server to (default: 8080).
*   `--reload`: Enable hot-reloading for development.

### `bookaudit config`
Display the effective runtime configuration (with sensitive keys redacted).

```bash
bookaudit config
```

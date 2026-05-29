# API Documentation

The `calibre-ai-auditor` API follows a RESTful design. Interactive OpenAPI docs are available at `/docs` when the server is running.

## System & Config

*   `GET /api/health` — Application health.
*   `GET /api/doctor` — Connectivity checks (Tika, Qdrant, Ollama, Gotenberg).
*   `GET /api/config` — Runtime configuration (sanitized).
*   `POST /api/config` — Update selected settings (`log_level`, `privacy`, `providers`).
*   `GET /api/providers` — List metadata providers.
*   `POST /api/providers/test?name=...` — Live connectivity probe (Open Library, Google Books, or Calibre library).

## Jobs & Runs

*   `GET /api/runs` — List scan/audit runs.
*   `POST /api/runs/scan` — Start library scan (returns `{ "job_id": "..." }`).
*   `POST /api/runs/{run_id}/audit` — Start audit for a run (returns `job_id`).
*   `GET /api/jobs/{job_id}` — Job status and result.
*   `POST /api/runs/{run_id}/revert` — Undo all changes for a run. Body: `{ "force": true }` required.

## Books & Evidence

*   `GET /api/books` — List books (latest run snapshot, limit 100).
*   `GET /api/books/all/duplicates` — Fuzzy, ISBN, and (if enabled) semantic duplicates.
*   `GET /api/books/{book_key}` — Book details.
*   `GET /api/books/{book_key}/evidence` — Evidence package.
*   `GET /api/books/{book_key}/resolution` — Resolver/judge decision JSON.
*   `GET /api/books/{book_key}/similar` — Vector similarity (requires `vectors.enabled`).

## Inspections & Previews

*   `GET /api/inspect/fs?dir_path=...` — Browse library paths (sandboxed).
*   `POST /api/inspect/path` — Inspect one file. Body: `{ "path": "...", "no_providers": false }`.
*   `POST /api/inspect/upload` — Upload and inspect (requires `BOOKAUDIT_ALLOW_REMOTE_FILE_UPLOAD=true`, max 100MB).
*   `GET /api/preview/{book_key}` — HTML review packet.
*   `GET /api/preview/{book_key}.pdf` — PDF packet (requires Gotenberg).

## Review & Application

*   `POST /api/review/{book_key}/approve` — Mark book as ready to apply (`suggest_fix`).
*   `POST /api/review/{book_key}/reject` — Reject proposed changes.
*   `POST /api/review/{book_key}/lock-field` — Lock a field. Body: `{ "field": "title", "value": "..." }`. Locked fields are excluded from apply and win in evidence resolution.
*   `POST /api/apply` — Apply all `suggest_fix` books. Body: `{ "force": true }` required; library must not be read-only.
*   `POST /api/undo/{change_id}` — Revert one change. Body: `{ "force": true }` required.

## Bridges

*   `POST /api/bridges/paperless/webhook` — Ingest and audit a Paperless document (query or JSON `document_id` / `id`). If `PAPERLESS_WEBHOOK_SECRET` is set, send header `X-Webhook-Secret`.

## Static

*   `GET /api/covers/...` — Extracted cover images from artifacts.

## Authentication

If `BOOKAUDIT_API_KEY` is set, send header `X-API-Key` on all `/api/*` requests.

# API Documentation

The `calibre-ai-auditor` v1.0 API follows a RESTful design. Interactive
OpenAPI docs are available at `/docs` when the server is running.

All responses use the shape:

```json
{ "status": "success", "data": { ... } }
```

If `BOOKAUDIT_API_KEY` is set, send header `X-API-Key` on all `/api/*` requests.

---

## System & Config

| Endpoint | Description |
|---|---|
| `GET /api/health` | Application health (returns `{ "status": "ok" }`). |
| `GET /api/metrics` | **v1.0** Prometheus text exposition. Counter / gauge / histogram metrics from `Metrics` collector. |
| `GET /api/doctor` | Connectivity checks: Calibre CLI, Tika, Qdrant, Gotenberg, Ollama + homelab inference hosts. |
| `GET /api/config` | Runtime configuration (sanitized; sensitive keys redacted). |
| `POST /api/config` | Update selected settings (`log_level`, `privacy`, `providers`). |
| `GET /api/providers` | List metadata providers. |
| `POST /api/providers/test?name=...` | Live connectivity probe (Open Library, Google Books, or Calibre library). |

---

## v1.0 Verify (new)

| Endpoint | Description |
|---|---|
| `POST /api/verify` | **v1.0** Start a verify run. Body: `{ "library"?: str, "limit"?: int, "use_llm"?: bool }`. Returns `{ "data": { "run_id": "verify_...", "total": int, "status": "running" } }`. |
| `GET /api/verify/runs` | **v1.0** List recent verify runs with summary stats. |
| `GET /api/verify/{run_id}` | **v1.0** Get progress + per-book verdicts for a run. |

### Verify response shape

```json
{
  "run_id": "verify_20260709_120000",
  "status": "running",
  "started_at": "2026-07-09T12:00:00+00:00",
  "total": 1234,
  "completed": 567,
  "counts": { "no_change": 350, "suggest_fix": 150, "needs_review": 50, "defer": 17 },
  "verdicts": [
    {
      "book_key": "calibre:1",
      "field_verdicts": {
        "title":    { "field": "title",    "verdict": "confirmed", "confidence": 99, "evidence": [...] },
        "authors":  { "field": "authors",  "verdict": "confirmed", "confidence": 99 },
        "isbn":     { "field": "isbn",     "verdict": "mismatch",  "confidence": 92, "risk_flags": ["isbn_conflict"] }
      },
      "overall_confidence": 96,
      "risk_flags": ["isbn_conflict"],
      "action": "needs_review",
      "auto_apply_eligible": false,
      "proposed_patch": { "isbn": "9780451524935" },
      "reasons": ["[isbn] ISBN conflict: declared ... but book has ..."]
    }
  ]
}
```

---

## Jobs & Runs (legacy v0.9 paths — still work)

| Endpoint | Description |
|---|---|
| `GET /api/runs` | List scan/audit runs. |
| `POST /api/runs/scan` | Start library scan. Returns `{ "job_id": "..." }`. |
| `POST /api/runs/{run_id}/audit` | Start legacy v0.9 audit for a run. Returns `job_id`. Internally backed by the v1.0 engine. |
| `GET /api/jobs/{job_id}` | Job status and result. |
| `POST /api/runs/{run_id}/revert` | Undo all changes for a run (legacy v0.9 path). Body: `{ "force": true }` required. |

---

## Books & Evidence

| Endpoint | Description |
|---|---|
| `GET /api/books` | List books (latest run snapshot, limit 100). |
| `GET /api/books/all/duplicates` | Fuzzy, ISBN, and (if enabled) semantic duplicates via Qdrant. |
| `GET /api/books/{book_key}` | Book details. |
| `GET /api/books/{book_key}/evidence` | Evidence package (legacy shape). |
| `GET /api/books/{book_key}/verdict` | **v1.0** Per-field `BookVerdict` for one book. Drives the Review page's per-field chip rendering. |
| `GET /api/books/{book_key}/similar` | Vector similarity (requires `vectors.enabled`). |

---

## Inspections & Previews

| Endpoint | Description |
|---|---|
| `GET /api/inspect/fs?dir_path=...` | Browse library paths (sandboxed to configured library roots). |
| `POST /api/inspect/path` | Inspect one file. Body: `{ "path": "...", "no_providers": false }`. Internally uses the v1.0 engine. |
| `POST /api/inspect/upload` | Upload + inspect (requires `BOOKAUDIT_ALLOW_REMOTE_FILE_UPLOAD=true`, max 100MB). |
| `GET /api/preview/{book_key}` | HTML review packet. |
| `GET /api/preview/{book_key}.pdf` | PDF packet (requires Gotenberg). |

---

## Review & Application

| Endpoint | Description |
|---|---|
| `POST /api/review/{book_key}/approve` | Mark book as ready to apply (`suggest_fix`). |
| `POST /api/review/{book_key}/reject` | Reject proposed changes; book is removed from queue. |
| `POST /api/review/{book_key}/lock-field` | Lock a field. Body: `{ "field": "title", "value": "..." }`. Locked fields win in resolution. |
| `POST /api/apply` | Queue explicit approved books for the sole writer. Body: `{ "force": true, "book_keys": ["calibre:123"] }`; an empty selection is rejected. |
| `POST /api/undo/{change_id}` | Queue one explicit undo for the sole writer. Body: `{ "force": true }` required. |

---

## Bridges

| Endpoint | Description |
|---|---|
| `POST /api/bridges/paperless/webhook` | Ingest + audit a Paperless document (query or JSON `document_id` / `id`). If `PAPERLESS_WEBHOOK_SECRET` is set, send header `X-Webhook-Secret`. |

---

## Static

| Endpoint | Description |
|---|---|
| `GET /api/covers/...` | Extracted cover images from artifacts. |

---

## Error responses

All errors use the shape `{ "status": "error", "detail": "..." }`. Common HTTP codes:
- `400` — validation error
- `403` — sandbox violation (path outside library root), upload disabled
- `404` — book / run / file not found
- `500` — internal error

---

## Versioning

The API is currently `v1.0.0`. Breaking changes will be versioned under `/api/v2/` in a future release.

# API Documentation

The `calibre-ai-auditor` API exposes Manifestation V2 as the default verify
contract while retaining explicitly versioned legacy V1 routes and payloads.
Interactive OpenAPI docs are available at `/docs` when the server is running.

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

## Verify

| Endpoint | Description |
|---|---|
| `POST /api/verify` | Start a run. `pipeline` defaults to `v2`; `v1` remains available. Returns `{ "data": { "run_id": "verify_...", "total": int, "status": "running" } }`. |
| `GET /api/verify/runs` | List recent V1/V2 runs with pipeline version, mode, and summary stats. |
| `GET /api/verify/{run_id}` | Get progress and per-book sealed V2 summaries or legacy V1 verdicts. |

### Manifestation V2 start body

```json
{
  "pipeline": "v2",
  "library": "/library",
  "limit": 100,
  "run_id": null,
  "use_ocr": true,
  "use_vision": false,
  "use_llm": false,
  "allow_remote_text": false,
  "allow_remote_images": false
}
```

`run_id` resumes only the same frozen library membership and contract. OCR is
bounded and local by default. Vision requires `recognition_v2.vision.enabled`
in configuration. Remote text or images additionally require their global
privacy setting and the corresponding per-run consent above.

### Manifestation V2 result summary

```json
{
  "schema_version": 2,
  "evidence_id": "evidence_...",
  "book_key": "calibre:1",
  "state": "shadowed",
  "identity": {
    "tier": "A",
    "manifestation_ids": {"isbn": "9780306406157"},
    "field_decisions": {},
    "auto_patch": {"title": "Exact title"},
    "risk_flags": []
  },
  "warnings": []
}
```

The database-linked evidence package contains the full format inspections,
source provenance, locators, hashes, privacy receipts, package seal, and error
state. V2 does not populate the legacy `decision` column.

### Legacy V1 verify response shape

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
| `POST /api/apply` | Retired legacy route; always returns HTTP 410 and never queues work. |
| `POST /api/review/v2/{evidence_id}/authorize` | Authorize one exact sealed Tier A package. Body: `{ "reason": "..." }`. Returns `authorization_id`. |
| `POST /api/apply/v2` | Queue V2 packages for the sole writer. Body: `{ "force": true, "evidence_ids": ["evidence_..."], "authorization_ids": {"evidence_...": "authorization-uuid"} }`. |
| `POST /api/undo/{change_id}` | Queue one explicit undo for the sole writer. Body: `{ "force": true }` required. |

V2 authorization is immutable and bound to the evidence-package SHA-256 plus
the canonical patch after field locks. Reusing it after any package, run,
snapshot, lock, patch, live Calibre value, format membership, path, or ebook
hash changes fails closed. API calls
only queue operations; filesystem writes remain in the sole writer process.

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
- `410` — retired legacy apply route
- `500` — internal error

---

## Versioning

Manifestation V2 is a versioned evidence/policy contract on the existing API
routes, not an `/api/v2` URL namespace. `pipeline: "v1"` is retained for legacy
clients while V2 clients use `schema_version: 2` and `policy_version:
"manifestation-v2"`.

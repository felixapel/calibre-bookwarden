# Certificate A API

The production application exposes a small, versioned-by-contract API. OpenAPI,
Swagger, Redoc, legacy V1 routes, uploads, authorization, and writer operations
are not registered.

All `/api` routes except liveness require the internal `X-API-Key` header. Caddy
adds a separate whole-site browser authentication boundary. The internal API key
must not be placed in URLs, browser storage, logs, or Prometheus configuration
text; Prometheus reads it from a protected file.

## Health and capabilities

### `GET /api/health/live`

Public container liveness only:

```json
{"status":"alive"}
```

It does not establish database, verifier, source, or release readiness.

### `GET /api/health/ready`

Requires the API key. Success:

```json
{"status":"ready","certificate":"A"}
```

Readiness verifies production configuration, PostgreSQL, exact `bookaudit_app`
role, exact Alembic head, and a fresh verifier heartbeat matching the release
digest, schema revision, and library-root hash. Failure is HTTP 503 with one of
the sanitized codes `configuration_invalid`, `database_unavailable`, or
`verifier_unavailable`.

### `GET /api/capabilities`

Returns the non-secret production contract: Certificate A, shadow mode,
offline-folder source, exact-ISBN providers, Tesseract limits, and
`writes_enabled: false`. It never returns DSNs, provider credentials, host
paths, or the internal API key.

### `GET /api/metrics`

Prometheus text format with bounded labels. It refreshes exact readiness,
verifier health, per-status Certificate A run counts, oldest active heartbeat
age, and metrics-collection success before rendering. Run IDs, book IDs, paths,
titles, and evidence IDs are not metric labels.

## Verification requests

### `POST /api/verify`

Headers:

```text
X-API-Key: <internal key>
Idempotency-Key: <16-128 ASCII letters, digits, dot, underscore, colon, or hyphen>
Content-Type: application/json
```

Body:

```json
{
  "limit": 100,
  "use_ocr": true,
  "confirm_calibre_stopped": true
}
```

- `limit` may be omitted or be 1–10,000.
- `use_ocr` selects the configured bounded Tesseract path only.
- `confirm_calibre_stopped` must be literal `true`.
- Unknown fields are rejected.

The app persists an immutable request and returns HTTP 202. It does not inspect
the library or start in-process work. Reusing an idempotency key with the same
body returns the original run; reusing it with a different body returns 409
`idempotency_key_reused`. Only one active run for a source root is admitted.

Example response:

```json
{
  "run_id": "verify_<opaque-id>",
  "status": "pending",
  "started_at": "2026-08-08T12:00:00Z",
  "finished_at": null,
  "total": null,
  "completed": 0,
  "counts": {},
  "error_code": null
}
```

`total` remains `null` until the verifier freezes inventory.

### `GET /api/verify/runs?limit=50&offset=0`

Lists only Certificate A / Manifestation V2 runs, newest first. `limit` is
1–100 and `offset` is non-negative.

### `GET /api/verify/{run_id}`

Returns the run summary plus frozen result rows containing `book_key`, `state`,
and optional `evidence_id`. It never returns the configured source path or
worker lease owner/fence internals.

### `POST /api/verify/{run_id}/cancel`

Persists cancellation and returns HTTP 202 with state `cancelling`. The verifier
stops at a safe fenced boundary. Cancellation of a terminal run returns 409
`run_already_terminal`; unknown IDs return 404 `run_not_found`.

Run states are:

```text
pending, inventorying, running, cancelling,
cancelled, completed, completed_with_errors,
failed, source_changed, blocked_recovery
```

## Evidence review

### `GET /api/review/v2`

Optional filters are `run_id`, `state`, `tier`, `limit` (1–100), and `offset`.
Only sealed V2 evidence joined to a Certificate A run is returned. The summary
includes evidence/run/book keys, time, state, tier, current metadata,
manifestation identifiers, proposed patch field names, and risk flags.

Proposed patch fields are evidence for human review, not executable writes.

### `GET /api/review/v2/{evidence_id}`

Validates the stored package schema, SHA-256 seal, evidence ID, run ID, and book
key before returning it. Integrity failure returns 409
`evidence_integrity_failed`. The envelope always contains:

```json
{
  "status": "success",
  "data": {
    "package": {},
    "authorization": null,
    "operation": null,
    "writes_enabled": false
  }
}
```

## Retired write route

`POST /api/apply` returns HTTP 410 with a Certificate A shadow-only message. No
authorization, queue, preview-write, undo, or metadata mutation route is
registered.

## Common responses

- `400` invalid trusted host.
- `401` missing or invalid internal API key.
- `409` idempotency, active-run, terminal-state, or evidence-integrity conflict.
- `422` body, header, path, or query validation failure.
- `429` rate limit exceeded with `Retry-After`.
- `503` invalid production configuration, unavailable rate limiter/database, or
  unavailable verifier.

API responses use `Cache-Control: no-store`; security headers and an opaque
`X-Request-ID` are added to every response. A caller-supplied request ID is
accepted only when it matches the bounded safe character contract.

# Calibre Bookwarden API Reference

Calibre Bookwarden exposes a modern, high-performance REST API powered by FastAPI. Depending on deployment mode and profile, the API operates in either **Enterprise Mode (Certificate A Cold Boundary)** or **Homelab Companion Mode (Live Studio & Calibre-Web Companion)**.

---

## 1. Security & Authentication Boundary

### Headers & Authentication
- **Internal API Key**: All non-public endpoints require the `X-API-Key: <token>` header matching `BOOKWARDEN_API_KEY` (or `BOOKAUDIT_API_KEY`).
  - In `production` profile, weak keys (<32 characters or low entropy) cause the server to refuse startup or return `503 Service Unavailable`.
- **Public Endpoints**: Only `/api/health/live` is completely unauthenticated.
- **Request ID Tracking**: Every response includes an `X-Request-ID` header (caller-supplied if matching `^[A-Za-z0-9._:-]{1,128}$`, or generated UUIDv4).
- **Hardened Browser Headers**: Responses include strict `Content-Security-Policy`, `X-Content-Type-Options: nosniff`, `X-Frame-Options: DENY`, `Referrer-Policy: no-referrer`, and `Permissions-Policy`.
- **Rate Limiting**: In production profile, bounded Valkey/Redis token-bucket rate limiting enforces requests per window, returning HTTP `429 Too Many Requests` with `Retry-After`.

---

## 2. System & Observability Endpoints

### `GET /api/health/live`
Container liveness probe. Returns HTTP 200 immediately if the HTTP worker is running.
```json
{
  "status": "alive"
}
```

### `GET /api/health/ready`
Readiness probe verifying production prerequisites: configuration validity, PostgreSQL/SQLite connection, Alembic head migration (`f4a2d6e8c013`), and active background verifier heartbeat.
```json
{
  "status": "ready",
  "certificate": "A"
}
```
*Failure status*: HTTP `503 Service Unavailable` with `detail: "configuration_invalid" | "database_unavailable" | "verifier_unavailable"`.

### `GET /api/health`
Standard service status and runtime metadata.
```json
{
  "status": "ok",
  "app": "calibre-bookwarden",
  "version": "1.3.0"
}
```

### `GET /api/metrics`
Prometheus metrics endpoint (`text/plain; version=0.0.4`). Exposes bounded-cardinality counters, histograms, and gauges:
- `calibre_ai_auditor_http_requests_total`
- `calibre_ai_auditor_http_request_duration_seconds`
- `calibre_ai_auditor_verification_runs_total`
- `calibre_ai_auditor_verifier_heartbeat_age_seconds`
- `calibre_ai_auditor_cqs_score_distribution`

### `GET /api/doctor`
Comprehensive diagnostic report checking:
- Database connectivity & schema version.
- Calibre library accessibility (`metadata.db` read/write permissions, locks).
- External metadata provider reachability (Google Books, OpenLibrary, Crossref).
- Local OCR tools (Tesseract binary & language models).

### `GET /api/capabilities`
Returns the operational contract of the current runtime (Certificate tier, read-only status, enabled providers, storage backend). Never leaks host filepaths, database DSNs, or credentials.

---

## 3. 360° Forensic Audit Endpoints (`/api/audit`)

### `GET /api/audit/360`
Performs an instant forensic scan of the library using `DirectCalibreEngine` (benchmarked at ~6,090 books/second).
- **Query Parameters**:
  - `include_covers` (bool, default `true`): Include CQS cover quality assessment.
  - `include_duplicates` (bool, default `true`): Include cross-format duplicate cluster detection.
- **Response**:
```json
{
  "status": "success",
  "data": {
    "scanned_at": "2026-09-13T12:00:00Z",
    "total_books": 575,
    "metrics": {
      "missing_isbn": 12,
      "missing_publisher": 8,
      "all_caps_titles": 0,
      "malformed_isbns": 0,
      "spurious_covers": 3,
      "low_res_covers": 14,
      "unsorted_authors": 0
    },
    "integrity": {
      "missing_format_files": [],
      "orphaned_records": 0,
      "db_corruptions": 0
    }
  }
}
```

### `POST /api/audit/sync-author-sorts`
Executes automated synchronization of `author_sort` in Calibre SQLite database using the royal/particle/multiname authority algorithm.
- **Response**:
```json
{
  "status": "success",
  "data": {
    "authors_evaluated": 86,
    "authors_updated": 86,
    "books_affected": 575,
    "duration_ms": 42.8
  }
}
```

### `POST /api/audit/purge-orphan-fks`
Safely purges orphaned foreign key records in `books_authors_link`, `books_tags_link`, and `books_series_link` that reference non-existent book records.

---

## 4. Cover Studio & CQS Endpoints (`/api/covers`)

### `POST /api/covers/score`
Calculates the multi-factor **Cover Quality Score (CQS 0–100)** for a specific book.
- **Request Body**:
```json
{
  "book_id": 4127
}
```
- **Response**:
```json
{
  "status": "success",
  "data": {
    "book_id": 4127,
    "cqs": 94.5,
    "grade": "A+",
    "resolution": {"width": 1600, "height": 2400, "megapixels": 3.84},
    "aspect_ratio": 0.667,
    "sharpness": 142.6,
    "entropy": 7.82,
    "is_spurious": false,
    "spurious_reasons": []
  }
}
```

### `POST /api/covers/score-upload`
Evaluates CQS and checks for generic/placeholder cover artifacts on an uploaded image file (`multipart/form-data`).
- Limits: 60 Megapixels max (decompression bomb protection), 50 MB file size limit.

### `GET /api/covers/deck`
Returns a paginated list of candidate books with low CQS (<60) or detected spurious covers for triage in the Cover Deck UI.
- **Query Parameters**:
  - `threshold` (int, default `60`): Max CQS score to include.
  - `limit` (int, default `20`): Batch size.

### `POST /api/covers/extract-native`
Attempts to extract the native, lossless cover embedded inside the book container (EPUB/MOBI/CBZ) to replace low-res Calibre placeholders without external network calls.

### `GET /api/covers/book/{book_id}/image`
Serves the current cached or Calibre cover image for the requested book ID.

### `GET /api/covers/ui/deck`
Renders the interactive, swipeable HTMX/Tailwind **Cover Deck** component.

### `POST /api/covers/ui/deck/action`
Handles HTMX interactions from the Cover Deck.
- **Form Data**:
  - `book_id`: ID of target book.
  - `action`: `keep` | `replace_hd` | `extract_native` | `generate_ai` | `skip`.

---

## 5. Library Curation Endpoints (`/api/curation`)

### `GET /api/curation/duplicates`
Detects duplicate book clusters across formats (e.g. EPUB + PDF, identical byte clones, and title+author variants).
- **Query Parameters**:
  - `strategy` (string, default `all`): `fingerprint` | `exact_clone` | `title_author`.
- **Response**:
```json
{
  "status": "success",
  "data": {
    "cluster_count": 78,
    "clusters": [
      {
        "primary_id": 102,
        "primary_title": "Free to Choose",
        "members": [
          {"id": 102, "format": "EPUB", "size_bytes": 1420500},
          {"id": 418, "format": "PDF", "size_bytes": 5210900}
        ],
        "is_complementary_multiformat": true
      }
    ]
  }
}
```

### `GET /api/curation/series-gaps`
Analyzes series indices across the library to identify missing books in a series.
- **Response**:
```json
{
  "status": "success",
  "data": {
    "series_analyzed": 14,
    "gaps": [
      {
        "series_name": "The Expanse",
        "author": "James S.A. Corey",
        "owned_indices": [1.0, 2.0, 4.0, 5.0],
        "missing_indices": [3.0]
      }
    ]
  }
}
```

---

## 6. Bridges & Ingestion Endpoints (`/api/bridges`)

### `POST /api/bridges/paperless/webhook`
Webhook endpoint for Paperless-ngx document consumption triggers. When Paperless processes a document tagged as `#book` or `#manual`, it notifies Bookwarden to ingest, verify ISBN/author data, and stage or import into Calibre.

---

## 7. Books & Direct Inspection Endpoints (`/api/books`, `/api/inspect`)

### `GET /api/books`
Lists books from the active Calibre library with pagination, search, and sorting.
- **Query Parameters**:
  - `limit` (int, default 50, max 200).
  - `offset` (int, default 0).
  - `query` (string, optional): Full-text search across title and authors.

### `GET /api/books/{book_key}`
Fetches complete metadata, file formats, checksums, and forensic history for a single book.

### `GET /api/books/{book_key}/similar`
Returns books with similar conceptual topics using vector embeddings or keyword clustering.

### `GET /api/inspect/fs`
Inspects the library root directory structure, permission masks, and detected formats without connecting to Calibre.

### `POST /api/inspect/path`
Performs an isolated forensic scan on a specific ebook file path.

### `POST /api/inspect/upload`
Accepts a single uploaded ebook file (`multipart/form-data`) and runs an immediate, in-memory forensic parse.

---

## 8. Certificate A Enterprise Verification Endpoints (`/api/verify`, `/api/review/v2`)

These endpoints provide strict, cryptographic, immutable verification under **Certificate A** contracts.

### `POST /api/verify`
Submits an idempotent verification run.
- **Headers**:
  - `X-API-Key`: Required.
  - `Idempotency-Key`: 16–128 alphanumeric characters.
- **Body**:
```json
{
  "limit": 100,
  "use_ocr": true,
  "confirm_calibre_stopped": true
}
```
- **Response**: HTTP `202 Accepted` with `CertificateARunSummary`.

### `GET /api/verify/runs`
Lists historical Certificate A verification runs, newest first.

### `GET /api/verify/{run_id}`
Returns details of a run, including frozen book inventory and evidence IDs.

### `POST /api/verify/{run_id}/cancel`
Requests graceful fenced cancellation of a running verification run.

### `GET /api/review/v2`
Paginated search over cryptographically sealed V2 evidence packages.
- Filters: `run_id`, `state`, `tier`, `limit`, `offset`.

### `GET /api/review/v2/{evidence_id}`
Returns the verified, SHA-256 sealed evidence package. Returns HTTP `409 Conflict` if the cryptographic seal fails validation.

### `POST /api/apply`
**Retired**: In Certificate A mode, this endpoint returns HTTP `410 Gone`. Direct automated writes are strictly disabled under enterprise shadow contracts.

# Data Model: Calibre Bookwarden

Calibre Bookwarden uses a layered persistence architecture balancing cryptographic auditability, high-concurrency leases, and native SQLite interoperability.

---

## 1. System of Record & Relational Entities

Persistent models live in `src/calibre_ai_auditor/storage/models.py` and are managed via **Alembic** migrations (current head: `f4a2d6e8c013`):

### Core Verification & Audit Models
- **`VerificationRun`**: Orchestrates a batch audit execution under a frozen library membership.
  - `run_id`: Unique identifier (e.g. `verify_20260913_120000_a1b2c3d4`).
  - `contract_version`: Schema contract version (`v2`).
  - `release_digest`: Exact SHA-256 of the running container image (`sha256:...`).
  - `alembic_head`: Expected Alembic migration revision.
  - `library_root_sha256`: Digest of the library root path.
  - `owner_token` / `fence_token`: Monotonically increasing lease token for PostgreSQL `FOR UPDATE SKIP LOCKED` claims.
  - `status`: `pending`, `running`, `completed`, `failed`, `cancelled`.
- **`VerificationResult`**: Invariant verdict for one book in a verification run.
  - `book_key`: Canonical identifier (e.g. `calibre:123` or `calibre-server:<fp>:123`).
  - `tier`: Classification tier (`Tier A` = exact match, `Tier B` = review required, `Tier C` = conflict).
  - `action`: Recommended remediation action (`no_change`, `suggest_fix`, `needs_review`, `defer`).
  - `state`: Terminal state (`shadowed`, `review`, `deferred`, `source_changed`, `failed`).
  - `risk_flags`: Array of risk indicators (e.g. `author_swap`, `isbn_conflict`, `edition_ambiguous`).
  - `proposed_patch`: Sanitized proposed metadata patch in JSON format.
- **`EvidencePackage`**: Cryptographically sealed observations package.
  - `evidence_id`: Unique evidence identifier.
  - `package_sha256`: SHA-256 seal computed over canonicalized JSON observations.
  - `observations`: Strict V2 schema including `schema_version=2`, attached formats, hashes, OCR extractions, and provider records.
  - *(Note: `decision` remains NULL in V2 packages to prevent legacy V1 consumers from mistakenly executing them).*

### Supervised Mutation & Concurrency Control
- **`OperationLedger`**: Append-only state machine governing physical library mutations.
  - `operation_id`: UUIDv4 tracking a single mutation request.
  - `book_key`: Target Calibre book.
  - `state`: Durable state (`requested`, `claimed`, `writing`, `verifying`, `completed`, `failed`, `restoring`, `restored`).
  - `patch_sha256`: Hash of the exact authorized metadata patch.
  - `before_metadata` / `after_metadata`: JSON snapshots of Calibre metadata before and after mutation.
  - `backup_dir`: Path to rollback artifacts (`restore.json`, original files, OPF backup).
- **`PilotSession`**: Bounds automated operations to a strict human-supervised window.
  - `pilot_id`: Identifier for the active pilot session.
  - `max_operations`: Maximum operations permitted in this session (e.g. 5).
  - `consumed_operations`: Number of completed mutations.
  - `state`: `open` or `stopped`.
- **`ManualAuthorization`**: Human operator approval linking a specific `evidence_id` to a `patch_sha256`.
- **`BookWriteLock`**: Lease token enforcing single-writer exclusivity per `book_key`.
- **`OperationIncidentAcknowledgement`**: Immutable audit row acknowledging a terminal `failed` operation without erasing historical ledger records.
- **`OutboxEvent`**: Transactional outbox table ensuring reliable event publication.
- **`CoverVisionCache`**: Perceptual hash and CQS metric cache preventing redundant image re-evaluations.

### Historical / Compatibility Entities
- **`Run`**, **`BookRecord`**, **`Change`**: Maintained for backward-compatible v1.0 and legacy v0.9 audit logging.

---

## 2. File Formats & Checksum Representation

> [!NOTE]
> **Format Checksums Architecture:** Format checksums are **not stored in a separate relational table** named `book_format_checksums`. Instead, they are modeled and validated as structured Pydantic objects:
> 1. In `BookRecord.files`: JSON array of `BookFile` objects (`path`, `format`, `size_bytes`, `sha256`).
> 2. In `EvidencePackage.observations["formats"]`: List of `FormatEvidence` dictionaries, each containing a strictly validated 64-character lowercase hexadecimal SHA-256 string.

---

## 3. Calibre Native SQLite Schema (`metadata.db`)

When operating via `DirectCalibreEngine`, the engine queries and validates Calibre's internal SQLite database:

| Calibre Table | Fields Used by Bookwarden | Purpose |
|---|---|---|
| `books` | `id`, `title`, `sort`, `author_sort`, `has_cover`, `path` | Core book entity and title/author sort keys |
| `authors` | `id`, `name`, `sort`, `link` | Author authority database and normalized display |
| `books_authors_link` | `id`, `book`, `author` | Many-to-many junction table linking books to authors |
| `data` | `id`, `book`, `format`, `uncompressed_size`, `name` | Physical format files attached to each book |
| `identifiers` | `id`, `book`, `type`, `val` | External identifiers (`isbn`, `google`, `openlibrary`, `goodreads`) |
| `tags` / `books_tags_link` | `id`, `name` | Categorical tags, genres, and periodical markers |
| `ratings` / `books_ratings_link`| `id`, `rating` | 1-to-5 star ratings (stored as integers 0-10) |

### Bibliographic Author Authority Invariants
When executing `sync_all_author_sorts()`, the engine applies deterministic authority rules (`rules/authority.py`):
1. **Nobility & Surnames with Prefixes**: Particles (`von`, `van`, `de`, `de la`, `du`, `di`) are kept adjacent to the surname (e.g. *Johann Wolfgang von Goethe* $\rightarrow$ `von Goethe, Johann Wolfgang`).
2. **Religious & Classical Figures**: Epithets are preserved (e.g. *Thomas Aquinas* $\rightarrow$ `Thomas Aquinas, Saint`; *Pope John Paul II* $\rightarrow$ `John Paul II, Pope`).
3. **Compound Authors**: Multiple authors joined with ` & ` are sorted individually and rejoined (e.g. `Morison, Samuel Eliot & Commager, Henry Steele`).
4. **Corporate & Periodicals**: Preserved in natural reading order without inversion (e.g. *The Economist*, *Harvard Business Review*).

---

## 4. Artifact & Restore Point Layout

Safety backups and evidence files are organized in the configured `BOOKAUDIT_ARTIFACTS_DIR`:

```text
.artifacts/
├── restore/
│   └── <run_id>/
│       └── <book_key>/
│           ├── restore.json          # Machine-readable rollback manifest with file hashes
│           ├── metadata.opf.bak      # Pre-mutation Calibre OPF export
│           └── cover_original.jpg    # Pre-mutation cover image backup
├── covers/                           # Extracted & normalized cover candidates
├── paperless_imports/                # Downloaded candidate documents from Paperless-ngx
└── reports/                          # Generated JSON/PDF 360-degree audit reports
```

# Database Configuration

The application supports both SQLite (default) and PostgreSQL for data storage.

## SQLite (Default)

SQLite is the simplest way to run the application locally. Data is stored in a single file within the `.state/` directory.

**Configuration:**
```yaml
database:
  backend: "sqlite"
  sqlite_path: "/state/bookaudit.db"
```

## PostgreSQL

For production or homelab deployments requiring high availability and concurrency, PostgreSQL is the recommended backend.

**Configuration:**
```yaml
database:
  backend: "postgres"
  postgres_dsn: "postgresql+psycopg://user:password@host:5432/dbname"
```

### Docker Deployment with PostgreSQL

When using `docker compose up`, a PostgreSQL service is started and the `app` service is configured via `BOOKAUDIT_DATABASE__BACKEND=postgres` to use it as the system-of-record.

**Environment Variables:**
*   `BOOKAUDIT_DATABASE__BACKEND=postgres`
*   `BOOKAUDIT_DATABASE__POSTGRES_DSN=postgresql+psycopg://bookaudit:bookaudit@postgres:5432/bookaudit`

## System-of-Record

Regardless of the backend, the database serves as the system-of-record for:
- Scan and Audit runs
- Discovered book records
- Evidence packages
- Resolved metadata and proposed patches
- Audit log of applied changes and undo events

## Manifestation V2 records

Alembic head `f4a2d6e8c013` (advancing past `c8e1f0a2b4d6` worker leases and `e3c1a4b7d902` verification contracts) establishes full runtime role isolation between `bookaudit_app`, `bookaudit_verifier`, and `bookaudit_migrator`. The contract maintains strict invariant tracking:

- `VerificationRun.pipeline_version` and `mode` distinguish V1/legacy from
  `manifestation-v2` shadow runs.
- `VerificationResult.evidence_id` and `state` persist one book's resumable state
  and checksummed-package link.
- `EvidencePackage.schema_version=2` stores the complete checksummed package in
  `observations`; `decision` remains null to prevent accidental legacy apply.
- `OperationLedger.evidence_id` binds an authorized writer operation to the
  exact package; `pilot_id` binds V2 writes to one persisted pilot session.
- `PilotSession` stores the immutable library-root hash, release digest,
  Alembic revision, max-five reservation budget and open/stopped state. The app
  role can create/update it; the writer can only read it. Both queueing and the
  writer compare its reservation counter with the count of immutable
  pilot-bound operations, so resetting the app-writable counter fails closed.
- `OperationIncidentAcknowledgement` is keyed by the exact operation ID and is
  append-only at the PostgreSQL ACL boundary: the app may insert/select, while
  neither runtime role may update or delete it. It records operator, reason and
  timestamp for a reconciled terminal V2 `failed` operation only after the
  operation's exact `PilotSession` is stopped under lock, without rewriting
  ledger or outbox history or reopening that ID. Queue and metrics rederive
  validity from the linked operation, failed outbox, stopped pilot and absent
  book lease; acknowledgement-row presence alone grants nothing.
- `Change` and `OperationLedger` retain cover/custom-column backup paths, hashes,
  and rollback values for apply, undo, and crash reconciliation.

Run migrations explicitly with `bookaudit migrate` or the Compose maintenance
profile. For a Compose PostgreSQL deployment, run the idempotent
`provision-roles` maintenance task before `migrate`; direct CLI migration
assumes the release's database roles already exist. Runtime services do not
provision roles or migrate automatically. The migration suite tests a clean
upgrade/downgrade, an existing-volume role upgrade, and upgrade of legacy
verification rows.
Downgrade is intentionally refused when V2 audit evidence or V2
writer/recovery records exist, because dropping those columns would destroy the
ability to audit or recover operations. The package SHA-256 is an integrity
checksum inside the trusted database boundary, not an administrator signature.

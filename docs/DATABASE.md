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

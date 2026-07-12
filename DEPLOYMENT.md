# Deployment Guide

`calibre-ai-auditor` is designed to be deployed as a multi-service containerized stack.

## 1. Multi-Stage Docker Build

The application uses a two-stage build process:
1.  **Frontend Builder**: Compiles the React + Vite SPA.
2.  **Runtime**: A Python-based container that serves the static assets and the FastAPI backend.

**Image**: `${BOOKAUDIT_IMAGE:-calibre-ai-auditor:1.2.0}`

---

## 2. Service Stack (Docker Compose)

We use **Docker Compose Profiles** to manage complexity.

### Optional integrations
The default production stack starts only the required services. The `optional`
profile adds specialized sidecars:
- **`app`**: FastAPI Backend + React WebUI.
- **`postgres`**: Authoritative single-host database.
- **`valkey`**: Task queue and rate-limit caching.
- **`tika`**: Advanced document extraction sidecar.
- **`gotenberg`**: PDF preview/report renderer.
- **`qdrant`**: Semantic duplicate detection.

### Deployment commands
```bash
cp .env.example .env
# Replace every placeholder and synchronize each DSN password with the matching
# POSTGRES_*_PASSWORD value.
docker compose build app
docker compose up -d postgres valkey
docker compose --profile maintenance run --rm migrate
docker compose up -d app writer
```

On the first empty PostgreSQL volume, `scripts/postgres-init-roles.sh` creates
the app, writer, and migrator roles. The migrator owns the schema; runtime roles
receive DML and sequence privileges but cannot create schema objects. Password
rotation is an explicit operation because PostgreSQL init hooks run only for an
empty data directory.

---

## 3. Persistent Volumes

| Volume | Mount Point | Purpose |
|---|---|---|
| `./fake_library` | `/library` | The Calibre library to audit. |
| `./.state` | `/state` | Database files and app state. |
| `./.artifacts` | `/artifacts` | API audit artifacts (never writer restore evidence). |
| `./.writer-artifacts` | `/writer-artifacts` | Writer-exclusive OPF targets and restore evidence. |
| `./config` | `/app/config` | YAML configuration overrides. |

---

## 4. Port Mappings

| Service | Host Port | Container Port |
|---|---|---|
| **App (WebUI/API)** | **8080** | 8080 |
| **PostgreSQL** | not published | 5432 |
| **Valkey** | not published | 6379 |
| **Qdrant** | not published | 6333 |
| **Tika** | not published | 9998 |
| **Gotenberg** | not published | 3000 |

---

## 5. Environment Configuration (`.env`)

Critical variables for deployment:

*   `BOOKAUDIT_READ_ONLY`: Defaults to `true`. Protects your library (also `BOOKAUDIT_LIBRARY__READ_ONLY`).
*   `OLLAMA_BASE_URL`: Pointer to your homelab Ollama instance.
*   `BOOKAUDIT_JUDGE_MODEL`: The LLM to use for auditing (e.g., `qwen3.5:9b-q4_K_M`).
*   `OPENAI_API_KEY`: Required only for remote deep reasoning.
*   `BOOKAUDIT_APP_POSTGRES_DSN`, `BOOKAUDIT_WRITER_POSTGRES_DSN`, and
    `BOOKAUDIT_MIGRATOR_POSTGRES_DSN`: distinct database roles. Their passwords
    must match `POSTGRES_APP_PASSWORD`, `POSTGRES_WRITER_PASSWORD`, and
    `POSTGRES_MIGRATOR_PASSWORD`; do not reuse the migrator credential at runtime.

---

## 6. Operational Rules

1.  **Safety First**: Always mount your library as `:ro` (read-only) unless you are performing an active `apply` operation.
2.  **Backups**: Back up `.writer-artifacts`; only the writer may mount this directory read-write.
3.  **Permissions**: Containers run as the host's UID/GID (defined in `.env`) to prevent "root-owned" file issues on your host filesystem.
# Production v2 network boundary

The production Compose profile binds the API only to `127.0.0.1`. Terminate
TLS in a reverse proxy on the same host and proxy to
`http://127.0.0.1:${BOOKAUDIT_PORT:-8080}`. Do not publish the application port
directly on the LAN. PostgreSQL, Valkey, and optional sidecars are reachable
only on the Compose network.

Set `BOOKAUDIT_LIBRARY_HOST_PATH` to an absolute Calibre library path. The
read-only gate always mounts it as `/library:ro`; no environment option can
turn that mount read-write. Copy `.env.example` to `.env`, replace every
placeholder secret, and keep `.env` outside version control.

Schema upgrades are an explicit maintenance action. Stop `app` and `writer`,
take a PostgreSQL plus `.writer-artifacts` backup, run the one-shot `migrate`
service, then start the runtime roles. Runtime services never auto-migrate.

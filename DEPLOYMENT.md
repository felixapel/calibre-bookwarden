# Deployment Guide

`calibre-ai-auditor` is designed to be deployed as a multi-service containerized stack.

## 1. Multi-Stage Docker Build

The application uses a multi-stage build process:
1.  **Frontend Builder**: Compiles the React + Vite SPA.
2.  **Runtime**: A Python-based container that serves the static assets and the FastAPI backend.

**Image**: `${BOOKAUDIT_IMAGE:-calibre-ai-auditor:1.2.1}`

Release tags matching `v<pyproject version>` publish a signed GHCR image with
provenance and SPDX SBOM. For production, set `BOOKAUDIT_IMAGE` to the immutable
`ghcr.io/<owner>/<repo>@sha256:<digest>` printed by the release workflow. The
local version tag is intended for build-and-test deployments only.

GitHub Actions is the canonical release authority and the only workflow that
publishes production images. Gitea Actions gates homelab branches and pull
requests but does not publish or sign releases.

The runtime layer installs Debian's Calibre package, so rebuilding the same
commit can resolve different OS package versions. The release contract is the
single digest that is scanned, boot-tested, attested, signed, and only then
tagged—not bit-for-bit reproducibility across later rebuilds. Vendor-unfixed
HIGH/CRITICAL findings have a fail-closed review expiry in the release workflow;
fixed findings always block publication.

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
./scripts/prepare-production.sh
docker compose build app
docker compose up -d --wait postgres valkey
docker compose --profile maintenance run --rm migrate
docker compose up -d app writer
```

On the first empty PostgreSQL volume, `scripts/postgres-init-roles.sh` creates
the app, writer, and migrator roles. The migrator owns the schema; each runtime
role receives only the table-specific DML needed by its API or writer duties and
cannot create schema objects. Password
rotation is an explicit operation because PostgreSQL init hooks run only for an
empty data directory.

---

## 3. Persistent Volumes

| Volume | Mount Point | Purpose |
|---|---|---|
| `${BOOKAUDIT_LIBRARY_HOST_PATH}` | `/library` | Real Calibre library; app mounts read-only and the sole writer mounts read-write. |
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
*   `BOOKAUDIT_TRUSTED_HOSTS`: comma-separated public hostnames accepted from
    the reverse proxy. Add the production DNS name before exposing the proxy.
*   `BOOKAUDIT_APP_POSTGRES_DSN`, `BOOKAUDIT_WRITER_POSTGRES_DSN`, and
    `BOOKAUDIT_MIGRATOR_POSTGRES_DSN`: distinct database roles. Their passwords
    must match `POSTGRES_APP_PASSWORD`, `POSTGRES_WRITER_PASSWORD`, and
    `POSTGRES_MIGRATOR_PASSWORD`; do not reuse the migrator credential at runtime.

---

## 6. Operational Rules

1.  **Safety First**: The app always mounts the library read-only. Only the
    dedicated single-writer service receives the read-write mount; stop it when
    no approved apply/undo operation should be possible.
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

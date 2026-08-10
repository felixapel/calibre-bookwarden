# Certificate A deployment

The supported deployment is the four-service Certificate A Compose graph:

| Service | Authority |
|---|---|
| `app` | Loopback WebUI/API; request insert and cancellation only; no library mount |
| `verifier` | `/library:ro`; fenced evidence/progress writer; private tmpfs scratch |
| `postgres` | Authoritative queue, contracts, progress, and sealed evidence |
| `valkey` | Rate limits and verifier heartbeat only |

`provision-roles` and `migrate` are explicit one-shot maintenance tasks.
`writer` and `writer-maintenance` are quarantined Certificate B profiles and
must not be started for Certificate A.

## Images

The default/final Docker target is `certificate-a`. It includes the compiled
WebUI, production-only CLI, Tesseract/OCR dependencies, PostgreSQL/Valkey
clients, and migrations. It excludes Calibre, LLM SDKs, Qdrant, MCP, watcher,
upload, and legacy UI dependencies.

The `writer` target is a different image with checksum-pinned Calibre and
legacy extras. Its presence in the build file is not production approval.

Production must set `BOOKAUDIT_IMAGE` to an immutable registry digest and set
`BOOKAUDIT_RELEASE_DIGEST` to the identical `sha256:...` value. Mutable tags and
`BOOKAUDIT_ALLOW_LOCAL_IMAGE=true` are disposable validation only.

## Deploy

```bash
cp .env.example .env
chmod 600 .env
# Replace every placeholder and use distinct database role passwords.
# Keep COMPOSE_PROJECT_NAME=bookaudit-certificate-a.
./scripts/prepare-production.sh

docker compose pull app verifier postgres valkey
docker compose up -d --wait postgres valkey
docker compose --profile maintenance run --rm provision-roles
docker compose --profile maintenance run --rm migrate
docker compose up -d --wait verifier app
```

`provision-roles` is idempotent and must run before migration on fresh and
existing PostgreSQL volumes. The initdb hook alone cannot upgrade existing
volumes. Preflight verifies the exact default graph and rejects writer flags.
Runtime services validate but never provision roles or migrate the schema.

`bookaudit-certificate-a` is a dedicated Compose project. Do not reuse the
legacy `calibre-ai-auditor` project: it can contain a quarantined writer and
legacy volumes. Preflight rejects that name and refuses any unexpected service
already attached to the dedicated Certificate A project.

Only the app port is published, and only on
`127.0.0.1:${BOOKAUDIT_PORT:-8080}`. PostgreSQL and Valkey have no host ports.
Install the same-host Caddy edge from `deploy/caddy/Caddyfile.example` after
loopback readiness passes. Whole-site authentication has no path matcher.

## Persistent state

- `postgres-data`: authoritative durable state; back up with `pg_dump -Fc`.
- `valkey-data`: operational rate-limit/heartbeat state; not authoritative.
- `${BOOKAUDIT_LIBRARY_HOST_PATH}`: existing stopped Calibre library, mounted
  only into the verifier and only read-only.
- `./config`: application configuration, mounted read-only.
- `/scratch`: verifier tmpfs; never persisted.

Certificate A does not use `.state`, `.artifacts`, `.writer-artifacts`, upload
folders, or a writable library bind mount.

## Security properties

- Containers run read-only, without Linux capabilities, with
  `no-new-privileges`, PID/memory/CPU limits, and non-root identities.
- PostgreSQL roles are provisioned before Alembic applies the exact table and
  column ACLs. The app cannot forge evidence; the verifier cannot use writer
  ledgers or migrate schema.
- The verifier heartbeat and readiness bind the image release, Alembic head,
  and library-root identity.
- API authentication, trusted hosts, Valkey-backed rate limiting, security
  headers, and Caddy TLS/auth all fail closed.

Use [docs/runbooks/production-operations.md](docs/runbooks/production-operations.md)
for first deployment, backup, restore drill, upgrade, rollback, monitoring, and
incident response. Use
[docs/production-readiness.md](docs/production-readiness.md) for exact-commit
promotion evidence.

# Certificate A deployment

> [!NOTE]
> **Homelab Users:** This document describes the formal enterprise Certificate A deployment boundary (read-only stopped library, PostgreSQL, Valkey, and isolated verification workers). For daily homelab companion setups with `calibre-web-automated`, UnRAID, or TrueNAS, see [docs/HOMELAB.md](docs/HOMELAB.md) and [docker-compose.sidecar.yml](docker-compose.sidecar.yml).

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
`BOOKAUDIT_ALLOW_LOCAL_IMAGE=true` are disposable validation only. Build both
release targets with `--build-arg BOOKAUDIT_BUILD_REVISION=$(git rev-parse
HEAD)`, then set `BOOKAUDIT_SOURCE_REVISION` to that exact 40-character commit.
Preflight requires the reviewed checkout and the image's
`org.opencontainers.image.revision` label to match it.

## Deploy

```bash
cp .env.example .env
chmod 600 .env
# Replace every placeholder and use distinct database role passwords.
# Keep COMPOSE_PROJECT_NAME=bookaudit-certificate-a.
./scripts/certificate-a-compose.sh pull app verifier postgres valkey
./scripts/certificate-a-compose.sh --profile monitoring pull prometheus
./scripts/certificate-a-compose.sh --profile edge pull caddy
./scripts/prepare-production.sh
./scripts/certificate-a-compose.sh up -d --wait postgres valkey
./scripts/certificate-a-compose.sh --profile maintenance run --rm provision-roles
./scripts/certificate-a-compose.sh --profile maintenance run --rm migrate
./scripts/certificate-a-compose.sh up -d --wait verifier app
```

`provision-roles` is idempotent and must run before migration on fresh and
existing PostgreSQL volumes. The initdb hook alone cannot upgrade existing
volumes. Preflight verifies the exact default graph and rejects writer flags.
Runtime services validate but never provision roles or migrate the schema.

`bookaudit-certificate-a` is a dedicated Compose project. Do not reuse the
legacy `calibre-ai-auditor` project: it can contain a quarantined writer and
legacy volumes. Preflight rejects that name and refuses any unexpected service
already attached to the dedicated Certificate A project. Every Certificate A
command uses `scripts/certificate-a-compose.sh`, which pins the project,
Compose file, environment file, and only permitted maintenance profile instead
of trusting ambient Compose variables.

Only the app port is published on `127.0.0.1:${BOOKAUDIT_PORT:-8080}`, along with Prometheus metrics on `127.0.0.1:${BOOKAUDIT_PROMETHEUS_PORT:-19090}`. PostgreSQL and Valkey have no host ports.
The opt-in `edge` profile runs the pinned Caddy image after loopback readiness
passes. Docker publishes its ports only on `BOOKAUDIT_EDGE_BIND_IP`, which must
be this host's Tailscale IPv4 address; Caddy reaches the app only through the
private Compose network and obtains the exact `*.ts.net` certificate from the
local Tailscale daemon. Whole-site authentication has no path matcher.
`BOOKAUDIT_EDGE_IMAGE` must be the digest-pinned output of the reviewed
`caddy-edge` target and carry the same source-revision label as the app image.
The Caddy process uses the deployment UID rather than root. Grant only that UID
certificate access with `TS_PERMIT_CERT_UID` in `/etc/default/tailscaled`, then
restart `tailscaled`; do not grant the broader Tailscale operator permission.

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

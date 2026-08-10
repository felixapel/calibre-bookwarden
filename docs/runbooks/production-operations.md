# Certificate A production operations

This runbook applies only to the read-only Certificate A stack. Commands that
start `writer` or `writer-maintenance` are intentionally absent.

## Before every audit

1. Stop Calibre, Calibre Content Server, sync tools, backup jobs, and any other
   process that can change the library.
2. Confirm no `metadata.db-wal`, `metadata.db-shm`, or `metadata.db-journal`
   file exists. Do not delete a sidecar; its presence means the source is not a
   safe stopped-library snapshot.
3. Confirm the configured library path is the intended library. The verifier
   binds its heartbeat and every request to the canonical root hash.
4. Use the WebUI checkbox to make the explicit stopped-Calibre assertion. It is
   an operator safety statement, not an automated process detector.

If Calibre starts or any source file changes during a run, cancel the run and
leave it for evidence review. Never force it back to `pending`.

## First deployment

Create a mode-0600 environment file. Use five distinct PostgreSQL passwords, a
strong internal API key, explicit trusted hosts, and a Certificate A image
pinned by digest. `BOOKAUDIT_RELEASE_DIGEST` must be the same digest.

```bash
cp .env.example .env
chmod 600 .env
# Edit .env and replace every placeholder.
# Keep COMPOSE_PROJECT_NAME=bookaudit-certificate-a.
./scripts/prepare-production.sh
```

Preflight validates the secret relationships, image/release binding, existing
absolute library, dedicated Compose project, and exact default service graph.
It rejects the legacy `calibre-ai-auditor` project name, any unexpected service
already attached to the Certificate A project, auto-apply, and an enabled
writer pilot.

Pull or build the reviewed image, then start infrastructure, provision roles,
migrate once, and start the verifier before the app:

```bash
./scripts/certificate-a-compose.sh pull app verifier postgres valkey
./scripts/certificate-a-compose.sh up -d --wait postgres valkey
./scripts/certificate-a-compose.sh --profile maintenance run --rm provision-roles
./scripts/certificate-a-compose.sh --profile maintenance run --rm migrate
./scripts/certificate-a-compose.sh up -d --wait verifier app
```

`provision-roles` is an idempotent maintenance task, not a runtime service. Run
it before every reviewed migration so an existing PostgreSQL volume receives
new release roles and the passwords declared in `.env`. The initdb copy of the
same script runs only when PostgreSQL creates an empty data directory.

Use `scripts/certificate-a-compose.sh` for every Certificate A operation. It
pins the dedicated project plus the reviewed Compose and environment files,
verifies the existing project's service and Compose-file provenance, runs
Compose with only Docker transport variables inherited from the shell, and
rejects writer profiles, scaling, or identity/file overrides.
Commands that execute inside an existing container also require its Compose
configuration hash to match the service resolved from the reviewed `.env`.

For a local candidate build, `BOOKAUDIT_ALLOW_LOCAL_IMAGE=true` is permitted
only for disposable validation. It is not production promotion evidence.

Check readiness without placing the API key in the host process list:

```bash
./scripts/certificate-a-compose.sh exec -T app sh -c \
  'curl --fail --silent --show-error -H "X-API-Key: $BOOKAUDIT_API_KEY" \
  http://127.0.0.1:8080/api/health/ready'
```

The response must be exactly a ready Certificate A response. A 503 includes a
sanitized code: `configuration_invalid`, `database_unavailable`, or
`verifier_unavailable`.

## Same-host TLS and whole-site authentication

The backend publishes only `127.0.0.1:${BOOKAUDIT_PORT}`. Run Caddy on the same
host; never change the binding to `0.0.0.0` or a LAN address.

1. Generate a password hash with `caddy hash-password`. Do not put a plaintext
   password in the Caddyfile or shell history.
2. Install `deploy/caddy/Caddyfile.example` as the reviewed Caddyfile.
3. Provide these variables to the Caddy system service through a root-readable
   environment file: `BOOKAUDIT_DOMAIN`, `BOOKAUDIT_BASIC_AUTH_USER`,
   `BOOKAUDIT_BASIC_AUTH_HASH`, `BOOKAUDIT_PORT`, and
   `BOOKAUDIT_ACME_EMAIL`.
4. Validate before reload:

   ```bash
   sudo caddy validate --config /etc/caddy/Caddyfile
   sudo systemctl reload caddy
   ```

5. From another private/VPN host, verify an unauthenticated request is rejected,
   authenticate in a browser, and confirm Overview, Verify, Evidence, and API
   requests use trusted HTTPS. Enter the separate internal API key in the WebUI
   prompt; it is held only in page memory and must be entered again after reload.

The `basic_auth` directive has no path matcher, so it covers health, metrics,
assets, API routes, and future paths. The stored password must be a supported
hash. See the official [Caddy basic authentication](https://caddyserver.com/docs/caddyfile/directives/basic_auth)
and [reverse proxy](https://caddyserver.com/docs/caddyfile/directives/reverse_proxy)
references.

## Normal audit operation

Start a run from Verify with a positive optional book limit, bounded OCR choice,
and the stopped-Calibre confirmation. Only one active request per source root is
admitted.

Expected states:

| State | Operator meaning |
|---|---|
| `pending` | Persisted, waiting for a verifier claim |
| `inventorying` | Verifier owns a live fenced lease and freezes membership |
| `running` | Frozen books are being audited |
| `cancelling` | Cancellation was persisted; worker will stop at a safe boundary |
| `cancelled` | Terminal operator cancellation |
| `completed` | All selected books have sealed terminal evidence |
| `completed_with_errors` | Terminal; inspect per-book failures |
| `failed` | Terminal worker failure; investigate before another representative run |
| `source_changed` | Source drift detected; keep Calibre stopped and investigate |
| `blocked_recovery` | Persisted contract cannot safely resume on this release |

The total is intentionally unknown until inventory completes. A verifier crash
does not authorize an immediate second worker write: the lease must expire, the
recovery verifier increments the fence, and stale writes are rejected.

Use the WebUI Cancel action. Do not edit lease, fence, status, or evidence rows
directly. Preserve logs using request IDs; logs and metrics deliberately omit
library paths, API keys, run IDs as metric labels, and book metadata.

## Backup

PostgreSQL is authoritative. Valkey contains rate-limit and heartbeat state and
does not need to be restored for correctness.

Wait for every run to become terminal, then stop intake and the verifier:

```bash
./scripts/certificate-a-compose.sh stop app verifier
backup_dir="${BOOKAUDIT_BACKUP_HOST_PATH:-./backups}/$(date -u +%Y%m%dT%H%M%SZ)-certificate-a"
install -d -m 0700 "$backup_dir"
./scripts/certificate-a-compose.sh exec -T postgres \
  pg_dump -U bookaudit -d bookaudit -Fc > "$backup_dir/bookaudit.dump"
chmod 600 "$backup_dir/bookaudit.dump"
(cd "$backup_dir" && sha256sum bookaudit.dump > SHA256SUMS)
chmod 600 "$backup_dir/SHA256SUMS"
./scripts/certificate-a-compose.sh up -d --wait verifier app
```

Copy the dump and checksum together to protected backup storage. Record the
exact image digest and Alembic revision in the operator release record, not in a
file containing secrets.

## Empty-environment restore drill

Never prove restore by overwriting the production database. Use a clearly named
disposable Compose project on the same reviewed commit:

```bash
./scripts/certificate-a-compose.sh --restore-drill up -d --wait postgres
(cd /protected/path/to/backup && sha256sum --check SHA256SUMS)
./scripts/certificate-a-compose.sh --restore-drill exec -T postgres \
  pg_restore -U bookaudit -d bookaudit --clean --if-exists < \
  /protected/path/to/backup/bookaudit.dump
./scripts/certificate-a-compose.sh --restore-drill exec -T postgres \
  psql -U bookaudit -d bookaudit -Atc 'SELECT version_num FROM alembic_version'
```

Require the expected revision and inspect aggregate run counts. Do not start the
verifier in the restore drill with a live library path. After evidence is
recorded, remove only the explicitly named disposable project:

```bash
./scripts/certificate-a-compose.sh --restore-drill down --volumes
```

## Upgrade

1. Require a green automatic Gitea run and clean image scans for the exact
   commit. Verify the immutable Certificate A digest in the registry.
2. Wait for terminal runs. Stop `app` and `verifier` and take the backup above.
3. Retain the previous image digest. Update `.env`, keeping the image and
   release digest identical, then rerun preflight.
4. Pull the image. Provision roles idempotently, then execute the migration
   profile exactly once:

   ```bash
   ./scripts/certificate-a-compose.sh --profile maintenance run --rm provision-roles
   ./scripts/certificate-a-compose.sh --profile maintenance run --rm migrate
   ```

5. Start `verifier`, then `app`; require authenticated readiness and the Caddy
   HTTPS check.
6. Run one small stopped-library audit before returning to normal limits.

Runtime app and verifier processes never run Alembic.

## Rollback

If the release notes explicitly state that the previous image supports the new
schema, stop runtime services and restore the previous digest. Otherwise,
restore the pre-upgrade dump into a new empty environment and promote that
restored database through the host's documented database procedure. Do not run
an ad hoc Alembic downgrade on production.

Keep the source stopped throughout rollback. An image/schema mismatch causes
readiness or verifier startup to fail and must not be bypassed.

## Monitoring alerts

`ops/monitoring/prometheus.yml` assumes Prometheus runs on the same host and
scrapes `127.0.0.1:8080`. Store only the internal API key, with no variable name,
in `/etc/prometheus/secrets/bookaudit_api_key`, owned by the Prometheus account
and unreadable by other users. The `http_headers.files` setting reads it from
that protected file; see the official [Prometheus configuration reference](https://prometheus.io/docs/prometheus/latest/configuration/configuration/).

Load `ops/monitoring/alerts.yml` and route at least these alerts:

- `BookAuditUnavailable`: backend cannot be scraped.
- `BookAuditCertificateANotReady`: database, config, or verifier binding failed.
- `BookAuditVerifierHeartbeatStale`: verifier stopped or differs by release,
  schema, or library root.
- `BookAuditCertificateAMetricsCollectionFailed`: heartbeat or PostgreSQL run
  metrics failed.
- `BookAuditCertificateARunStalled`: an active run has no durable heartbeat for
  more than ten minutes.
- `BookAuditCertificateARunFailed`: review terminal failed/source-change/
  blocked-recovery evidence.
- HTTP server-error and latency alerts.

Prometheus itself should not pass through Caddy; it scrapes the loopback backend
with the internal API key. Browser users still go through Caddy authentication.

## Incident response

- **Verifier stale:** stop new requests, inspect the verifier container and its
  release/schema/root binding, then restart only the same reviewed image.
- **Source changed:** keep Calibre stopped, preserve the run and logs, determine
  what changed, and start a new run only after the source is stable. Do not
  overwrite the terminal state.
- **Blocked recovery:** preserve PostgreSQL and logs. Compare the persisted
  release/schema/root contract with the deployed image. Restore the matching
  image or escalate; never loosen contract validation.
- **Database unavailable or ACL denial:** stop runtime services, verify the
  exact role DSNs and Alembic head, and rerun the ACL test against a disposable
  database—not production.
- **Low disk:** cancel work and stop the verifier. Do not delete evidence rows or
  PostgreSQL files manually.
- **Suspected credential exposure:** stop the edge, rotate Caddy credentials,
  API key, and affected database role password, update mode-0600 files, recreate
  services, and verify readiness.

Certificate A never requires touching a book file to recover an incident.

## Certificate B prohibition

Do not start the `writer` or `writer-maintenance` profile, set `read_only=false`,
enable auto-apply, or enable the supervised pilot under this runbook. Those
actions belong to a future Certificate B release with separate approval and
restored-clone write drills. Certificate A preflight rejects the writer flags;
do not bypass it.

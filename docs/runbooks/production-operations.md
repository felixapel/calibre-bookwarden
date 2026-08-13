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
pinned by digest. `BOOKAUDIT_RELEASE_DIGEST` must be the same digest and
`BOOKAUDIT_SOURCE_REVISION` must be the exact commit embedded in the image's
`org.opencontainers.image.revision` label. `BOOKAUDIT_EDGE_IMAGE` is a separate
digest-pinned image built by the reviewed `caddy-edge` target with the same
revision label. Its committed Go module lock carries patched dependencies even
when the latest upstream Caddy image has known fixed HIGH vulnerabilities.

For the private HTTPS edge, enable Tailscale HTTPS for the tailnet, then grant
only the numeric deployment UID certificate access. Add (or update) this line
in `/etc/default/tailscaled`, restart the daemon, and do not grant the broader
Tailscale operator permission:

```bash
TS_PERMIT_CERT_UID=1000
sudo systemctl restart tailscaled
edge_image="$(python -c 'import runpy; from pathlib import Path; print(runpy.run_path("scripts/validate-production-env.py")["load_env"](Path(".env"))["BOOKAUDIT_EDGE_IMAGE"])')"
docker run --rm -it \
  "$edge_image" hash-password
```

Run `./scripts/prepare-production.sh` only after the restart. Its final
Tailscale probe can issue or renew the certificate and therefore is the
promotion gate, not a harmless dry-run. If it reports `cert access denied`, do
not start Caddy; fix the UID grant and repeat the probe as the configured
deployment user.

Set `BOOKAUDIT_DOMAIN` to this node's exact `*.ts.net` MagicDNS name,
`BOOKAUDIT_EDGE_BIND_IP` to its Tailscale IPv4 address, and put the generated
bcrypt hash in single quotes in `BOOKAUDIT_BASIC_AUTH_HASH`. Include the domain
in `BOOKAUDIT_TRUSTED_HOSTS`. The certificate name is published in Certificate
Transparency, while service access remains restricted by the tailnet.

```bash
cp .env.example .env
chmod 600 .env
# Edit .env and replace every placeholder.
# Keep COMPOSE_PROJECT_NAME=bookaudit-certificate-a.
./scripts/certificate-a-compose.sh pull app verifier postgres valkey
./scripts/certificate-a-compose.sh --profile monitoring pull prometheus
./scripts/certificate-a-compose.sh --profile edge pull caddy
./scripts/prepare-production.sh
```

Preflight validates the secret relationships, image/release/commit binding,
existing absolute library, dedicated Compose project, and exact default service graph.
It rejects the legacy `calibre-ai-auditor` project name, any unexpected service
already attached to the Certificate A project, auto-apply, and an enabled
writer pilot.

Start infrastructure, provision roles, migrate once, and start the verifier
before the app:

```bash
./scripts/certificate-a-compose.sh up -d --wait postgres valkey
./scripts/certificate-a-compose.sh --profile maintenance run --rm provision-roles
./scripts/certificate-a-compose.sh --profile maintenance run --rm migrate
./scripts/certificate-a-compose.sh up -d --wait verifier app
./scripts/certificate-a-compose.sh --profile monitoring up -d --no-deps --wait app
./scripts/certificate-a-compose.sh --profile monitoring up -d --no-deps --wait prometheus
./scripts/certificate-a-compose.sh --profile edge up -d --no-deps --wait caddy
```

Docker publishes edge ports 80/443 solely on the configured Tailscale address,
not LAN or all interfaces. Caddy reaches `app:8080` only on the private Compose
network and has no host-network access. An unauthenticated request to every path
must return 401 before promotion; authenticate in a browser and confirm the
certificate is trusted.

The application and edge image revisions must match the exact commit whose
automatic Gitea run is green. A documentation-only commit changes that
revision binding: rebuild and republish both images, update `.env` by digest,
and wait for the new exact-commit run before promotion.

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
Production volume removal is prohibited; `down --volumes` is accepted only for
the separately named disposable restore-drill project.

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

The backend publishes only `127.0.0.1:${BOOKAUDIT_PORT}`. The canonical edge is
the pinned Compose `edge` profile above; a host Caddy/systemd deployment is not
supported. Never change the binding to `0.0.0.0` or a LAN address. From another
tailnet host, verify an unauthenticated request is rejected, authenticate in a
browser, and confirm Overview, Verify, Evidence, and API requests use trusted
HTTPS. Enter the separate internal API key in the WebUI prompt; it is held only
in page memory and must be entered again after reload.

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
   release digest identical and setting the matching source revision, then
   rerun preflight.
4. Pull the image. Provision roles idempotently, then execute the migration
   profile exactly once:

   ```bash
   ./scripts/certificate-a-compose.sh --profile maintenance run --rm provision-roles
   ./scripts/certificate-a-compose.sh --profile maintenance run --rm migrate
   ```

5. Start `verifier`, then `app`; require authenticated readiness and the Caddy
   HTTPS check. Reconcile Caddy separately with `--profile edge up -d --no-deps
   --wait caddy` so an edge operation cannot recreate stateful dependencies.
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

`./scripts/prepare-production.sh` creates `.monitoring/bookaudit_api_key` from
the validated deployment environment with mode `0600`; it contains only the
internal API key and is ignored by Git. The opt-in `monitoring` profile mounts
that value as `/run/secrets/bookaudit_api_key`, persists its TSDB only in the
dedicated `.monitoring/data` directory, and scrapes `app:8080` over an internal
network that is not connected to PostgreSQL or Valkey. Its UI is loopback-only at
`http://127.0.0.1:${BOOKAUDIT_PROMETHEUS_PORT:-19090}`. The
`http_headers.files` setting reads the protected secret; see the official
[Prometheus configuration reference](https://prometheus.io/docs/prometheus/latest/configuration/configuration/).

Start or reconcile only this project's collector with:

```bash
./scripts/certificate-a-compose.sh --profile monitoring up -d --no-deps --wait app
./scripts/certificate-a-compose.sh --profile monitoring up -d --no-deps --wait prometheus
```

The first command safely recreates only the app when needed to attach its
dedicated monitoring bridge. IP masquerading is disabled on that bridge so
Prometheus cannot use it for external egress, while Docker can still publish
the UI on loopback. To roll back the collector, do not use `down`:

```bash
./scripts/certificate-a-compose.sh --profile monitoring stop prometheus
./scripts/certificate-a-compose.sh --profile monitoring rm --force prometheus
```

Keep `.monitoring/data` for diagnosis; deleting it requires separate
destructive authorization.

Verify the live target and loaded rules without exposing the API key:

```bash
curl --fail --silent --show-error \
  'http://127.0.0.1:19090/api/v1/query?query=up%7Bjob%3D%22bookaudit-certificate-a%22%7D'
curl --fail --silent --show-error \
  'http://127.0.0.1:19090/api/v1/rules'
```

Prometheus loads and evaluates at least these alerts from
`ops/monitoring/alerts.yml`:

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

Prometheus itself should not pass through Caddy; it scrapes the app only over
the private Compose network with the internal API key. Browser users still go
through Caddy authentication.

The current application has one API key scope, so the collector's secret can
also authorize non-metrics API calls. Treat compromise of Prometheus as API-key
compromise: keep its image pinned and scanned, keep its network internal and its
UI loopback-only, and rotate the key by rerunning preparation and recreating
both `app` and `prometheus`. This profile intentionally does not include
Alertmanager or an external receiver; it evaluates and exposes alerts locally.
External notification delivery remains an operator integration and must not be
claimed until a reviewed receiver is configured and tested.

The pinned Prometheus image is the official 3.13.2 release. Its dedicated
Trivy invocation ignores only `CVE-2026-42154`: Trivy reports the embedded
Prometheus module as a `+dirty` pseudo-version even though the upstream fix is
present in releases 3.5.3, 3.11.3, and later. Do not reuse that ignore file for
the auditor images or add another entry without a new documented review.

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

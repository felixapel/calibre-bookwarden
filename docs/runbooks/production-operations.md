# Production operations runbook

## First deployment

Copy `.env.example` to `.env`, replace every placeholder with distinct secrets
and an absolute library path, then run `./scripts/prepare-production.sh`. This
creates every bind-mount target with the configured runtime UID/GID and validates
the complete Compose contract before Docker can create root-owned directories.

Build or pull the exact `BOOKAUDIT_IMAGE`, start PostgreSQL and Valkey, run the
one-shot migration, then start the writer and app:

```bash
docker compose build app
docker compose up -d --wait postgres valkey
docker compose --profile maintenance run --rm migrate
docker compose up -d writer app
```

Do not expose the port until both containers report healthy and authenticated
`/api/health/ready` returns `ready`.

The writer is Linux-only. Its startup/runtime contract requires `/proc/self/fd`
and sealable `memfd` support for immutable OPF/cover handoff. A failure to
create or pass a sealed descriptor is a stop condition, not a reason to fall
back to pathname-based writes.

## Backup

The commands use the PostgreSQL bootstrap administrator inside the private
database container. Protect `POSTGRES_PASSWORD` as a backup credential;
application and writer roles intentionally lack schema-wide backup privileges.

Stop mutation intake and the writer, then capture PostgreSQL and the
writer-exclusive artifacts in the same maintenance window:

```bash
docker compose stop app writer
backup_dir="${BOOKAUDIT_BACKUP_HOST_PATH:-./backups}/$(date -u +%Y%m%dT%H%M%SZ)"
install -d -m 0700 "$backup_dir"
docker compose exec -T postgres pg_dump -U bookaudit -d bookaudit -Fc > "$backup_dir/bookaudit.dump"
tar -C . -czf "$backup_dir/writer-artifacts.tar.gz" .writer-artifacts
python scripts/create-backup-manifest.py \
  "$backup_dir/bookaudit.dump" \
  "$backup_dir/writer-artifacts.tar.gz" \
  "$backup_dir/manifest.json"
```

Store the two files together. Valkey is transport/cache state; the durable DB
outbox is authoritative, so a Valkey snapshot is not required for correctness.

## Restore into an empty environment

Provision an empty PostgreSQL database and an empty writer artifacts directory,
then restore both halves before starting runtime roles:

```bash
docker compose up -d --wait postgres valkey
docker compose exec -T postgres pg_restore -U bookaudit -d bookaudit < bookaudit.dump
tar -C . -xzf writer-artifacts.tar.gz
BOOKAUDIT_REQUIRE_WRITER_READY=false docker compose up -d app
```

Do **not** start the writer yet. Confirm the restored Alembic revision and ACLs,
inspect every non-terminal ledger/outbox row, and verify the referenced restore
artifact hashes against the Calibre library. Resolve `unknown`, `restore_failed`,
`claimed`, `writing`, `verifying`, and `restoring` cases under the incident
procedure before allowing any consumer to run. The app is read-only and starts
with writer-readiness temporarily disabled solely to support inspection.

Only after the ledger and artifacts are accepted:

```bash
docker compose up -d writer
BOOKAUDIT_REQUIRE_WRITER_READY=true docker compose up -d --force-recreate app
```

Confirm authenticated `/api/health/ready` after re-enabling the writer gate. A
clean-environment `pg_dump`/`pg_restore` drill on 2026-07-12 restored the then
current Alembic head `b18f4c2d7a90` and the runtime ACLs successfully. The
Manifestation V2 development head is now `e91a7f42c6b4`; repeat the clean
upgrade/downgrade and backup/restore drill before promoting that schema.

## Upgrade

The signed GHCR procedure below is a legacy mirror-release path, not the normal
development workflow. Development, review, and CI use Gitea; use the mirror
release path only for an explicitly authorized release operation.

1. Verify the release signature and attestations, pull the digest-pinned image,
   and retain the prior image digest:

   ```bash
   cosign verify ghcr.io/OWNER/REPOSITORY@sha256:DIGEST \
     --certificate-identity-regexp='https://github.com/OWNER/REPOSITORY/.github/workflows/release.yml@refs/tags/v.*' \
     --certificate-oidc-issuer=https://token.actions.githubusercontent.com
   gh attestation verify oci://ghcr.io/OWNER/REPOSITORY@sha256:DIGEST \
     --repo OWNER/REPOSITORY
   ```

2. Set `BOOKAUDIT_IMAGE` to that exact digest.
3. Stop `app` and `writer` and take the paired backup above.
4. Run `docker compose --profile maintenance run --rm migrate` exactly once.
5. Start `app` in read-only mode and require readiness to pass.
6. Start the single writer only after the read-only gate is healthy.

The full production profile sets `BOOKAUDIT_REQUIRE_WRITER_READY=true`. Once the
writer is enabled, `/api/health/ready` and the writer container healthcheck both
require a fresh Valkey heartbeat. During the temporary read-only upgrade gate,
set it to `false` for the app only; restore it to `true` before declaring the
write-enabled deployment healthy.

Runtime services never run migrations automatically.

## Rollback

If the schema change is additive and the previous image is N-1 compatible, stop
runtime roles and restart the previous image digest. If compatibility is not
explicitly documented, restore the paired database/artifacts backup into an
empty environment. Never run an ad-hoc Alembic downgrade against live writer
operations.

## Incident handling

- `unknown` or `restore_failed`: stop writer, preserve DB and artifacts, inspect
  actual Calibre metadata against `before_metadata` and `target_metadata`.
- Missing/tampered OPF: do not retry the write; recover the matching artifact
  from backup and re-run reconciliation.
- Writer-lock conflict: verify there is exactly one live writer. Do not delete a
  `BookWriteLock` while its owner may still be running.
- Low disk: stop writer before deleting anything. Restore points have a 30-day
  retention and must be cleaned only through the verified retention workflow.

## Restore-point retention

Retention is dry-run by default and operates only on `.writer-artifacts`:

```bash
docker compose --profile maintenance run --rm retention
```

After taking and verifying a paired PostgreSQL plus `.writer-artifacts` backup,
execute the exact previewed cleanup with its audit reference:

```bash
docker compose --profile maintenance run --rm retention retention \
  --execute \
  --backup-reference "/backups/<backup-directory>/manifest.json"
```

The command verifies both backup checksums, acquires the writer advisory lock,
rejects a fresh heartbeat or any non-terminal operation, and atomically
quarantines only the exact inode+manifest-digest set from its preview. It is safe
to invoke while the writer service exists: if the writer owns the lock, the
command exits non-zero before mutation. Stopping the writer remains a convenient
maintenance-window practice, but it is not a human-supplied safety assertion.

The backup manifest and every referenced path component must be regular files
or directories, never symlinks. Retention holds the advisory lock through all
ledger, heartbeat, revalidation, quarantine, and deletion work. Record the exit
status and deleted count, then check artifact disk usage.

If the process is interrupted after the transaction is published, normal
retention fails closed and prints the pending transaction ID. Preserve the
original paired backup and inspect the incident before resuming exactly that
transaction:

```bash
docker compose --profile maintenance run --rm retention retention \
  --recover-quarantine "<transaction-id>" \
  --execute \
  --backup-reference "/backups/<original-backup-directory>/manifest.json"
```

Recovery accepts only the same still-verifiable backup manifest whose SHA-256
digest was bound to the original deletion. It re-acquires the advisory lock and
re-runs the heartbeat and ledger guards. It then validates the journal,
transaction and payload inodes, path-location state, and candidate manifest
digests before mutation. Recovery resumes deletion; it does not roll candidates
back into the live restore tree.

Never rename, restore, or delete files inside `.retention-quarantine` manually.
Completed transactions intentionally retain a small `state=deleted` journal as
a durable tombstone and do not block later retention. Pre-publication staging
contains no moved candidates and is removed automatically only after its shape
has been validated as an abandoned empty staging transaction.

## Schema downgrade guard

Do not downgrade a database that contains Manifestation V2 runs, evidence, or
writer recovery records. The V2 migrations deliberately raise an error instead
of dropping those columns. Export and verify both the database and writer
artifacts, finish or reconcile every operation, and use a separately reviewed
data-migration procedure if a downgrade is ever required.

## Monitoring alerts

Load `ops/monitoring/alerts.yml` into Prometheus and replace the API-key
placeholder in `ops/monitoring/prometheus.yml` through the deployment secret
mechanism. The metrics endpoint is intentionally authenticated in production.

- `BookAuditUnavailable`: check container health, then `/api/health/ready` with
  the API key. Keep the writer stopped if PostgreSQL, Valkey, or its heartbeat is
  unhealthy.
- `BookAuditHighServerErrorRate`: correlate the route/status labels with
  structured logs using `X-Request-ID`; pause new apply requests if writes fail.
- `BookAuditSlowApi`: inspect database and Valkey latency before scaling API
  replicas. Never scale the writer beyond one instance.
- `BookAuditWriterHeartbeatStale`: stop new apply intake and inspect the sole
  writer before restarting it; never start a second writer as a workaround.
- `BookAuditOperationalMetricsCollectionFailed`: check app-role database access
  and Valkey connectivity; treat writer/outbox graphs as unknown until restored.
- `BookAuditOutboxBacklog`: inspect pending/processing ledger operations and
  Valkey connectivity. Preserve the database before manual reconciliation.
- `BookAuditRollbackFailure`: stop the writer immediately and follow the
  `restore_failed` incident procedure above.

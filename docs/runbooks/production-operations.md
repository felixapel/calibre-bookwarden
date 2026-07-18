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
current Manifestation V2 Alembic head is `c8e1f0a2b4d6`; it includes the
supervised-pilot schema from `a72c9d4e8f31` and durable verification-run leases.
Repeat the clean upgrade and backup/restore drill before promoting that schema.
Its staged downgrades refuse to discard persisted pilot evidence or to remove
lease state while any run is leased or any Manifestation V2 run is non-terminal.

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
require a fresh Valkey heartbeat. When the supervised pilot is enabled,
readiness additionally requires its exact pilot ID, max operations, release
digest, Alembic head, and canonical library-root hash. During the temporary
read-only upgrade gate, set it to `false` for the app only; restore it to `true`
before declaring the write-enabled deployment healthy.

Runtime services never run migrations automatically.

## Supervised Manifestation V2 pilot

Keep `BOOKAUDIT_MANIFESTATION_V2__SUPERVISED_PILOT__ENABLED=false` during
ordinary shadow operation. Before a pilot, require the exact-commit Gitea
real-service gate plus the disposable and restored-clone rehearsals in the
[Manifestation V2 runbook](../calibration/manifestation-v2-runbook.md).

The operator-owned `.env` must bind a unique pilot ID, one-to-five operation
budget, and the exact `sha256:...` digest suffix of `BOOKAUDIT_IMAGE`.
`./scripts/prepare-production.sh` rejects a mutable/mismatched image, missing
pilot ID, larger budget, disabled writer readiness, or enabled auto-apply.

Queue one manually reviewed Tier A evidence package, wait for its operation and
outbox to become terminal/published, then validate Calibre readback and rollback
artifacts before the next package. A consumed reservation is never refunded.
Metrics must show the pilot and exact writer binding healthy throughout.

Emergency stop order:

```bash
docker compose stop app writer
# Set BOOKAUDIT_MANIFESTATION_V2__SUPERVISED_PILOT__ENABLED=false in .env.
docker compose run --rm app pilot-stop "$PILOT_ID" --yes
```

Do not restart the writer until every nonterminal or failed operation is
classified against live Calibre metadata and its hashed recovery artifacts.
Closing the row blocks writer revalidation but cannot cancel an external
Calibre process that had already started. Never reopen or reuse a stopped pilot
ID; start a separately reviewed pilot only after the incident is resolved.

If the terminal state is exactly `failed`, the outbox is `failed`, no writer
lease remains, and live Calibre plus recovery evidence prove the write either
never began or was fully restored, append the incident acknowledgement. The
command independently requires and locks the stopped pilot row:

```bash
docker compose run --rm app incident-ack "$OPERATION_ID" \
  --actor "$OPERATOR" \
  --reason "Verified Calibre matches before_metadata and recovery hashes" \
  --yes
```

This insert-only record preserves the failed operation/outbox while allowing a
separately reviewed next pilot to pass the historical-outbox gate. It clears
that acknowledged `failed` row from the active V2-failure alert and increments
`bookaudit_v2_incidents_acknowledged`. Never use or attempt to emulate this for
`unknown` or `restore_failed`; the command rejects them. It does not repair
metadata, reopen a stopped pilot, or refund its reservation.
Queue admission and the active-failure metric independently recheck the linked
terminal state, failed outbox, stopped historical pilot, distinct current pilot
and absence of a book lease; acknowledgement-row presence alone is not enough.

## Rollback

If the schema change is additive and the previous image is N-1 compatible, stop
runtime roles and restart the previous image digest. If compatibility is not
explicitly documented, restore the paired database/artifacts backup into an
empty environment. Never run an ad-hoc Alembic downgrade against live writer
operations. Before downgrading from `c8e1f0a2b4d6`, stop app and writer and
confirm this precondition returns zero rows:

```sql
SELECT run_id, status, finished_at, lease_owner, lease_expires_at
FROM verificationrun
WHERE lease_owner IS NOT NULL
   OR lease_expires_at IS NOT NULL
   OR (
        pipeline_version = 'manifestation-v2'
        AND (
             finished_at IS NULL
             OR status NOT IN (
                  'completed',
                  'completed_with_errors',
                  'failed',
                  'blocked_recovery'
             )
        )
   );
```

PostgreSQL downgrade additionally acquires an exclusive `NOWAIT` table lock;
lock contention is a hard stop, not a reason to bypass the migration guard.

## Incident handling

- `unknown` or `restore_failed`: stop writer, preserve DB and artifacts, inspect
  actual Calibre metadata against `before_metadata` and `target_metadata`.
- Terminal `failed`: stop intake/writer and prove from Calibre, ledger, outbox,
  change row, and hashed artifacts whether no mutation occurred or restoration
  completed. Only then use `incident-ack`; otherwise keep the hard stop.
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
- `BookAuditV2WriterBindingMismatch`: stop intake and writer; compare the
  configured image digest, pilot digest, Alembic head and canonical library
  root. Do not change the persisted binding to make the alert disappear.
- `BookAuditV2PilotOperationStalled`: stop intake and writer after preserving
  state; inspect the exact ledger/outbox row and Calibre readback before
  recovery.
- `BookAuditV2PilotOperationFailed`: stop and close the exact pilot. Preserve
  all rollback artifacts. `unknown`/`restore_failed` remain unresolved until
  repaired; an exactly `failed`, safely reconciled row may use the append-only
  acknowledgement procedure above before a new pilot.
- `BookAuditV2PilotBudgetExhausted`: the pilot is complete for operational
  purposes. Disable and close it; never raise or reset the persisted budget.

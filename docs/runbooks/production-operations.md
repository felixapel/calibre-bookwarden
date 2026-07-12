# Production operations runbook

## Backup

The commands use the PostgreSQL bootstrap administrator inside the private
database container. Protect `POSTGRES_PASSWORD` as a backup credential;
application and writer roles intentionally lack schema-wide backup privileges.

Stop mutation intake and the writer, then capture PostgreSQL and the
writer-exclusive artifacts in the same maintenance window:

```bash
docker compose stop app writer
docker compose exec -T postgres pg_dump -U bookaudit -d bookaudit -Fc > bookaudit.dump
tar -C . -czf writer-artifacts.tar.gz .writer-artifacts
```

Store the two files together. Valkey is transport/cache state; the durable DB
outbox is authoritative, so a Valkey snapshot is not required for correctness.

## Restore into an empty environment

Provision an empty PostgreSQL database and an empty writer artifacts directory,
then restore both halves before starting runtime roles:

```bash
docker compose up -d postgres valkey
docker compose exec -T postgres pg_restore -U bookaudit -d bookaudit < bookaudit.dump
tar -C . -xzf writer-artifacts.tar.gz
docker compose up -d app writer
```

Confirm `/api/health/ready`, inspect any ledger rows in `unknown` or
`restore_failed`, and do not enable writes until every non-terminal operation is
reconciled. A clean-environment `pg_dump`/`pg_restore` drill on 2026-07-12
restored Alembic head `6a3f83d9e621` successfully.

## Upgrade

1. Pull/build the digest-pinned image and retain the prior image digest.
2. Stop `app` and `writer` and take the paired backup above.
3. Run `docker compose --profile maintenance run --rm migrate` exactly once.
4. Start `app` in read-only mode and require readiness to pass.
5. Start the single writer only after the read-only gate is healthy.

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

# Production readiness

## Current decision

Release `v1.2.1` (2026-07-13) is approved for supervised and unattended
**internal** production operation. This decision covers the shipped
application, writer, database protocol, maintenance commands, Compose contract,
and release pipeline. It does not certify the surrounding host or operator
accounts.

Before exposing a deployment, the operator must rotate deployment secrets and
revoke every superseded Gitea or registry personal access token. Never embed a
token in a Git remote URL, committed file, shell history, or CI log. Token
revocation is an account-side action and cannot be proven by repository tests.

## Acceptance evidence

- `scripts/verify-calibre-gate.sh` exited 0 for the `v1.2.1` release candidate.
- 218 selected backend and integration tests passed, followed by the separate
  Komf integration gate.
- The Komf integration test passed.
- Seven conditional local skips remain limited to unavailable optional integrations;
  CI and release verification explicitly require the PostgreSQL/Valkey retention
  test and cannot silently skip it.
- The PostgreSQL/Calibre SIGKILL reconciliation test was executed separately
  with both dependencies present and passed.
- A disposable production-mode PostgreSQL/Valkey drill proved that retention
  exits non-zero and preserves the restore point for an advisory-lock owner, a
  fresh heartbeat, and a non-terminal ledger row, then deletes exactly that
  restore point only when all three guards are clear.
- Fault injection covers journal creation, atomic transaction publication,
  candidate rename, journal advancement, partial payload deletion, and terminal
  tombstone recovery boundaries.
- Independent adversarial review returned GO for both supervised and unattended
  internal production operation after the retention and lock-cleanup fixes.

## Safety boundaries

- Only the dedicated writer receives a read-write Calibre library mount.
- Writer exclusivity is enforced by a PostgreSQL session advisory lock, not by a
  human confirmation flag.
- Retention requires a paired backup manifest whose database dump and artifact
  archive match their SHA-256 digests. Symlinks are rejected in the manifest and
  referenced paths.
- Retention records and revalidates each expired restore directory's device,
  inode, and manifest digest before moving the exact set into a same-filesystem
  quarantine.
- Recovery is explicit per transaction ID, requires the same verified backup,
  resumes deletion under the production writer guard, and retains a durable
  terminal journal. It never guesses between rollback and deletion.
- The advisory lock protects against application-mediated concurrency. A
  privileged host process that mutates bind-mounted paths concurrently is
  outside this trust boundary; restrict host access during maintenance.
- If retention is interrupted after publication, preserve the hidden quarantine
  for investigation and use only the documented `--recover-quarantine` command.
  Manual deletion remains outside the supported recovery contract.

## Deployment checklist

1. Revoke superseded Gitea and registry tokens; rotate deployment secrets.
2. Pin `BOOKAUDIT_IMAGE` to the verified release digest.
3. Run `scripts/prepare-production.sh` and resolve every validation error.
4. Create the paired PostgreSQL/artifact backup and its manifest.
5. Run the explicit migration service before runtime services.
6. Require authenticated readiness and a fresh writer heartbeat.
7. Load the supplied monitoring and alert rules.
8. Perform the restore drill and rollback check described in the production
   operations runbook.

See [DEPLOYMENT.md](../DEPLOYMENT.md) and the
[production operations runbook](runbooks/production-operations.md) for exact
commands.

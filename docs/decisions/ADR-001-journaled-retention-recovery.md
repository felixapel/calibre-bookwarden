# ADR-001: Resume journaled retention deletion after interruption

## Status

Accepted

## Date

2026-07-13

## Context

Restore-point retention deletes safety artifacts and may be interrupted between
moving a candidate into quarantine and removing the quarantine payload. A
best-effort rollback cannot be made atomic across multiple directory renames,
and guessing whether to roll back or continue can recreate already-expired data
or delete an unverified replacement. Recovery must remain fail-closed across
process termination and power-loss boundaries.

The production contract also requires the writer advisory lock, heartbeat, and
operation ledger to remain authoritative. A valid but unrelated backup must not
authorize destruction of a pending transaction.

## Decision

Retention uses a durable, versioned journal and recovery always resumes deletion.

- A complete `prepared` transaction is built under a staging name and published
  with one same-filesystem rename before any restore candidate moves.
- Every candidate is bound by its relative path, directory device/inode, and
  `restore.json` SHA-256 digest. Every rename is followed by `fsync` of both
  parent directories before the journal advances.
- Recovery requires one canonical transaction ID and the exact paired-backup
  manifest digest recorded when deletion began.
- State and path location are preflighted before recovery mutates anything.
  Symlinks, ambiguous locations, unknown paths, replaced identities, and forged
  `moved` inventories are rejected.
- `deleting` permits a partially removed payload but never a recreated original.
  Completion leaves a durable `deleted` journal tombstone rather than opening an
  unlink/rmdir crash window.
- Abandoned pre-publication staging may be removed only when it contains no
  candidate data and matches the strict staging shape.

## Alternatives considered

### Roll every moved candidate back

Rejected because rollback itself spans multiple non-atomic renames and can be
interrupted. It also cannot safely reverse a payload that was already partially
deleted.

### Recover every pending transaction in one command

Rejected because one valid transaction could be destroyed before a later corrupt
transaction fails preflight. Explicit transaction selection bounds the mutation.

### Remove the journal and transaction directory after success

Rejected because unlinking the final journal before `rmdir` creates an
unclassifiable crash state. A small terminal tombstone is safer than eager
cleanup.

## Consequences

- Operators must retain and reuse the original verified paired-backup manifest
  for recovery.
- Recovery is an explicit incident action and resumes deletion; it is not an undo
  command.
- Completed tombstones accumulate small journal files. A future compaction policy
  may remove them only with a separate, equally durable protocol.
- Concurrent privileged host mutation remains outside the application trust
  boundary and must be prevented operationally.

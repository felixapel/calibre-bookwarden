# ADR-010: Retire direct library mutations and parallel authority

## Status

Accepted for local remediation; release gates pending. Supersedes ADR-008 unsupported CoverDeck apply behavior and ADR-009 mutation and unverified performance claims.

## Context

Local fixtures demonstrated a WAL-incomplete raw-copy snapshot, direct changes with read_only enabled, fake Cover Deck apply, a tamper-accepting prototype rollback and false duplicate clones. The repository already has a stronger exact-authorization and serialized-writer contract. Repairing each alternate writer would create competing safety mechanisms.

## Decision

Make DirectCalibreEngine read-only and retire its mutators, the four legacy maintenance commands, direct audit writes and in-place extraction API. Preserve compatibility names with explicit rejection. Disable in-place optimizer methods, experimental ledger/schema creation, cooperative JSON lock and prototype thumbnail/reconnect mutations. Keep pure diagnostic helpers where useful.

Database snapshots use consistent SQLite backup and verification, outside the library; they are explicitly database-only. No incomplete raw-copy fallback is accepted.

The production Certificate A boundary remains read-only. Manifestation V2 identity, exact authorization, sealed artifacts, coordinator and sole writer remain the only supervised correction architecture. Provider ranking, OCR and vision are evidence-only. Duplicate suggestions never authorize merging or deletion.

## Consequences

Old direct-maintenance callers now receive clear errors instead of silently bypassing authorization. Some advertised features are deliberately unavailable until their supported V2 contracts and real-service gates exist. A read-only library cannot be made writable by selecting another legacy command or toggling configuration.

Local checks do not establish production readiness. Whole-library restoration, external-writer exclusion, exact-revision Linux/Gitea gates and explicit deployment/pilot authorization remain necessary. Preserve pre-existing local changes through the verified source archive.

# ADR-009: DirectCalibreEngine SQLite Access for Homelab Forensics

## Status
Accepted

## Date
2026-09-01

## Context
In v0.9 and v1.0, Calibre interaction was conducted via `calibredb list --for-machine`. In production libraries of 3,000 to 50,000 books, subprocess invocation incurs substantial serialization and parsing overhead (~200ms per invocation), making library-wide scans take upwards of 15 minutes. Furthermore, `calibredb` cannot perform deep forensic checks such as detecting orphaned junction rows, verifying SQLite B-tree integrity, or streaming keysets with bounded memory.

## Decision
Introduce `DirectCalibreEngine` (`src/calibre_ai_auditor/calibre/direct_engine.py`) for offline CLI and Homelab sidecar environments:
- **Direct SQLite Access**: Opens `metadata.db` with `busy_timeout=30000` and `PRAGMA foreign_keys=ON`.
- **Emulated Python Triggers**: Registers custom Python callbacks (`title_sort`, `author_sort`) to perfectly mimic Calibre's native trigger functions during mutation.
- **Keyset Pagination**: Implements keyset streaming (`WHERE id > last_id ORDER BY id LIMIT 500`), avoiding offset scanning and maintaining constant `<32 MB` memory overhead across 100,000+ volumes.
- **Zero N+1 Queries**: Aggregates authors, identifiers, tags, and formats via compound SQL `GROUP_CONCAT` and single-pass joins, scanning 3,180 books in **44 seconds** (up to 6,090 books/second on NVMe).
- **Atomic Pre-flight Safety**: Enforces `VACUUM INTO metadata.db.bak_<timestamp>` prior to any mutation (e.g. `sync_all_author_sorts`, `purge_orphan_foreign_keys`).
- **Profile Boundary**: `DirectCalibreEngine` is strictly scoped to homelab sidecars and interactive CLI curation. It is **expressly excluded** from the Certificate A production boundary (which strictly forbids database mutations and requires a stopped library).

## Consequences
- Throughput increased by over 19x compared to subprocess execution.
- Enables deep foreign key and schema health audits impossible through standard CLI tools.
- Preserves complete Calibre database invariants through trigger emulation.

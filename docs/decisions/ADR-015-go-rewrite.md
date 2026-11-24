# ADR-015: Rewrite bookwarden in Go as one container

- Status: Accepted
- Date: 2026-10-12
- Decision owners: maintainer

## Context
Certificate A, the Python implementation, ran as four containers and built three images: app 901 MB, writer 2.37 GB and edge 105 MB. It read a snapshot of a stopped library and could not apply fixes in production. Its 2026-10-06 run of 3,173 books found no Tier A identity, because only Open Library answered.

## Decision
Rewrite bookwarden in Go as one container, as specified in `docs/superpowers/specs/2026-10-12-bookwarden-go-rewrite-design.md`:
- Calibre's content server for every read and write;
- the shared Postgres;
- guided fixing;
- DNB and BNE as official sources.

The rewrite lives on the `go-rewrite` branch until the cutover merges it into `main`.

This supersedes the ADRs that describe the Python implementation: ADR-001, ADR-004, ADR-005, ADR-006, ADR-007, ADR-010, ADR-012 and ADR-013. ADR-010's ban on writing the library directly carries over: every write goes through the content server.

These still apply:
- ADR-002: exact-manifestation tiers, as amended in spec section 7.
- ADR-003: the supervised scope.
- ADR-014: metrics only for the operator's own Prometheus.

## Consequences
- One image (at most 100 MB) and one container. No snapshot, no library mount, no compose.
- The old Postgres evidence is archived, not migrated.
- At the cutover (spec section 17), the last Certificate A commit is tagged `certificate-a-final`, and `go-rewrite` is merged into `main`.

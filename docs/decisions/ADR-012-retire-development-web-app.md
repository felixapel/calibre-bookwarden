# ADR-012: Retire the development web application

- Status: Superseded by [ADR-015](ADR-015-go-rewrite.md).
- Date: 2026-10-04 (amended the same day: writer operator path)
- Decision owners: maintainer

## Context

The repository serves two HTTP applications. Certificate A
(`web/production.py`, ADR-005) is the supported, read-only product boundary.
The development application (`web/app.py` with fourteen routers, jobs,
templates and its own auth, rate-limit and static handling) predates it and
keeps legacy surfaces alive: V1 runs, uploads, provider testing, cover and
curation tools, ingest, vectors and Paperless/Calibre-Web bridges.

The runtime import trace (`scripts/trace_import_closure.py`, issue #16) shows
that neither Certificate A nor the writer imports the development application,
yet it holds roughly 9–10k lines of otherwise unused code in the tree, carries a
second copy of every security middleware, and needed its own hardening
(loopback default, DNS-rebinding guard). It has two consumers. An agent's MCP
wrapper reads `/api/books`, `/api/runs` and verdict routes from it. The
supervised writer's only input path also runs through it: authorizing a sealed
V2 package, queueing its apply, queueing an undo and reading the operation
state (`/api/review/v2/{id}/authorize`, `/api/apply/v2`, `/api/undo/{id}`,
`/api/operations/{id}`). The CI-required supervised pilot exercised that path.

## Decision

Retire the development web application.

1. Certificate A gains the read capability the wrapper still needs: an exact
   `book_key` filter on `GET /api/review/v2`. Together with `/api/verify/runs`,
   `/api/verify/{run_id}`, `/api/review/v2/{evidence_id}`, `/api/health/ready`
   and `/api/metrics`, it replaces every wrapper tool with sealed V2 data.
2. That MCP wrapper migrates to those endpoints before
   the development application is removed.
3. The writer's operator actions move to `bookaudit` CLI commands in the writer
   image (`authorize`, `queue-apply`, `queue-undo`, `operation`) through a shared
   `apply.operator` module, and the supervised pilot drives that CLI. They stay
   off HTTP so that Certificate A remains read-only. No runtime role may write
   authorizations or operations (migration `f4a2d6e8c013`), so operators run
   them with the migrator DSN until a dedicated operator role exists.
4. Remove `web/app.py`, its routers, jobs, templates and helpers, and the
   development branch of `bookwarden web`, which then serves only Certificate A.
5. Remove, in separate pull requests, the modules that only the development
   application used, re-running the import trace each time so the Certificate A
   closure and the writer CLI closure stay intact.

The supervised writer (Certificate B: `apply/`, the writer and retention
commands and the writer image) is out of scope and remains.

## Consequences

The repository keeps one HTTP surface, one middleware stack and one API
contract. Development and debugging use Certificate A against a disposable
library (the existing disposable lab), not a second application. Writes are
authorized and queued only from a shell that holds writer-image access and the
migrator credentials. Legacy V1 reads, uploads, provider testing and the
cover/curation tools stop being available over HTTP; they were not part of the supported product (ADR-003,
ADR-005). Removing them also removes their dependencies from the image build
and the vulnerability surface.

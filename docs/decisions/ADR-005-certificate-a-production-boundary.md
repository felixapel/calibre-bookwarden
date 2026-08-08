# ADR-005: Isolate Certificate A as a stopped-library read-only product

- **Status:** Accepted
- **Date:** 2026-08-08

## Context

The repository accumulated a generic FastAPI application, V1 and V2 workflows,
LLM and vision providers, uploads, ingest watchers, MCP, vector search,
Paperless, Content Server access, and a privileged metadata writer. Many pieces
are useful research, but shipping them in one process and image makes the
production authority much larger than the actual user goal: audit a local
Calibre library without changing it.

Calibre's SQLite database cannot be treated as a safe offline snapshot while
Calibre, Content Server, or another writer is active. A long audit also cannot
be reliable if an in-process web task is its only owner. Production needs a
small explicit product boundary, durable recovery, and database permissions
that enforce the process split.

## Decision

Certificate A is a separate production application and runtime closure with
these invariants:

1. The only supported source is an operator-confirmed stopped Calibre folder.
   The verifier rejects SQLite sidecars, snapshots `metadata.db`, freezes
   membership and hashes, and detects later source drift.
2. The web app never mounts the library. It persists immutable requests and
   cancellation intent through the `bookaudit_app` PostgreSQL role.
3. A separate read-only verifier mounts `/library:ro`. It atomically claims a
   PostgreSQL request, increments a fence, holds an expiring lease, and
   predicates every durable mutation on its current owner/fence/lease.
4. PostgreSQL is authoritative. Valkey is used only for app rate limiting and a
   heartbeat bound to release digest, Alembic revision, and library-root hash.
5. Certificate A allows bounded Tesseract OCR and exact-ISBN Google Books/Open
   Library requests. It sends no book text or images to remote providers.
6. The production API and WebUI expose only health, metrics, capabilities,
   verify/cancel, run review, sealed evidence review, and bounded cover reads.
   Legacy routes and generated API documentation are absent.
7. The default image excludes Calibre, LLM SDKs, vector/MCP/watcher packages,
   and legacy UI surfaces. A writer is built into a different image and appears
   only under explicit Compose profiles.
8. The default Compose graph is exactly app, verifier, PostgreSQL, and Valkey.
   Runtime services never migrate the schema.
9. The backend binds to loopback. A same-host Caddy edge provides TLS and
   authentication without a path matcher, covering the entire site.
10. Certificate A configuration rejects auto-apply and the supervised writer
    pilot. Metadata writes require a future Certificate B decision.

## Consequences

- A running Calibre library is unavailable to Certificate A. The operator must
  choose a maintenance window; convenience does not weaken SQLite consistency.
- A worker crash is recoverable after lease expiry, and a stale process cannot
  write after another verifier increments the fence.
- The app role can request and cancel work but cannot forge evidence. The
  verifier can persist evidence but has no writer-ledger or schema authority.
- External providers learn a checksum-valid ISBN but not book content. Provider
  failure yields incomplete evidence rather than fabricated metadata.
- Certificate A operations, monitoring, backups, and CI no longer depend on a
  writer, writer heartbeat, restore artifacts, MCP, or live Content Server.
- Historical V1, remote-source, and supervised-writer code may remain for
  development, but it is outside the supported production closure.
- Production promotion is per exact Gitea commit and immutable image digest;
  old runs and mutable local tags are not evidence.

## Alternatives considered

### Keep one generic production app with feature flags

Rejected. Import and route registration still expose code and dependency
authority, and a configuration regression can silently widen the product.

### Mount the library read-only into the web app

Rejected. The request process does not need it, and removing the mount makes
that fact enforceable by the container boundary.

### Read a live Calibre database or copy `metadata.db` while Calibre runs

Rejected. SQLite WAL state and concurrent book-file changes cannot be made
consistent by a simple file copy or read-only bind mount.

### Use Valkey as the authoritative work queue

Rejected. The durable request, progress, cancellation, evidence, and fence need
one transactional authority. Valkey loss must not lose or duplicate audit work.

### Promote the existing supervised writer with Certificate A

Rejected. Read-only auditing and metadata mutation have different failure and
operator-recovery contracts. Writer approval requires a separate Certificate B
threat review, exact release gates, restored-clone drill, and human canaries.

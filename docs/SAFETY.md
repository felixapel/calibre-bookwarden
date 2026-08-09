# Certificate A safety model

Certificate A audits evidence and never changes Calibre metadata. Safety comes
from removing authority, not from asking a broad application to behave.

## Enforced boundaries

- Calibre and Content Server must be stopped. The offline source rejects a
  missing/unsafe `metadata.db`, unsupported schema, symlinked roots or metadata,
  mismatched schema/application-ID marker, and WAL/SHM/journal sidecars. Known
  schemas 25-27 are checked against their exact official marker state and the
  complete required table-and-column contract.
- Only the verifier receives `/library:ro`. The web app has no library mount;
  the Certificate A image has no Calibre executable.
- Formats are copied to a private bounded tmpfs and inspected there. Logical
  source references, not scratch paths, are sealed into evidence.
- The source snapshot freezes selected membership, metadata hash, and format
  hashes. Drift becomes `source_changed`.
- PostgreSQL atomically claims work with `FOR UPDATE SKIP LOCKED` semantics,
  increments a monotonic fence, and stores an expiring lease. Every worker
  mutation rechecks owner, fence, live lease, run state, and frozen membership.
- The app and verifier use different PostgreSQL login roles. App privileges are
  limited to request insertion, reads, and cancellation columns. Verifier
  privileges are limited to audit evidence/progress tables. Neither runtime
  role can migrate schema. Compose uses `.env` only for interpolation and
  passes each process its own declared DSN; it never injects the whole file.
- Readiness requires production configuration, exact app role/Alembic head,
  and a verifier heartbeat matching release digest, schema, and library root.
- OCR is Tesseract-only with bounded pages, timeout, process-group termination,
  and bounded sidecar output.
- Remote providers receive only one checksum-valid ISBN. Book text/images,
  uploads, LLM, vision, Tika, Paperless, vectors, watcher, MCP, and Content
  Server paths are disabled in the production contract.
- Evidence is schema-validated and SHA-256 sealed before review. Integrity
  mismatch is a conflict, never a best-effort display.
- The production API has no authorization or writer route. The retired apply
  route returns 410 and `writes_enabled` is always false.

## Operator responsibilities

Code cannot determine with certainty that every external Calibre writer is
stopped. The operator must provide that assertion before each run and keep the
library quiescent until terminal state.

Protect the following separately:

- `.env` (mode 0600), database role passwords, and internal API key;
- Caddy password hash and private TLS/DNS/VPN boundary;
- PostgreSQL dumps and their checksum files;
- the immutable image digest and exact Gitea release evidence;
- logs/evidence, which can contain private book metadata even though secrets and
  paths are excluded from public capability and metric surfaces.

Do not delete sidecars, edit run/fence/lease rows, disable integrity checks,
mount the library into the app, broaden database grants, expose port 8080 to the
LAN, or manually mark an incident complete.

## Expected failure behavior

- Provider timeout/conflict: incomplete or review evidence, no fabricated
  fallback metadata.
- OCR failure/timeout: bounded failure for that evidence path; no alternate
  cloud OCR.
- Verifier crash: lease expires and a recovery verifier increments the fence;
  the stale process loses write authority.
- Calibre/source change: terminal `source_changed` and operator investigation.
- Contract/release/schema mismatch during recovery: `blocked_recovery`.
- PostgreSQL, Valkey rate limiter, or verifier unavailable: readiness fails and
  request handling fails closed.
- Evidence seal mismatch: HTTP 409 integrity failure.

## Certificate B

The repository retains historical writer code, an explicit writer image, and
non-default profiles. They are outside Certificate A's deployment, monitoring,
backup, and support contract. Do not enable auto-apply or the supervised pilot;
the Certificate A validator rejects them.

A future Certificate B must receive its own threat model and promotion evidence
covering real Calibre mutations, immutable handoff, readback, rollback, crash
windows, disk exhaustion, restored-clone rehearsal, and serial human canaries.
No Certificate A result implies that approval.

See [ADR-005](decisions/ADR-005-certificate-a-production-boundary.md) and the
[production operations runbook](runbooks/production-operations.md).

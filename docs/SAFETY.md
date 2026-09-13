# Certificate A & Container Safety Model

Safety in Calibre Bookwarden comes from removing authority, enforcing least privilege at every system boundary, and treating all untrusted container contents as potential attack vectors.

---

## 1. Enforced Production Boundaries (Certificate A)

- **Library Isolation**: Calibre and Content Server must be stopped. The offline source rejects a missing/unsafe `metadata.db`, unsupported schema, symlinked roots or metadata, mismatched schema/application-ID marker, and WAL/SHM/journal sidecars. Known schemas 25-27 are checked against their exact official marker state and the complete required table-and-column contract.
- **Process & Mount Separation**: Only the verifier receives `/library:ro`. The web application has no library mount; the Certificate A image contains no Calibre executable.
- **Private Bounded Scratch**: Formats are copied to a private bounded tmpfs and inspected there. Logical source references, not scratch paths, are sealed into evidence.
- **Monotonic Fencing & Atomic Claims**: PostgreSQL atomically claims work with `FOR UPDATE SKIP LOCKED` semantics, increments a monotonic fence token, and records an expiring lease. Every worker mutation rechecks owner, fence, live lease, run state, and frozen membership.
- **Database Role Partitioning**: The app and verifier use different PostgreSQL login roles (`bookaudit_app`, `bookaudit_verifier`, `bookaudit_migrator`). App privileges are limited to request insertion, reads, and cancellation columns. Verifier privileges are limited to audit evidence and progress tables. Neither runtime role can migrate schema.
- **Fail-Closed API**: The production API exposes no writer route. The retired legacy apply route returns HTTP 410 and `writes_enabled` is immutable `false`.

---

## 2. Container & Operating System Hardening

- **Non-Root Execution**: Container processes run strictly under unprivileged user identity `UID:GID 10001:10001` (`bookaudit`).
- **Read-Only Root Filesystem**: The container root filesystem is mounted `read_only: true`. Temporary file operations are constrained to explicit `tmpfs` mounts with `mode=1777`.
- **Capability Dropping**: All Linux kernel capabilities are dropped (`cap_drop: [ALL]`).
- **No Privilege Escalation**: Enforces `security_opt: ["no-new-privileges:true"]`, preventing binaries from gaining elevated permissions via setuid bits.
- **Process Resource Limits**: Production Compose files specify explicit CPU, memory, and PID limits to mitigate runaway threads or denial-of-service conditions.

---

## 3. Ingestion & Malicious File Defenses (Zip-Bombs & Traversal)

All incoming ebooks and archives are treated as untrusted binary blobs and validated prior to decompression:

### Zip-Bomb & Decompression Bomb Protection
- **Member Size Caps**: In `extractors/multiformat.py` and `extractors/text.py`, each archive member is validated against `MAX_MEMBER_BYTES` (50 MB).
- **Archive Size Caps**: Total uncompressed archive size is constrained to `MAX_ARCHIVE_BYTES` (250 MB).
- **Compression Ratio Invariant**: Archives exceeding a compression ratio of **500:1** (`uncompressed_size / compressed_size > 500`) are rejected immediately as potential Zip-bomb denial-of-service payloads.
- **Image Decompression Bomb Guard**: Image parsing is capped at `MAX_IMAGE_PIXELS = 60,000,000` (60 Megapixels). Any oversized image triggers an `Image.DecompressionBombError` catch block, preventing process memory exhaustion.

### Path Traversal & Symlink Resolution
- **Anchored Traversal Prevention**: `security/files.py` enforces component-by-component path resolution using `openat(2)` with `O_NOFOLLOW | O_DIRECTORY` flags starting from the validated library root.
- **Sealed File Descriptors**: Under Linux, format handoff between processes uses `memfd_create` with write seals (`_F_SEAL_WRITE`), ensuring bytes cannot be mutated after verification.
- **Web Static Guard**: All WebUI asset requests pass through `is_path_within_static`, preventing directory escape attacks (`../`).

---

## 4. Operator Responsibilities

Code cannot determine with certainty that every external Calibre writer is stopped. The operator must provide that assertion before each run and keep the library quiescent until terminal state.

Protect the following separately:
- `.env` (mode 0600), database role passwords, and internal API key;
- Caddy password hash and private TLS/DNS/VPN boundary;
- PostgreSQL dumps and their checksum files;
- the immutable image digest and exact Gitea release evidence;
- logs/evidence, which can contain private book metadata even though secrets and paths are excluded from public capability and metric surfaces.

---

## 5. Expected Failure Behavior

- **Provider timeout/conflict**: Incomplete or review evidence, no fabricated fallback metadata.
- **OCR failure/timeout**: Bounded failure for that evidence path; no alternate cloud OCR.
- **Verifier crash**: Lease expires and a recovery verifier increments the fence; the stale process loses write authority.
- **Calibre/source change**: Terminal `source_changed` and operator investigation.
- **Contract/release/schema mismatch during recovery**: `blocked_recovery`.
- **PostgreSQL, Valkey rate limiter, or verifier unavailable**: Readiness fails and request handling fails closed.
- **Evidence seal mismatch**: HTTP 409 integrity failure.

---

## 6. Certificate B Boundary

The repository retains historical writer code, an explicit writer image, and non-default profiles. They are outside Certificate A's deployment, monitoring, backup, and support contract. Do not enable auto-apply or the supervised pilot; the Certificate A validator rejects them.

A future Certificate B must receive its own threat model and promotion evidence covering real Calibre mutations, immutable handoff, readback, rollback, crash windows, disk exhaustion, restored-clone rehearsal, and serial human canaries. No Certificate A result implies that approval.

See [ADR-005](decisions/ADR-005-certificate-a-production-boundary.md) and the [production operations runbook](runbooks/production-operations.md).

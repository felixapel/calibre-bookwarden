# Certificate A production readiness

## Decision

Certificate A is the only production target in this repository. It is a
read-only, stopped-library metadata audit with human evidence review. It is not
permission to change Calibre metadata.

The implementation is a **production candidate** until every gate below is
green for the same Git commit and immutable Certificate A image digest. A prior
run, local tag, mutable image, or Certificate B test cannot substitute for that
evidence.

Certificate B remains blocked. The production environment validator rejects
both auto-apply and an enabled supervised writer pilot.

## Shipped trust boundary

| Boundary | Certificate A contract |
|---|---|
| Source | Existing Calibre folder; Calibre and Content Server explicitly stopped |
| Mount | Verifier only, `/library:ro`; app has no library mount |
| Queue | PostgreSQL request rows with atomic claim, expiring lease, and monotonic fence |
| Worker writes | Evidence/progress database writes only; no Calibre metadata writes |
| App database role | Select plus request insert and cancellation columns only |
| Verifier database role | Exact evidence/progress tables only; no writer ledger privileges |
| OCR | Tesseract only; bounded pages, input size, output size, and process-group timeout |
| Providers | Google Books and Open Library queried only with one checksum-valid ISBN |
| Remote disclosure | ISBN only; remote text and image disclosure disabled |
| Web/API | Dedicated production app; Overview, Verify, Evidence; docs and legacy routes absent |
| Network edge | Backend loopback only; same-host Caddy TLS and whole-site basic authentication |
| Images | Minimal Certificate A default; quarantined writer is a separate explicit target/profile |
| Schema | Explicit role provisioning then one-shot migration; runtime services do neither |

The offline reader admits only Calibre's reviewed schema markers: versions 25
and 26 with an unset SQLite application ID, and version 27 with application ID
`0x63616c69`. Version 25 is exercised with Debian's real Calibre 6.13 boundary;
version 27 is the schema produced by the separately pinned Calibre 9.11 writer.
Every accepted version still has to provide the complete table-and-column
contract, pass SQLite integrity checks, and remain unchanged for the audit.
Unknown versions and version/application-ID mismatches fail closed.

The verifier heartbeat is bound to the immutable release digest, Alembic head,
and canonical library-root hash. Readiness requires the exact database role and
schema plus a fresh matching verifier heartbeat. A different release, schema,
library root, or stale worker fails closed.

## Mandatory exact-commit release gates

The canonical pipeline is `.gitea/workflows/v1-tests.yml`; GitHub mirrors are
not release evidence.

1. Lock verification, Ruff, format, strict mypy, and the complete hermetic
   backend suite pass.
2. Disposable PostgreSQL proves an existing-volume upgrade that initially
   lacks the verifier role, idempotent role provisioning, the runtime ACL
   matrix, denied abuse cases, concurrent single-owner claim, fence increment,
   and stale-worker rejection.
3. Real Calibre creates a disposable version-25 library that the offline source
   reads through the reviewed compatibility contract, and real Tesseract reads
   a bounded raster PDF.
   Neither live-service test may skip.
4. The exact Certificate A Python dependency closure passes `pip-audit` at the
   pinned audit-tool version.
5. The WebUI passes `npm ci`, high-severity npm audit, zero-warning lint, build,
   desktop Chromium, mobile Chromium, and accessibility contracts.
6. The default and writer images build separately. The default image proves
   the absence of Calibre and quarantined packages. Both images pass pinned
   high/critical vulnerability and secret scans.
7. Compose renders exactly `app`, `verifier`, `postgres`, and `valkey` by
   default; optional migration and writer profiles do not enter that graph.
8. The 50k metadata microbenchmark passes. It is a resolver/storage guard, not
   a promise of full-library wall-clock throughput.

Do not manually rerun a failed Gitea Action merely to produce a green badge.
Fix the cause and let the next commit trigger a new run.

## Operator-owned promotion gates

Automation cannot prove these facts. Record them for the exact image digest:

1. The deployment `.env` is mode 0600, contains distinct strong secrets,
   explicit trusted hosts, a digest-pinned image, and a matching release digest.
   Compose interpolation must not become whole-file `env_file` injection into
   any runtime container.
2. Calibre and every writer to the library are stopped before the audit; no
   SQLite WAL, SHM, or journal sidecar exists.
3. PostgreSQL backup and empty-environment restore are performed and verified.
4. Caddy validates and serves a trusted TLS name; authentication covers the
   SPA, assets, API, health, and metrics paths; the backend port is loopback.
5. Prometheus reads the API key from a protected file and all Certificate A
   alerts load successfully.
6. A small, then representative, read-only audit completes without unexplained
   `source_changed`, `blocked_recovery`, stale-heartbeat, or evidence-seal
   states.
7. The operator verifies that stopping an audit, restarting the verifier, and
   restoring PostgreSQL follow the runbook without touching the library.

## Release evidence record

Store this outside the repository secrets and complete it at promotion time:

```text
git_commit=
gitea_run_id=
gitea_conclusion=success
certificate_a_image=
certificate_a_image_digest=
alembic_revision=f4a2d6e8c013
deployment_host=
tls_name=
backup_manifest_or_ticket=
restore_drill_ticket=
representative_shadow_run=
operator=
approved_at_utc=
```

Never put API keys, database passwords, authentication hashes, library paths,
book titles, or evidence contents in this record.

## Certificate B separation

The `writer` Docker target and the `writer`/`writer-maintenance` Compose profiles
exist only to preserve future migration work and disposable recovery research.
They are not started, monitored, or supported by Certificate A. Writer safety
documentation remains historical until a separate Certificate B review defines
its release gates, operator approval, restored-clone drill, and serial canary
process.

See [ADR-005](decisions/ADR-005-certificate-a-production-boundary.md) and the
[production operations runbook](runbooks/production-operations.md).

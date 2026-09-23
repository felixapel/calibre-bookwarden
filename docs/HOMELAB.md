# Homelab deployment: Certificate A

Bookwarden's supported homelab deployment is the **Linux Docker Compose
Certificate A profile**. It audits a stopped Calibre library and lets a human
review sealed evidence. It cannot change Calibre metadata or covers. The
historical Calibre-Web sidecar, direct writer, and single-container Unraid/TrueNAS
templates are not supported production installers.

Start with [installation](../INSTALL.md) and use the
[production operations runbook](runbooks/production-operations.md) for actual
commands, backups, preflight, updates, and recovery. This guide explains how
that contract fits a homelab; it does not replace either document.

## Boundary to preserve

| Component | Access to Calibre library | Network exposure |
| --- | --- | --- |
| Verifier | The only library mount, `/library:ro` | Internal Compose networks |
| Web/API | No library mount | Host loopback by default; use reviewed same-host Caddy for TLS and authentication |
| PostgreSQL and Valkey | No library mount | Internal Compose networks |
| Optional Prometheus | No library mount | Host loopback by default (`127.0.0.1:19090`) |

The Compose profile also applies read-only container roots, dropped Linux
capabilities, bounded CPU/memory/PIDs, separate database roles, and immutable
release digests. A legacy web container with a read-only library bind is **not**
equivalent: it lacks the verifier separation, frozen source checks, and release
contract.

## Before every audit

1. Confirm a tested **whole-library restore** exists. A copy of
   `metadata.db` alone does not back up ebook formats, covers, or OPFs.
2. Stop Calibre Desktop, Content Server, Calibre-Web, and any other process
   that could write the same library. A read-only Bookwarden mount does not
   stop another container from changing the source.
3. Verify the exact library root and review the preflight result. Do not delete
   SQLite `-wal` or `-shm` files to make a check pass.
4. Run the bounded verifier only after the source is stable, then inspect
   readiness and evidence. Resume the usual readers/writers according to the
   maintenance plan after the audit finishes.

The [readiness checklist](production-readiness.md) records the remaining
deployment gates, including corpus calibration and external-writer exclusion.
Do not infer production readiness from a published image or a successful
development test.

## Installation shape

Use a dedicated Compose project and its `.env.example` contract; do not
reuse a generic homelab PostgreSQL/Valkey instance or copy a single-container
template. Pin the Certificate A image to the digest in the matching release's
`release-images.json` and set `BOOKAUDIT_RELEASE_DIGEST` to that same digest.
Keep the backend on loopback and protect any remote access through the reviewed
edge, VPN, and authentication described in the runbook.

Optional integrations such as Calibre-Web reconnect, LM Studio, Ollama,
Paperless, Tika, Gotenberg, and Qdrant are **not enabled** in Certificate A.
Enabling them in a development environment does not make them part of the
supported production profile. Certificate A may query Google Books and Open
Library with a checksum-valid ISBN; it does not send book text or images.

The optional monitoring profile pins Prometheus 3.13.3 LTS. Its two known
gRPC HIGH findings are a scoped, time-bounded release exception, not a clean
scan or a reason to expose Prometheus beyond loopback. See
[ADR-011](decisions/ADR-011-prometheus-lts-publication-exception.md).

## Updating or recovering

Use the [operations runbook](runbooks/production-operations.md) for a
maintenance window, immutable image comparison, database backup, migrations,
health checks, and rollback. Do not substitute a mutable `latest` tag, an
old wrapper mounted over the image entrypoint, or a single-container template.
If a health or schema contract fails, preserve evidence and restore the
matching image and database state rather than weakening the validator.

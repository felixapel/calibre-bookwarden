# ADR-004: Use a capability-limited Content Server adapter for live inventory

## Status

Accepted

## Date

2026-07-16

## Context

The live Calibre library can have an active SQLite WAL and can reside on a
FUSE-backed filesystem. Copying `metadata.db`, opening it through SSHFS, or
giving the existing `CalibreCLI` a remote URL would not establish a credible
read-only boundary. `CalibreCLI` intentionally exposes both reads and metadata
mutators, and its generic subprocess errors can include command details.

We need a first-pass inventory that reveals coverage and data-quality
distributions without persisting raw titles, authors, comments, identifiers or
paths, and without initializing the auditor database, providers, OCR or LLMs.

## Decision

Use a separate `ContentServerSource` with these constraints:

- accept only an HTTP loopback URL supplied by an operator-created SSH tunnel;
- require a stable source identity derived from a separately verified SSH host
  fingerprint (or equivalent), independent of the local tunnel port;
- authenticate with a Calibre account independently verified as read-only;
- send the password only through `calibredb --password <stdin>`;
- expose only `list`, exact metadata lookup and one-format `export` operations;
- reject malformed or ambiguous machine output before it enters the pipeline;
- export into a private, bounded temporary directory and delete it on every
  exit path;
- emit an aggregate-only `InventoryReport` by default.
- keep public providers disabled by default for remote verification and reject
  LLM/vision egress in this milestone; exact provider lookup requires explicit
  per-run consent.

The adapter does not inherit from `CalibreCLI` and cannot construct arbitrary
calibredb commands. The V2 pipeline materializes one format at a time, replaces
temporary paths with source-bound logical references before sealing, and
binds the snapshot to a hash of the complete remote record. It defers a book if
that record or its format set changes during inspection. Remote evidence is
rejected explicitly by the supervised writer boundary.

## Alternatives considered

### Direct SQLite or copying `metadata.db`

Rejected because a live WAL must be coordinated with the database and a direct
reader can create or update SQLite coordination files.

### SSHFS or a direct remote mount

Rejected because the project has no executable proof that Calibre performs no
lock or auxiliary writes through that path.

### Reusing `CalibreCLI`

Rejected because callers would receive metadata mutation capabilities on the
same object used for live access.

## Consequences

- An SSH/HTTP access log can still be written by the host; zero remote logging
  is not promised.
- The aggregate inventory can measure catalog quality without inspecting ebook
  content; `verify-content-server` is the separate persisted content workflow.
- Materialized files exist only in the configured private scratch directory for
  the active book and their local paths are not sealed or persisted.
- Remote runs are shadow-only and cannot be authorized or queued for writing.
- A missing read-only account or incompatible Calibre client blocks live use;
  the auditor does not create users or alter server configuration.

## Operational validation gate

The local disposable laptop gate passed on 2026-07-16 with the official, checksum-
pinned Calibre 9.11.0 client and server. The exact command was:

```bash
uv run python scripts/disposable_calibre_lab.py run
```

It validated password-stdin authentication, exact library selection, machine
JSON, five records and five formats, one-format-at-a-time export, five shadow
verdicts, rejection of `set_metadata`, an empty scratch directory, byte-
identical before/after regular-file paths, sizes, and SHA-256 values, and
automatic cleanup of the run's
containers, volumes, networks, and image. The generated fixtures and report
contain no live data. See the
[disposable lab runbook](../runbooks/disposable-calibre-lab.md).

This local gate approves the adapter mechanism, not the current commit's Gitea
validation and not an unattended live audit. Live
use remains blocked until the operator separately verifies the target host,
exact library ID, server version, read-only account, SSH tunnel, and explicit
per-run scope. The auditor must remain shadow-only.

# Disposable Calibre Content Server lab

## Purpose and boundary

This lab proves the remote, shadow-only audit path without connecting to a
homelab or mounting an existing Calibre library. It generates five CC0 test
records, starts the exact pinned Calibre 9.11.0 Content Server, runs inventory
and content verification, attempts a forbidden metadata write, attests the
relative paths, sizes, and SHA-256 values of every regular library file, and
removes all run-scoped Docker resources.

The lab Compose file has no host bind mounts, published ports, environment
files, host namespaces, devices, Docker socket, or live-library address. Its
default network is internal. Do not add any of those capabilities or reuse this
Compose file with production data.

## Prerequisites

- an x86-64 Linux laptop;
- a local Docker Engine selected through a Unix-socket Docker context;
- project dependencies installed through the locked `uv` environment.

The runner rejects `DOCKER_HOST`, `DOCKER_CONTEXT`, TLS and certificate-path
overrides before contacting Docker, then pins every command to the verified
local context.

The image uses the official Calibre Linux tarball and verifies the pinned
SHA-512 before extraction. Calibre recommends its official binaries instead of
distribution packages: <https://calibre-ebook.com/download_linux>.

## Isolated-runtime safety gate

The running lab has an internal-only network and the default audit makes no
public-provider requests. A first image build still needs network access to
download locked Python/system dependencies and the checksum-pinned Calibre
tarball unless they are already cached; this is not an offline build claim.

From the repository root:

```bash
uv run python scripts/disposable_calibre_lab.py run
```

The gate must report all of the following before it passes:

- Calibre client and server version `9.11.0`;
- five books and five formats, including one multi-format record and one record
  without a format;
- one remote shadow verdict per book;
- a rejected `set_metadata` probe using the read-only account;
- a successful local `set_metadata` control before server startup, proving the
  probe syntax itself is valid;
- only the wrapper's `--version`, `list`, and `export` operations; only `list`
  and `export` read the generated library;
- an empty private scratch directory;
- identical relative paths, sizes, and SHA-256 values for every regular library
  file before and after the run;
- completed Docker cleanup.

Cleanup is not inferred from `docker compose down` alone. The runner queries
containers, volumes, networks, and the run-tagged image and fails if any
project-scoped resource remains.

The sanitized, mode-`0600` gate report is written below
`reports/disposable-calibre/`. Reports are intentionally ignored by Git and do
not contain titles, authors, paths, comments, credentials, or raw provider
responses.

Calibre probes filesystem case sensitivity with a temporary create/unlink when
opening a library. Therefore the server receives write access to the generated
named volume. This does not weaken the host boundary: no host path is mounted,
Content Server local writes are disabled, the remote account is read-only, a
write rejection is required, and the entire disposable tree is hashed before
and after the audit.

## Optional provider connectivity smoke

The default gate makes no public-provider requests. To exercise only the
provider clients in a separate networked container:

```bash
uv run python scripts/disposable_calibre_lab.py run --with-web-smoke
```

This check sends one public ISBN to Google Books and Open Library. It is
explicitly non-gating, has no access to the library, credentials, or auditor
state, and reports only provider status and evidence counts.

## Retained diagnostic run

Use retention only while diagnosing the lab itself:

```bash
uv run python scripts/disposable_calibre_lab.py run --keep
uv run python scripts/disposable_calibre_lab.py cleanup --run-id bookaudit-lab-012345abcdef
```

Copy the exact run ID from the report. Cleanup rejects any ID outside the
`bookaudit-lab-<12 hex characters>` pattern. A retained run is not an approval
to attach host paths, expose ports, or inspect live data.

## Stop conditions

Stop and do not treat the gate as evidence if Compose validation fails, Docker
is remote, the architecture is not x86-64, the write probe succeeds, the
library-file attestation changes, scratch is non-empty, a command outside the
`--version`/`list`/`export` allowlist reaches the wrapper, or cleanup fails.

The relevant upstream interfaces are documented in the official
[`calibre-server` reference](https://manual.calibre-ebook.com/generated/en/calibre-server.html)
and [`calibredb` reference](https://manual.calibre-ebook.com/generated/en/calibredb.html).

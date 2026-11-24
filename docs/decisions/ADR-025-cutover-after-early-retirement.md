# ADR-025: Cut over against Certificate A's archive, since it was retired first

- Status: Accepted (M6)
- Date: 2026-10-20
- Decision: approved in the M6 implementation plan

## Context

Spec section 17 planned the cutover for a running Certificate A: a shadow run
compared with its results, then its retirement, and a two-week window in which
its stopped containers could be started again. Felix had Certificate A retired
on 2026-10-12, before bookwarden was deployed. Its containers, networks,
volumes and images were removed. A final `pg_dumpall`, the volumes, the
compose directory, the 51 GB library snapshot and the images were archived on
an external disk on the server, and the nightly database dump stopped
covering it. An agent VM's tunnel key and its unit were left in place.

## Decision

**Compare with the archive.** The shadow run is compared with Certificate A's
last complete whole-library run in the final dump, `verify_3c480a0d…` of
2026-10-06 (3,173 books). The dump is restored into a throwaway Postgres
container, one JSON line per book is exported (Calibre book id, tier, risk
flags, and per format its name, SHA-256, status and the one ISBN it kept),
and the container is deleted. The export holds no titles and stays on
the server. The archive gets a `SHA256SUMS`, created and verified, since it
is now the only way back.

**A read-only `parity` command.** `bookwarden parity --run N --certificate-a
FILE` (or `-` for stdin, since the container is read-only) walks the run in
one report snapshot, so every package is seal-checked once, and writes one
CSV row per difference:
- a book only on one side, or one bookwarden couldn't check;
- different format names, showing Certificate A's risk flags when it read no
  formats;
- a changed file, or one bookwarden couldn't read;
- for unchanged files, a state disagreement, or a Certificate A ISBN that
  bookwarden found neither in the file's metadata nor in its text-layer or
  OCR hints. Certificate A kept one ISBN per format, mostly from the text
  (an EPUB's front matter, a PDF's edition page), so the two sets aren't
  compared whole.

Tiers are counted, not listed: ADR-020 changed the rules. The library
changed after 2026-10-06, so a changed file explains every other difference
of that format.

**Findings aren't compared.** Spec section 17 also names findings. Certificate
A's risk flags grade identity confirmation (manifestation and expression
evidence), while bookwarden's findings name file and metadata defects, so
there is no mapping between them that a difference could test. Files, their
states and ISBNs are the facts both recorded. Certificate A's flags appear only
to explain a book it read no formats for.

**Status mapping.** Certificate A's `readable` is bookwarden's `ok`; `corrupt`
is `corrupt`; `unsupported` is `unsupported`, `not_inspected` or `drm`.

**No rollback window.** There are no stopped containers to start again. A
rollback means restoring the archive, which the archive's note describes.
Undoing library changes is still done in bookwarden first.

**Backups need no change.** The nightly job runs `pg_dumpall` on
`postgresql18`, so the `bookwarden` database is covered once it exists; the
cutover only confirms it in the first dump after.

## Consequences

- Differences caused by library changes since 2026-10-06 show as
  `only_certificate_a`, `only_bookwarden`, `formats` or `file_changed`, and
  are explained by that date alone.
- A `state` or `isbn` difference on an unchanged file needs a reason before
  writes are turned on; one without a reason is a bug.
- The `certificate-a-final` tag, the merge into `main` and the tunnel key
  removal still follow section 17.

# ADR-002: Exact-manifestation metadata verification

## Status

Accepted

## Date

2026-07-14

## Context

A Calibre record can name the correct work while still describing the wrong
edition, translation, publisher, publication date, series position, or cover.
Title similarity and an LLM guess are therefore insufficient for unattended
metadata correction. The auditor also has to handle several formats attached
to one Calibre book, protected or corrupt files, scanned PDFs, provider
disagreement, privacy constraints, crashes, and changes made in Calibre after
review.

The current Calibre record is the subject under audit. Treating it as
corroborating evidence would make the decision circular. OCR, vision, and LLM
responses can also be wrong or correlated even when they appear confident.

## Decision

Manifestation V2 is the default `verify` contract and follows these rules.

1. A run freezes its ordered Calibre membership. It processes exactly one book
   at a time and inspects every attached format before moving to the next book.
   A resumed run rejects changed membership and skips only terminal books with
   a persisted evidence package.
2. Original files are hashed and never modified by extraction. EPUB, PDF, and
   CBZ have bounded native parsers. MOBI/AZW3 and CBR use temporary conversion
   when `ebook-convert` is available. DRM, corruption, unsupported formats, and
   parser failures are explicit evidence states.
3. ISBN is a manifestation identifier only after checksum validation. Native
   edition-bearing content is preferred. One OCR engine or cover vision may
   seed an exact ISBN provider query for review, but is non-authoritative. Two
   distinctly named OCR engines must agree before `ocr_consensus` becomes
   authoritative; they still share the `book_content` independence root.
4. Google Books and Open Library are queried only by the exact ISBN candidate.
   Returned records are accepted only when their structured identifiers contain
   that same checksum-valid ISBN. Redirects, non-HTTPS URLs, unlisted hosts,
   private DNS results, oversized responses, and invalid schemas fail closed.
5. Identity uses three deterministic tiers:

   - **Tier A**: every format is readable; all formats identify one ISBN and
     have complete, agreeing title/author/language data; internal content and
     one exact structured external record agree on ISBN and those core fields.
   - **Tier B**: evidence is missing or incomplete, including a single OCR or
     vision candidate, an unreadable format, or absent core fields. Human review
     is required and no automatic patch is produced.
   - **Tier C**: formats or exact external records conflict. The book is
     blocked/deferred and no patch is produced.

6. A field enters the canonical patch only when two independent roots agree on
   the exact manifestation value and at least one root is internal or official.
   Calibre-current, LLM, vision, and `calibre_fetch` observations never promote
   a decision. Independence is counted by `independence_root`, not by response
   count.
7. Remote text and images are disabled in configuration by default. Remote LLM
   text and remote vision each require both an administrative setting and
   per-run consent. Only bounded title/copyright snippets are eligible for text
   egress; body text is excluded. Every allowed request stores a content-free
   receipt with counts, provider, locality, and payload hash.
8. Every evidence package is strict-schema, versioned, SHA-256 checksummed, and
   durably linked to its run, book snapshot, state transitions, sources, privacy
   receipts, and proposed patch. V2 evidence never masquerades as a V1
   `BookVerdict`. The checksum detects accidental or out-of-contract changes
   inside the trusted database boundary; it is not a signature and does not
   authenticate a database administrator.
   Sealing and later loading recompute a Tier A resolution from the stored
   formats and source provenance; a caller cannot promote a hand-labeled Tier A
   identity merely by supplying that enum value.
9. The current public V2 write flow is supervised: an operator authorizes one
   exact sealed Tier A package, then explicitly queues that evidence ID with
   `force=true`. A disabled-by-default pilot gate binds the operation to one
   persisted pilot ID, canonical library-root hash, immutable release digest,
   Alembic head, and a maximum budget of five operations. Queueing is serialized
   by one fixed PostgreSQL transaction advisory lock before the exact pilot row
   lock, so different proposed pilot IDs cannot race. Any nonterminal operation
   or unacknowledged unpublished outbox event blocks the next reservation.
   Reservation count must equal the number of pilot-bound ledger rows. The sole
   writer revalidates the complete configured and
   persisted pilot binding, that count, package seal, run/book
   identity, field locks, authorization hash, canonical patch, live pre-write
   values, exact format membership, the sealed library root, and every live ebook
   SHA-256.
   Ebook paths are opened beneath that root with descriptor-relative no-follow
   traversal for every component, closing parent-directory substitution races.
10. Calibre writes use only the canonical adapter fields: title, authors,
    namespaced identifiers, languages, publisher, pubdate, series,
    series_index, `#edition`, and a local manifestation-bound cover artifact.
    OPF, custom-column, and cover rollback artifacts are hashed and verified.
    Artifact trees use the same descriptor-anchored traversal. Verified OPF and
    cover bytes are copied to Linux sealed memory descriptors and inherited by
    `calibredb`, so the writer never asks Calibre to reopen a verified pathname.
11. Tier A automatic application is unavailable. The feature flag and calibration
    report are advisory inputs only and cannot unlock a writer. Reports are not
    signed attestations: they help measure a reviewed corpus but do not authorize
    unattended writes. The legacy direct CLI apply and `POST /api/apply` paths are
    also disabled; only exact, manually authorized V2 packages can be queued.

## Consequences

- The system prefers an honest Tier B result over guessing an edition. A book
  without an exact identifier will not fall back to title-only web search.
- Multiple provider responses do not automatically mean multiple independent
  truths. Google Books and Open Library are separate roots; duplicated records
  from one provider count once.
- OCR and vision improve discovery and review coverage without silently
  becoming authority. Scanned books may remain Tier B when core fields cannot
  be established from the file.
- `#edition` writes require that custom column to exist in Calibre. Cover writes
  require a pre-materialized, image-validated artifact inside the configured
  artifacts directory. V2 does not currently auto-download a provider cover.
- Google Books and Open Library are the implemented external adapters. The
  source contract supports national-library and official-publisher evidence,
  but those adapters are future work.
- The WebUI review surface is V2-native: it renders sealed evidence and exact
  current-versus-patch data, authorizes one package, and queues one operation.
  Tier B/C controls are disabled. V1 records remain historical/read-only in the
  UI; the legacy apply endpoint remains permanently retired.
- An operator can close one exact persisted pilot with `bookaudit pilot-stop
  PILOT_ID --yes`. Closing shares the row lock used by queue reservation and
  makes queued work fail writer revalidation; it does not replace stopping a
  writer that may already be inside an external Calibre call.
- A separately permissioned, append-only incident row may acknowledge one
  reconciled terminal V2 `failed` operation only after locking and confirming
  its exact pilot is stopped. It preserves ledger/outbox history, never applies
  to `unknown` or `restore_failed`, does not refund budget or reopen the stopped
  ID, and is the only historical-failure exception for a distinct next pilot.
  Queueing and alert suppression independently join the acknowledgement back to
  the completed failed operation, failed outbox, absent lease and stopped
  historical pilot; a standalone or forged row is insufficient.
- Migration downgrades refuse to discard persisted V2 evidence or active V2
  writer/recovery records.
- Shadow verification is portable, but the supervised writer is deliberately
  Linux-only because immutable artifact handoff depends on `/proc/self/fd`,
  inherited file descriptors, and `memfd` sealing.

## Alternatives considered

### Let the LLM identify and correct the book directly

Rejected because model confidence is not provenance, model calls can be
correlated, and a plausible work-level answer can select the wrong edition.

### Use the current Calibre ISBN as the lookup key

Rejected because that is precisely one of the values being audited. It can be
used as context but cannot confirm itself.

### Apply every Tier A patch immediately

Rejected until a representative, current gold corpus demonstrates zero false
automatic writes and the privileged applier boundary is explicitly enabled.

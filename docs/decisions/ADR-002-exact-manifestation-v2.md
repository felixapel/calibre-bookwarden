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
9. The current public V2 write flow is supervised: an operator authorizes one
   exact sealed Tier A package, then explicitly queues that evidence ID with
   `force=true`. The sole writer revalidates the package seal, run/book identity,
   field locks, authorization hash, canonical patch, live pre-write values, exact
   format membership, the sealed library root, and every live ebook SHA-256.
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
- The WebUI still renders the V1 review shape. V2 is fully available through
  CLI verification and the API review/apply endpoints; a dedicated V2 WebUI is
  not part of this decision.
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

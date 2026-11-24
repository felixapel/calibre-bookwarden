# ADR-020: Derive tiers and per-field proposals from sealed files and identity records

- Status: Accepted (M3.2)
- Date: 2026-10-18
- Decision: approved in the M3.2 implementation plan

## Context

M3.1 seals each source's validated records next to the file evidence. Spec
section 7 keeps ADR-002's tiers and field rule: Tier A when the files and one
exact record agree on ISBN, title, authors and language; Tier C when formats or
exact records conflict; a field is proposed only when two independent roots
agree on the exact value and one of them is internal or official. Tier B books
get proposals for fields that pass on their own; Tier C books get none. The
spec doesn't say what a root is for a source with several records, which ISBN's
records count, or how values compare.

## Decision

**Roots.** All metadata-eligible formats together are the internal root `file`.
DNB and BNE are official roots. Open Library and Google Books are external
roots; an Open Library record whose source records all come from one national
library joins that library's root without giving it official standing. Several
records from one source are one root. A root whose members state different
valid values for a field is split for that field. OCR and text-layer hints are
never roots, and Calibre's current values are never evidence.

**Identity.** Only records for the ISBN the files establish count: every
eligible format that states an ISBN states exactly one valid ISBN, they agree,
and no ISBN field is capture-flagged. Records reached through Calibre's ISBN or
a PDF hint are shown but grant nothing.

**Comparison** uses ADR-017's rules (NFC, Unicode case fold, whitespace,
ordered authors, primary-language sets, full dates), exported from
`internal/findings`. A record value at M3.1's capture cap (511 runes or more, a
16-item list) is unverified. A record's title is its title without subtitle.

> Amended by [ADR-030](ADR-030-record-match-tolerances.md): from decision version 5, a record agrees with the files for Tier A despite stating no language, listing extra people, keeping the subtitle in its own field, or differing by a trailing parenthetical that names no volume, a leading English article or "&" for "and". Records still conflict with each other by these comparison rules. From decision version 7 they conflict only after the further tolerances of ADR-030's amendment "Decision v7 tolerances".

**Tiers** carry sorted reason codes. C: two eligible formats disagree on ISBN,
title, authors or languages, or two identity records disagree on title, authors
or languages, whether from one source or two. B: no formats, an unreadable format, a
missing or ambiguous ISBN, an incomplete core field, no identity record, or no
identity record matching the files. A: no reason at all.

**Field rule.** A value is proposed when the tier isn't C, at least two roots
state it, one of them internal or official, no internal or official root states
another valid value or is split (the veto), and its key differs from Calibre's
or Calibre's is missing or invalid. Fields: title, authors, languages, isbn,
publisher, pubdate. Languages are proposed as ISO 639-3 codes, dates only as
full dates. An ISBN is proposed as the identity ISBN when Calibre's differs and
a record from another root agrees with the files' title. The displayed value
comes from the first supporting root in the order dnb, bne, file, openlibrary,
googlebooks.

**Sealing.** The package gains `decision_version` (1), `tier`, `tier_reasons`
and `proposals`, all omitted when empty, so older packages keep their seals.
Every load verifies the findings, recomputes the decision and requires an exact
match for version 1; version 0 derives the decision in memory. Changing these
rules, the shared comparison helpers or the Open Library root-sharing table
needs a new version and a historical derivation. Proposals live in the sealed evidence; the `proposals` table, with
approval status, arrives with guided fixing (M4). The Fixes list is read-only
until then.

## Consequences

- Books whose files carry no ISBN get no proposals, whatever Calibre's ISBN or
  an OCR hint finds.
- Initials formatted differently ("J.K." vs "J. K.") neither agree nor can be
  merged as keys. Between two records they count as a conflict, which puts the
  book in Tier C and blocks all its proposals. The shadow run
  measures how often; a later version can relax it.
- Subtitles aren't compared or proposed, and differences only in case or
  whitespace are never proposed.
- Year-only dates, which most sources give, are never proposed.
- Publishers compare after normalization only; "Carlsen" and "Carlsen Verlag"
  don't agree.
- The source cache gains a mapping version, so a parser change can't replay
  records mapped by older rules for 30 days.

> Amended by [ADR-028](ADR-028-sort-fixes-from-calibre-rules.md): title and author sort proposals restate Calibre's own rules with the single root `calibre_rule` and are made in every tier, Tier C included.

> Amended by [ADR-045](ADR-045-decision-v10-text-root-corrections.md): from decision version 10, a record root supports or dissents on a title or authors proposal only when a language it states is the files' and it lists every file author (rule 4).

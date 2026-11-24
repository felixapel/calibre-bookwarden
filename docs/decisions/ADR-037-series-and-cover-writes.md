# ADR-037: Series and cover writes

- Status: Deferred (2026-11-08)
- Date: 2026-11-04

## What it would decide

1. **Series and series index by proposal.** A series name and index would
   become a proposal under the field rules of ADR-020, approved by the admin
   and written through the content server with readback and undo, in a
   decision version of its own.
2. **Covers through a manual deck.** A better cover candidate (ADR-043) would be
   shown beside the current one on a triage page, and replaced only by the
   admin's explicit choice, never by a bulk approval.
3. **Tags and comments** stay manual or fill-empty only, from verbatim CC0
   descriptions or a deterministic subject mapping (the v2 design's table);
   author renames stay manual.

## Why it is deferred

Run 16 (the first decision v9 / findings v6 run, 3,212 books) showed series
proposals would be almost empty: no root but the recorded-only ones (Wikidata,
Hardcover) carries a series, so two roots rarely state one.
`cover_better_available` fired for 5 books. Neither justified the new write
paths before v1.0 (the spec's §2.4 amendment, 2026-11-08). The series and cover
read paths and their findings stay as a manual worklist.

## What is already known

Spike S1 measured how Calibre 9.16's content server stores a series, its index
and a cover on write; the results are in
[`docs/calibre-protocol.md`](../calibre-protocol.md) ("Series, series index and
covers").

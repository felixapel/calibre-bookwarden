# ADR-028: Title and author sort fixes restate Calibre's own rules

- Status: Accepted
- Date: 2026-10-26
- Decision: approved in the sort-fixes plan; extends ADR-020's proposals and ADR-021's writes

## Context

Run 4 on the real library flagged 454 wrong title sorts and 44 sorts that
keep a leading article, such as "The Captive Mind" sorted as "Innocent
Captive". It also flagged 58 author sorts. 27 of those already equal
Calibre's own value: the author-sort finding follows the legacy authority
rules (M2.3), which write "John le Carré" as "le Carré, John", while Calibre
writes "Carré, John le". The other 31 really differ, such as Cormac McCarthy
sorted as "Road, The".

A sort isn't a fact about the book. Calibre derives it:
- The title sort is Calibre's `title_sort` of the title, for the book's
  language.
- The author sort is each author's sort in Calibre's authors table, joined
  with " & ". A new author's table entry is `author_to_author_sort` of the
  name.
- Calibre recomputes both when the title or authors are written (pinned in
  `TestCalibreWriteBehaviour`).

Some table entries are themselves wrong: "Maurice Obstfeld" is not
inverted. Others were customised on purpose, such as "Lonely Planet".

## Decision

**What is proposed.** Decision version 4 adds two fields:
- `title_sort` is Calibre 9.16's `title_sort` of Calibre's title and first
  language (the M2.4 port). It is proposed when the title-sort finding flags
  the current sort as missing, with the article not moved, or wrong.
- `author_sort` is the book's authors' entries in Calibre's authors table,
  joined with " & ". It is proposed only when every entry is Calibre 9.16's
  default `author_to_author_sort` of the name, ported and pinned to golden
  values from Calibre itself. A customised or broken entry is never copied
  onto a book.

**Roots.** The only root is `calibre_rule`. No source, file or model supplies
the value: it is what Calibre computes from its own record. Proposals are
made in every tier, because they don't depend on the files or sources.

**Never beside a proposal for a field the rule reads.** That is the title
or languages for the title sort, and the authors for the author sort. A
title or authors write makes Calibre recompute the sort from the new value,
and a sort computed from the old title or language would be wrong: "Der
Prozess" sorted by the English rule beside a proposal to make it German.

**Capture.** bookwarden reads `author_sort_map` from `/ajax/books` into the
record as `author_sort_map`. Packages sealed before this change have none,
so they keep their bytes and seals, and they get no author sort proposal.

**Writes.**
- `title_sort` writes Calibre's `sort` field and `author_sort` writes its
  `author_sort` field.
- A sort written without its title or authors is restored alone.
- The stale check and undo compare records without the authors table when
  the recorded record has none. Operations from before this change stay
  undoable.

## Consequences

- The legacy `author_sort_mismatch` finding stays as an observation. In run
  4, 27 books are flagged with no proposal, because their sort already
  equals Calibre's table.
- A wrong entry in Calibre's authors table is fixed once in Calibre's Manage
  Authors. That is a library-wide edit, outside guided fixing.
- Approvals from runs sealed before this change still apply. Their record
  has no authors table, so the stale check compares the record without it,
  and their proposals don't depend on it (`TestApplyBeforeMapCapture`).
- Changing an author's sort in Manage Authors changes the record of every
  book by that author, so their open proposals go stale too.
- Spec section 7's two-root rule and ADR-020's "Tier C gets no proposals"
  cover field proposals. Sort proposals, which only restate Calibre, are the
  one exception to both.

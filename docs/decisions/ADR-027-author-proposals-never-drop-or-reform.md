# ADR-027: An authors proposal never only drops or re-forms Calibre's authors

- Status: Accepted
- Date: 2026-10-26
- Decision: approved in the author-proposals plan; amends ADR-020's field rule

## Context

Run 3 on the real library had 15 Tier A authors proposals, and every one
of them either dropped a name Calibre lists or changed how a name is
written:
- real co-authors (Ariel Durant, Jonathan M. House), translators and
  introducers dropped, because the files and Open Library usually list the
  main author alone;
- one book kept a wrongly added name and dropped the real author;
- "Walter Scott" → "Sir Walter Scott", "Anthony Robbins" → "Tony Robbins",
  middle initials added or dropped.

A name-form change written to one book splits the author in Calibre: that
book gets a new author item while the author's other books keep the old one.
Renaming an author is a library-wide edit in Calibre, outside guided fixing.

## Decision

**The guard.** There is no authors proposal when every proposed author's
surname is already the surname of one of Calibre's authors, counting repeats.
The surname is a name's last case-folded word, split as ADR-026 splits
titles. Such a proposal drops names, reorders them or changes their form; it
can also name a different person with the same surname, and that is
suppressed too (see Consequences).

**What stays proposed.** A proposal that brings in a surname Calibre doesn't
list, or one more name with a surname Calibre lists: a different author, an
added co-author (Ariel Durant next to Will Durant too), or the right form of a
name Calibre has surname-first ("Rowling, J. K." → "J. K. Rowling").

**Versions.** Decision version 3. Versions 1 and 2 keep their rules, and the
version checks are a range, so a later version can't drop an earlier one by
omission.

## Consequences

- An editor that both the file and Open Library list as the author is still
  proposed, with both values shown (two books in run 3).
- Name standardisation stays Felix's library-wide rename in Calibre.
- A Calibre author list that misses a real co-author still gets the proposal
  that adds them.
- A wrong author whose surname matches the right one's, such as a relative or
  an editor (Charlotte for Emily Brontë, Christopher for J. R. R. Tolkien), is
  not proposed and stays a manual fix in Calibre. Suppressing writes nothing,
  so it fails closed; a tighter test would let the run-3 re-forms through.

# ADR-024: Report possible duplicates as a re-verified projection of each run

- Status: Accepted (M5.2)
- Date: 2026-10-20
- Decision: approved in the M5.2 implementation plan

## Context

Spec section 10 lists four duplicate checks: the same ISBN on two books, the
same file hash on two books, the same normalised title and author, and
near-identical covers by perceptual hash (a hint only). ADR-010 says duplicate
suggestions never authorize merging or deletion. Findings are derived from one
sealed package at a time (ADR-017), but a duplicate is a relation between
books, so it can't be sealed into either package without making each seal
depend on the rest of the library.

## Decision

**Within one whole-library run.** Duplicates are compared among the books one
run checked. A one-book recheck has nothing to compare with. A cancelled or
failed run shows the groups among the books it did check, and says so. Runs
created before M5.2 say they predate duplicate detection.

**Four kinds, from verified evidence only.**
- `isbn`: a checksum-valid canonical ISBN-13 from Calibre's `isbn` identifier
  or from a format's embedded metadata, within its capture limits. PDF
  text-layer and OCR hints and source lookups never count.
- `file`: the same format SHA-256 on two books. Empty files and formats whose
  bytes weren't read are skipped.
- `title_author`: Calibre's title and authors, both valid under the M2.3
  comparison rules. The stored key is a SHA-256 of the two keys, so the text
  stays in the sealed package.
- `cover`: difference hashes within Hamming distance 4. This is a hint only.

**Which covers compare.** A decoded cover that isn't a version 4 placeholder
and whose hash has 8 to 56 bits set. Near-blank pages and smooth gradients
have hashes with almost no bits set (or almost all), and would match each
other without meaning anything.

**Cover grouping.** The hash is split into five bands of 13, 13, 13, 13 and 12
bits. Two hashes within distance 4 agree on at least one band, so candidates
come from equal bands and are confirmed by their full distance. Groups are
connected components; chains are acceptable for a hint. A Book page reads
only the book's own groups: the rows of its own keys, and the covers within
distance 4 of its own (direct neighbours, not the run's chains).

**A projection, re-verified.** `FinishBook` writes each book's exact keys to
`duplicate_keys` and marks its `covers` row comparable, in the same
transaction as the findings. Groups are built when a page is read. Every
member a page shows is loaded through the report snapshot, so its seal is
checked, and its package must still produce the group's key or cover hash;
otherwise the page is an integrity error. The Book page checks the book's
whole set of keys.

**Versioned per run.** `runs.duplicates_version` records the rules a run was
checked under (1). Another kind, threshold or key format needs version 2;
pages for an unknown version say the duplicates aren't available.

**No writes, no merges.** Duplicates create no proposals and call no writer.
The page links each book to its Book page and says merging is done in Calibre.

## Consequences

- Findings stay at version 4, and no seal or package changes.
- Groups are recomputed on every page view: one `GROUP BY` for the exact
  kinds, and banded matching over the run's comparable covers.
- A page shows at most 25 groups and 10 members of each, so a bogus ISBN shared
  by hundreds of books costs at most 10 package loads per group.
- Members past the first 10 of a group aren't re-verified until shown on their
  own Book page.
- A cover lookalike with a very plain hash isn't reported at all.
- Only the members a page shows are re-verified. The per-kind counts, and the
  absence of a group (a deleted row, a cleared comparable flag), aren't; the
  Book page of a book whose rows were deleted is an integrity error.
- Banding 50 000 covers, 30 % of them sharing a band value, takes about 0.9 s
  per Duplicates page view (`BenchmarkGroups`); a Book page doesn't band.

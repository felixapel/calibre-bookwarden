# ADR-032: Read ISBN hints from EPUB front matter, with their context

- Status: Accepted (parts 1 and 2)
- Date: 2026-10-29
- Decision: approved in the Bookwarden v2 plan (M1); extends ADR-018's hints

## Context

ISBN hints seed exact-ISBN lookups and are never authority (spec section 7).
Until now they came only from PDFs: pages 1 to 5 and the last 5, with OCR
always in English. On the real library that misses a lot. 324 EPUBs print an
ISBN in their front matter that bookwarden never reads, and 56 PDFs have one
on pages 6 to 12. Later work, such as matching the ISBN of the edition as
printed in the file and telling the e-book ISBN a copyright page names from
its print edition's, needs more than the number: where in the book it was
found and what label sits next to it.

## Decision

**EPUB front matter.** The EPUB inspector scans the front matter and the last
3 spine documents for ISBNs. Front matter is every spine document before the
first body landmark (EPUB 3 nav `landmarks`, EPUB 2 `<guide>`: `bodymatter`
or `text`), the first 6 if there is none, and at most 12. The scan reads the
first 32 KiB of text of each document (the per-document window, see the
amendment below) and at most 15 x 32 KiB across the scan, under the zip limits
and the file's deadline. A scan that hit the total bound, the per-document read
limit, a document it couldn't parse or a navigation document it couldn't read
is `failed`, never `ok`; a document cut at the window is not one of those. The scan stops 5 seconds before the file's
deadline: if less is left it doesn't start and the hints are `timeout`, and if
it runs out, the hints are `timeout` while the format keeps its own result and
metadata. Encrypted and DRM EPUBs aren't scanned. The hints have method `epub_text`, and `pages`
holds the 1-based spine positions read. Verify takes them from the inspector,
so they cost no OCR and no tool time.

**Context.** Each hint ISBN has an entry in `context`, in the same order: the
landmark of the document it was found in (`copyright`, `title`, `front`,
`back`, or empty for a PDF) and a qualifier from the label next to it
(`ebook`, `pdf`, `hardcover`, `paperback`, `print` or empty), found by a closed
table of English, German and Spanish terms in the 40 characters before the ISBN
or in parentheses after it. The window stops at a line break, and a term after
the ISBN on the same line, before the next ISBN's label, labels it too. An ISBN seen in several documents keeps its first
sighting, but a copyright page's context wins. Part 1 stores no book text, only
ISBNs, positions and these two closed sets; part 2 below adds bounded excerpts.

**PDFs.** A text layer is read from pages 1 to 12 and the last 3, which is
cheap. OCR keeps pages 1 to 5 and the last 5. OCR runs in the book's language,
from Calibre's record: `eng`, `deu` (also `ger`) or `spa`, the models in the
image; anything else, or no language, is `eng`. The model used is recorded as
`lang`. The OCR self-check stays `eng`.

**Schema 2.** The evidence schema version becomes 2: `context`, `lang` and the
`epub_text` method are new, all optional. Schema 1 packages still open and
derive exactly as before. A schema 1 package that holds a schema 2 field is
refused as invalid evidence, and so is a context that doesn't follow its ISBNs
or leaves the closed sets.

**Still hints.** Nothing about authority changes. `epub_text` seeds lookups as
its own class like `text_layer` and `ocr`; records found through it are shown
and never back a fix.

### Amendment: a per-document scan window (2026-11-01)

Run 10 sealed `epub_text` hints as `failed` for 527 of 3,970 EPUB formats
(13 %). A random sample of 20 of them, read with `bookwarden inspect`, showed
all 20 failed as `text_budget`. Those EPUBs have no landmarks, so "front matter"
is the first 6 spine documents, and these are often whole chapters; one book
(2332) has its entire text in one file, so no document was even scanned
(`pages = []`). Together they exceeded the 256 KiB budget, and the scan was
marked `failed` even when the copyright page had been read completely and held
an ISBN (8 of the 20 did). A failed scan also drops the copyright-page identity
of ADR-035 (decision v6).

**Window.** Each spine document contributes at most 32 KiB of text to the ISBN
scan (`docScanWindow`), cut at a rune boundary, with a trailing run of digits,
`X`, spaces and hyphens trimmed so that a cut ISBN-13 can't leave a valid
ISBN-10 prefix. Front pages are usually small, so an ISBN at the start of a long
chapter file is still found. **Documents whose landmark is `copyright` or
`title` are never windowed.** A landmark may carry a `#fragment` (the scan drops
it), so it can label a whole file, and the ISBN labelled `(ebook)` can sit
anywhere in it; cutting it would let v6 pick a print ISBN as the identity. Such
a document is read whole, as before, or the scan fails with `text_budget`. Only
`front` and `back` documents (and unlandmarked ones, which are `front`) get the
window.

**A window cut is not a failure.** The scan stays `ok` when the window is the
only bound hit. The document counts as read: its position is in `pages`, and
the inspector's diagnostics list it as not whole with the reason `doc_window`
(`ScanDoc.Reason`, shown by `bookwarden inspect` as `(window)`). `doc_window` is
never the scan's `FailReason`, and nothing about it is sealed. Everything else
fails as before: an XML error, the 4 MiB per-document read limit, a navigation
document, zip limits, DRM, and the deadline. The read limit fails a document
only when it cut it before the 32 KiB window was filled; a window filled
earlier makes the document `doc_window` and the scan stays `ok`, since the
later cut lost nothing the scan needs. After the window cut the scan keeps
tokenizing the document's read bytes without collecting text, bounded by the
read limit and the deadline, so a parse error later in the document still
fails the scan with `xml_error`, as before; the one exception is the unexpected
EOF that the read-limit cut itself causes, once the window is filled.

**Total bound.** Ruling: the total grows to 15 x 32 KiB = 480 KiB (option b),
the most that 12 front documents plus 3 back ones can contribute at the window.
The simplest rule that never fails on the ordinary case of up to 15 documents
each cut at the window; no per-document arithmetic is needed. The total is now a
safety net for the windowed documents, and reaching it is still `failed` with
`text_budget`. A long `copyright` or `title` document, which is not windowed,
can reach it. Options (a), keep 256 KiB, would fail any book with 8 long
documents, and (c), a window of the remaining budget over the remaining
documents, makes what a document contributes depend on its neighbours.

**Excerpt.** Unchanged. It is built from the same bytes with its own bound
(48,000 bytes of text per document, 12,000 runes in all), which the window
doesn't touch. A document cut at the window or at the excerpt's own bound sets
`truncated`; `complete` is cleared only by a document that wasn't read or didn't
parse.

**Evidence.** The format and the schema are unchanged, and so are findings and
decide: packages sealed before this amendment replay from their sealed data
exactly as before. Only the inspector's behaviour for new runs changes.

**Trade-off.** An ISBN printed deeper than 32 KiB into a document isn't seen.
That is not front-matter text in practice for `front` and `back` documents: an
ISBN deep in a chapter is a citation of another book. Copyright and title
documents, where it could be, are not windowed.

## Part 2: bounded front-matter excerpts

Each format of a book gets a bounded text excerpt of its front matter, so that
an admin can see where a hint came from. Later work (M4) may feed the excerpts
to grounded LLM extraction. This decision only stores, seals and shows them:
nothing in findings or decide reads an excerpt, and replaying stored packages
at every findings and decision version stays byte-identical.

**Content.** An EPUB's excerpt is the text of the spine documents the
front-matter scan above reads, in its order: front matter, then the last 3.
The text is that of the XHTML body: block elements (`p`, `div`, `h1` to `h6`,
`li`, `tr`, `section` and so on) break the line, `br` and `hr` break it once,
`head`, `script` and `style` are skipped, entities are decoded, and the
source's own line breaks and runs of white space count as one space, as in a
browser. A PDF's excerpt is the text layer of the pages read for hints (pages
1 to 12 and the last 3, from `pdftotext`), in page order, one unit per page.
OCR text is noisy and is never stored; it stays hint-only. Other formats get no
excerpt. A format whose pages hold no text, or whose scan didn't run (DRM,
encrypted, a PDF with an unverified text layer), has none either.

**Bounds and normalisation.** At most 12,000 runes per format. A cut is made
at a rune boundary, at the last line break inside the bound when that is
within 200 runes of it, and sets `truncated`. The EPUB scan also reads at most
48,000 bytes of excerpt text from one document, backing off to the start of a
rune, so that a cut never splits a character; a document cut there, or at the
4 MiB read limit for one XML document, sets `truncated` and keeps its text so
far, and later documents are still read (a copyright page often follows a huge
first file, and the cut is the same on every run). The stored text is NFC with LF
line endings, no control characters (NUL included), lines trimmed, white space
inside a line collapsed to one space and at most one blank line in a row. Two
units are joined by one blank line, which belongs to neither. Format and bidi
characters stay in the stored text, so that offsets stay true; the book page
strips them (Unicode category Cf, which includes U+202A to U+202E and U+2066 to
U+2069).

**Package, schema 3.** A format's evidence gets an optional `excerpt`:
`method` (`epub_text` or `text_layer`), `units` (a list of `{pos, landmark,
start, end}`: the 1-based spine position or page, the landmark from the closed
set above and empty for a PDF, and rune offsets into the excerpt), `sha256`
(lowercase hex of the UTF-8 text), `chars`, `truncated` and `complete`. The
evidence schema version becomes 3.

`truncated` means a bound cut some text: a document's read limit or the
12,000-rune cap. `complete` is true only when every document or page range the
scan meant to read was read. A document that fails to read or parse adds none
of its text and clears `complete`, and so does a PDF page range that fails or
doesn't line up with its pages (`pdftotext` must end each requested page with
one form feed); the units that remain are the documents or pages that were
read. A scan that times out seals no excerpt at all, for an EPUB and a PDF
alike, because a deadline that depends on how long the download took must never
decide which text is sealed; its hints keep their own `timeout` state. A
document the full excerpt no longer needs is not counted as missed. `complete`
is required in the package, never defaulted.

Open also requires an `epub_text` unit to have a landmark and a `text_layer`
unit none, and the method to fit the format: `epub_text` on EPUB and KEPUB,
`text_layer` on PDF (Calibre's `ORIGINAL_` prefix ignored), and no excerpt on
any other format. A PDF stored under an EPUB name keeps its hints, and its
excerpt is left out. One hash named by two formats with different `chars` is
refused. Schema 1 and 2 packages open exactly as before. A schema 1
or 2 package that carries an `excerpt` is refused as invalid evidence, as is an
excerpt with a method outside the set, a hash that isn't 64 lowercase hex
characters, a landmark outside the set (or any landmark on a text layer), a
length over the bound, or units that are empty, out of order, overlapping or
past `chars`.

**Storage.** The text is not part of the package: only its hash is, so the
package stays small and the seal covers the text through the hash. It lives in
`excerpts (sha256 PRIMARY KEY, body, chars, created_at)`, migration 0016,
content-addressed and written in the transaction that writes the book's
package. The body in hand is hash-verified, so a write on an existing hash
replaces a row that no longer matches it (`ON CONFLICT DO UPDATE ... WHERE
the body differs`) and does nothing to a row that does. `FinishBook` refuses an outcome whose bodies
aren't exactly the ones the package's hashes name, each with its hash and
length. Every read hashes the body again and fails on a mismatch, and
`bookwarden verify-seals` checks that every excerpt a run's packages refer to
exists and matches, printing counts and never text.

**Showing.** On the book page each format with an excerpt has a collapsed
`<details>` labelled "Front matter text (N characters[, truncated][, incomplete: a document
could not be read])", with the
escaped text in a wrapping `<pre>`. Every hint ISBN that is printed in the text
(its 13 digits with spaces or any hyphens between them, not on two lines and
not part of a longer number; a hint that isn't 13 digits is ignored) is wrapped
in `<mark>`; an ISBN-10 printed for it
is not found. A missing or corrupt excerpt shows "Text unavailable" and never
fails the page. The fixes, findings and other list pages never load excerpts.

**Privacy.** An excerpt is book text. It never reaches logs, `/metrics`, error
messages or test fixtures in git (tests use synthetic text), and it is not sent
to the agents by this decision. A later version may send it to the configured
LLM providers and no one else; that will need its own decision.

## Consequences

- Lookups are capped at 4 ISBNs per book. Hint seeds are interleaved across
  formats (each format's first hint ISBN, then each one's second, and so on),
  and an `epub_text` ISBN from a `back` document is never seeded: it stays
  sealed as evidence, but back-matter ISBNs are usually ads for other books and
  would otherwise crowd out the PDF's.
- More lookups, and more ISBNs on a book page, each with its landmark and
  qualifier. A book whose front matter lists the print and e-book ISBNs shows
  both; choosing between them is later work.
- A short story collection with no landmarks may hit the text bound in its
  first 6 documents, and then records `failed`.
- Rollback: a binary from before this decision can't open schema 2 packages.
  Roll back only with a database restored from before the first run with this
  code, or after those runs are pruned.
- Part 2 grows the database. An excerpt is at most 12,000 characters per
  format. For 3,200 books with about two text-bearing formats each that is at
  most 3,200 x 2 x 12,000, about 77 MB of text before Postgres compresses it
  (TOAST compresses prose by roughly half), and most books have one format or
  short front matter, so 40 to 80 MB is the ceiling and 20 to 40 MB the
  expectation. Rows are content-addressed, so a run over unchanged files adds
  none; only changed files add rows. Each run's packages also grow by a unit
  map of about 1 KB per format. The fixtures in the tests measure 196
  characters for a five-document EPUB.
- Rollback: a binary from before part 2 can't open schema 3 packages. Roll
  back only with a database restored from before the first run with this code,
  or after those runs are pruned. The `excerpts` table is additive and older
  binaries ignore it.
- Retention: nothing deletes an excerpt yet. A pruned run leaves its excerpts
  behind; unreferenced ones are pruned later, with the retention work (M7).
- The EPUB scan parses each document it reads twice, once for the ISBN text
  and once for the excerpt, which differ in how they treat white space; the
  zip is read once. 48,000 bytes of excerpt text per document is a read limit,
  not a promise about runes: heavy markup or white space can fill it with
  fewer than 12,000 runes, and then `truncated` says text was left out. The
  deadline is checked between the two parses.
- A `failed` scan can still have an excerpt, of the documents that were read,
  with `complete` false.

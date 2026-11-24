# ADR-035: Look books up in Wikidata and Hardcover; identity v6

- Status: Accepted (parts 1, 2 and 3)
- Date: 2026-10-29
- Decision: the Bookwarden v2 plan, milestone M2; amends ADR-019's source list

## Context

On check run 7, 1,899 of 3,196 books had no source record, mostly English
books that DNB and BNE don't hold and Open Library lacks or lists without
an ISBN. Wikidata and Hardcover both answer exact-ISBN lookups. Whether
their records count as independent roots is a precision question, and the
decision rules (ADR-020, ADR-030) must not change under a collection change.

## Part 1: collect and seal (this change)

- **Wikidata** (`wikidata`). One SPARQL POST to `query.wikidata.org` finds
  items whose P212 (ISBN-13) equals the hyphenated ISBN and, for a 978 ISBN,
  whose P957 (ISBN-10) equals its hyphenated ISBN-10. Wikidata stores ISBNs
  hyphenated and matches them as exact strings, so the ISBN is hyphenated
  from the International ISBN Agency's range table, vendored in
  `internal/findings/isbnranges` with its date and hash. An ISBN in an
  unassigned range isn't queried: the outcome is `not_queried`, which is
  neither cached nor counted against the source's breaker. The record keeps
  title, subtitle, authors (P50 in P1545 order, else P2093 strings), the
  language's ISO 639 code, publisher label, publication date at the
  precision the item states, and the item's QID. Wikidata's data is CC0.
- **Hardcover** (`hardcover`). One GraphQL POST to `api.hardcover.app` with
  the token from `BOOKWARDEN_HARDCOVER_TOKEN`, sent only in the
  `Authorization` header and never logged. Without a token the outcome is
  `not_configured`. Editions with the ISBN-13 give title, subtitle, authors
  (contributions that are null or "Author", in order), language, publisher,
  release date, both ISBNs and the book's ID. Hardcover allows 60 requests a
  minute (burst 10) and 5,000 a day, so its host is paced at 1.1 s. A 429 or
  a GraphQL `errors` array is `unavailable`; a 401 is `unavailable` and logged
  once, without the token.
- **Fetcher.** `Fetcher` gains `Post`, under the same host allowlist, pacing,
  budget and address rules as `Get`; a POST follows no redirect. Both hosts
  join `Hosts`.
- **Outcomes.** As in ADR-019: `ok`, `no_match`, or `unavailable` for an HTTP
  error, timeout, 429 or a malformed or truncated answer. `unavailable` is
  never cached or sealed as `no_match`.
- **Order.** Results stay in adapter order, with the two new sources after
  `googlebooks`.
- **Decisions ignore them.** Decision versions up to 6 skip `wikidata` and
  `hardcover` before classifying sources, so they never count as a root or a
  conflict, and the same package decides byte-identically with or without
  their records. The agent packet leaves them out too, so existing review
  hashes hold. The book page shows them with the other source results.
- Hardcover is a community catalogue whose data partly comes from other
  catalogues, so it may not be an independent root. Part 2 decides that.

## Part 2: identity v6 (this change)

`decide.Version` becomes 6. Versions 1 to 5 re-derive byte-identically, with
or without front-matter hints in the package; a golden replay test pins this.
No sealed structure changes: the new reasons are strings.

1. **The v5 identity path is unchanged.** An ISBN every ISBN-bearing format's
   metadata agrees on, unflagged and unambiguous, is the identity.
2. **Identity from the front matter.** It applies only when the v5 path gives
   no identity and every eligible format has no metadata ISBN at all: none
   valid, none flagged, and none garbled (a bad checksum is still the file's
   own ISBN and blocks this path).
   - *Candidates:* the ISBNs of `epub_text` hints in state `ok` whose context
     landmark is `copyright` or `title`, over all EPUB formats, deduplicated.
     `front` and `back` ISBNs and PDF hints are ignored. The path fails
     closed if any EPUB's `epub_text` hint is not `ok` (a front matter that
     was not fully read may hide the right ISBN), and a hint list at the
     16-ISBN cap counts as ambiguous, since it was cut off.
   - *Q1 filter* (ADR-031, the ISBN of the edition the file is): if any
     candidate is labelled `ebook`, only those stay; otherwise candidates
     labelled hardcover, paperback, print or pdf go and the unlabelled stay.
   - *Exactly one* ISBN left is a provisional identity. More than one gives
     `frontmatter_isbn_ambiguous`. None left gives `frontmatter_isbn_missing`,
     when ok hints did carry ISBNs.
   - *Confirmation:* at least one ok record for the provisional ISBN, from a
     source that counts at v6 (DNB, BNE, Open Library, Google Books), must
     match the files in full (ADR-030: title, authors and language) with a
     title key identical to the files', which is stricter than ADR-030's
     title rule so a volume's record cannot confirm an omnibus. When the
     files lack a complete title, authors or language, the exact title key
     of the files or of Calibre decides. Then the identity is that ISBN, with
     the reason `identity_frontmatter` (so the book is never tier A). Any
     `records_*_conflict` or `records_disagree` after that drops the identity
     again and its records. Otherwise, and then, there is no identity and the
     reason is `frontmatter_isbn_unconfirmed`.
   - *No ISBN proposal.* A front-matter identity never yields an `isbn`
     proposal: the "file" root there is only a page-text hint, which must not
     replace an ISBN Calibre already holds.
   - A hint is never authority (spec section 7): the ISBN only selects whose
     records are read. Proposed values still need two independent roots.
3. **Other-work guard (both identity paths).** If the identity's records, over
   all roots, state exactly one title key and exactly one authors key, and both
   differ from Calibre's current title and authors keys (the keys proposals
   use), the ISBN belongs to another work. The usual case is a wrong file
   attached to the record: Calibre says "A by X" while the file and the
   catalogue say "B by Y", and renaming the record to B would be wrong. The
   reason is `other_work`, which is tier C like the other record conflicts,
   and the book gets no proposals at all, sort proposals included. Files
   agreeing with the records does not lift the guard.
   Plain keys differ for a dropped subtitle, a leading article or another
   form of a name, which are the same work. So the guard does not fire when
   any identity record matches Calibre's title under ADR-030's title rule or
   is a shortened form of it (ADR-026's `shortens`), or when any record
   author's surname is one of Calibre's authors' surnames.
   Another exception: the guard does not fire when Calibre's authors are a
   placeholder, because then the proposal is a fill, not another work. A
   placeholder is an empty authors list, or a single author whose folded key
   is one of a closed list: unknown, unknown author, anonymous, anónimo,
   anonimo, desconocido, autor desconocido, unbekannt, inconnu, various,
   varios, n/a, none (Calibre's own default is "Unknown"). The list lives in
   `placeholderAuthorNames` and needs an ADR change to extend.
   The Findings page lists the book under `other_work`. The entry is derived
   on every load from the decision and shown with the sealed findings; it is
   not sealed, so findings version 4 and every stored seal stay as they are.
4. **ISBN proposal.** From v6 a record vouches for the ISBN proposal only if
   its title key equals the files' exactly, as before, and it also matches the
   files in title, authors and language as ADR-030 describes; so it is never
   looser than version 5. Before, the exact title alone sufficed. A
   front-matter identity proposes no ISBN (point 2).
5. **Wikidata and Hardcover stay recorded only** up to version 6. Whether
   Hardcover, which partly draws on other catalogues, or Wikidata is an
   independent root is not established; a later version rules on it once
   labels show how often they agree with the national libraries.

## Part 3: Findings v5

`findings.Version` becomes 5. Versions 1 to 4 derive byte-identically
(`testdata/derive_v1_v4_golden.json` pins a corpus of packages that includes
every input below), and each detector runs only at version 5 or later. The
detectors **only add findings**. None proposes a value, feeds the decision
(`decide` ignores the new codes), authorizes a fix or queries a source. Each
reads Calibre's sealed record and nothing else, never the wall clock, and uses
a closed list or pattern kept in `internal/findings/v5.go`; extending one needs
an ADR change. When a value is ambiguous, no finding is derived. A finding has
no format; its field is the Calibre field and its reason is from the closed set
below.

Stored evidence is unaffected. A package sealed at version 4 still opens and
re-derives at version 4, and its evidence and findings rows are not rewritten.
What changes is the report: a seal check (a run's end, the hourly sweep of
stale checks, `bookwarden verify-seals`) re-derives each old book at version 5
and rewrites the run's book, finding and fix projections with the version-5
findings. It seals nothing. The `evidence` and `findings` rows stay as they
were written, so a version-4 run keeps `projection_version` 4 and shows the
new findings only through the projection. Migration 0017 widens the
`projection_version` check to 5. Runs checked after this change are written at
version 5.

`placeholder_pubdate` was planned and is deferred (see below).

| Code | Field | Reasons (closed) | Severity |
|---|---|---|---|
| `junk_title` | title | `word_artefact`, `file_extension`, `untitled`, `number_only` | warning |
| `title_noise` | title | `series_imprint`, `translator_credit`, `author_prefix`, `edition_credit` | info |
| `joined_authors` | authors | `and`, `und`, `et`, `y`, `ampersand`, `semicolon` | warning |
| `junk_publisher` | publisher | `placeholder`, `website`, `scanner_site` | warning |

**`junk_title`.** First match wins, in the order above.
- `word_artefact`: "Microsoft Word - " (any dash) followed by a file name
  that ends in a document extension, or by "Document" and digits.
- `file_extension`: a name glued to `.pdf .epub .mobi .azw3 .doc .docx .rtf
  .indd .odt`, case-insensitive. `.txt` is not on the list: "README.txt" can
  be a real title.
- `untitled`: "Untitled", "Untitled Document" or "untitled-1" (space, `_` or
  `-` before the number), case-insensitive.
- `number_only`: digits and at least one of `_ - .`, with a run of six digits
  or more ("20190312_153045"). A value of digits only is never flagged
  ("1984", "100000", an ISBN).

**`title_noise`.** One finding per kind present.
- `series_imprint`: one of "(Penguin Classics)", "(Penguin Modern Classics)",
  "(Oxford World's Classics)", "(Everyman's Library)", "(Vintage International)",
  "(Vintage Classics)", "(Signet Classics)", "(Signet Books)", "(Dover Thrift
  Editions)", "(Modern Library Classics)", in parentheses, case-insensitive,
  either apostrophe. Narrative-series markers ("(Dune Chronicles #1)") are not
  detected: that is a move to the series fields, which is a proposal.
- `translator_credit`: "Translated by", "Übersetzt von" or "Traducido por"
  followed by two capitalised words, and only inside parentheses or brackets or
  after " - ", ": " or " / ". "Lost Words Translated By Machine Learning" is
  not a credit.
- `author_prefix`: the title starts with "Surname, Given" (the sort form of one
  of the book's Calibre authors, from the author-sort table) and a dash
  (hyphen, en or em), with more text after it.
- `edition_credit`: in parentheses or square brackets, exactly one of
  "Annotated", "Illustrated", "Kindle Edition", "<Revised | Expanded | Revised
  and Expanded | Special | Deluxe | Collector's> Edition", "Nth Anniversary
  [Edition]", with "Annotated" and "Illustrated" also as "... Edition".

**`joined_authors`.** One finding per distinct separator in a book. An author
entry with exactly one separator kind from `; `, ` & `, ` und `, ` and `,
` et `, ` y `, where every part looks like a name: one to four words of
letters, `.`, `-` and apostrophes, or "Surname, Given"; at least one
capitalised word; lower-case words only if name particles; no digits; no
company word (Co, Inc, Ltd, GmbH, Sons, Brothers, Press, Publishers, Others and
a few more). Only `; ` may join single surnames. Every other separator, `&`
included, needs two words or more on each side, because "Ortega y Gasset" is
one name, "Pride and Prejudice" is not two people, and "Taylor & Francis",
"Faber & Faber", "Thames & Hudson", "Rowman & Littlefield" and "Strunk &
White" can't be told from firms. For `y`, `et`, `und` and `and` a right side
that starts with a lower-case particle (de, del, de la, van, von, da, di, du,
le, la) continues one name ("Ramón del Valle-Inclán y de la Peña") and gets no
finding. Not flagged: the
corporate list (Simon & Schuster, Procter & Gamble, Barnes & Noble, Harper &
Row, Marks & Spencer, Johnson & Johnson, Black & Decker, Ernst & Young, Hodder &
Stoughton, Farrar, Straus & Giroux, Little, Brown & Co) and an entry equal to
Calibre's publisher. It is a finding only because it is not writable: the fix
rules refuse author names with `&` or `|` (`fix/rules.go`), as Calibre rewrites
them and the before-value could not be restored.

**`junk_publisher`.** Compared folded, ignoring brackets and edge
punctuation. `placeholder`: unknown, unbekannt, desconocido, n/a, none,
publisher, calibre, as the whole value. `website`: written with a leading
`www.` or `http(s)://`. A bare domain such as Bookboon.com, Baen.com,
iUniverse.com or Xlibris.com is an imprint as printed (ADR-031) and is not
flagged. `www.` and the scheme are stripped before the exception list, so
"www.lulu.com" is judged as Lulu.com, with Tor.com, Smashwords.com,
CreateSpace.com, Amazon.com, Draft2Digital.com, BookBaby.com and Kobo.com.
`scanner_site`: the whole value "scan", or z-library, zlib(rary), z-lib or
libgen, or "library genesis", at the start of the value or after a non-letter
(so "Muñoz-Libros" and "Kranz-Libri" are not matches). Smashwords, CreateSpace
and "Smashwords, Inc." are real distributors and are not flagged.

### Rulings, and what each costs if wrong

Every ruling below flags less, not more: precision comes first.

1. **No bare-digit rule.** "1984" is a real title, so `number_only` needs
   digits plus one of `_ - .` and a run of six digits. Values of digits only
   ("100000", an ISBN) are never flagged. Cost: a junk bare number goes
   unflagged.
2. **"Microsoft Word - " needs a file-name remainder.** "Microsoft Word - The
   Missing Manual" is a plausible real title. Cost: "Microsoft Word - Kapitel 3"
   goes unflagged.
3. **Underscore file-name rule dropped.** The file's base name is not sealed
   (`Package` holds no paths). Cost: "my_book_final" goes unflagged. Sealing a
   base name is a schema change.
4. **`.txt` is not a junk extension.** Cost: "chapter1.txt" goes unflagged.
5. **`joined_authors` needs two words on each side, except for `; `.** Cost:
   "Smith & Jones" and "Strunk & White" go unflagged, and so does "Smith and
   Jones". If wrong the other way, a firm such as "Taylor & Francis" would be
   called two people.
6. **A particle after the separator means one name** (`y de la Peña`). Cost:
   "Juan Pérez y de la Cruz Maria" (two people) goes unflagged.
7. **Mixed separators are ambiguous** ("A & B; C") and get no finding.
8. **`translator_credit` needs brackets or a separator** before the credit.
   Cost: "Title, Translated by A B" (comma) goes unflagged.
9. **`edition_credit` needs brackets.** "20th Anniversary Edition" after a
   colon or dash can be part of a printed title.
10. **`website` needs the written form** (`www.` or a scheme). Cost: a scanner
    site written as a bare domain goes unflagged unless it is a `scanner_site`.
11. **`title_noise` is `info`**: a title with an imprint line is a style issue,
    not damage. The others are `warning`.

### Deferred: `placeholder_pubdate`

Not built. Its evidence is not sealed. The library reader drops a pubdate whose
year is below 1000 (`calibreTime`), so Calibre's undefined date (0101-01-01)
reaches the package as an empty pubdate and cannot be told from "no date".
A "later than the check" rule has no sealed check time (`Snapshot` is a hash);
`last_modified` as a stand-in never clears after the release date and would
label a real date a placeholder. It needs the raw Calibre pubdate and a sealed
check time: a later schema change.

### Skipped: `role_as_author`

Not built, because its evidence is not sealed. The EPUB inspector keeps only
`dc:creator` entries with no role or the role `aut`
(`internal/inspect/epub.go`), so a translator or editor is dropped before the
package is made; XMP creators carry no role; and `SourceRecord` has no
contributor roles (Hardcover contributions that are not "Author" are filtered
out at the adapter). The ADR-031 Q3 exception (editors are the authors of a
work with no author) would need the roles too. A later change that seals
creator roles (a schema bump) can add it.

## Consequences

- Cached and sealed packages from before this change end at `googlebooks`;
  readers must not assume six results per lookup.
- **No rollback past this change without a new check run.** An older binary
  can't open a package that holds an `ok` wikidata or hardcover result: it
  fails as an unknown source. The schema-2 packages from #80 already have the
  same limit, so this adds no new boundary beyond that one.
- A Wikidata answer of 600 rows and a Hardcover answer of 11 editions are
  treated as cut off and are `unavailable`; Hardcover's requests are capped
  at 4,500 per UTC day.
- Two more hosts receive ISBNs, and nothing else about a book.
- Run time grows by about one Wikidata and one Hardcover request per looked-up
  ISBN that isn't cached.
- **Findings v5 changes what old runs report.** Sealed packages at versions 1
  to 4 keep their rules and their rows, but the sweep re-checks old runs and
  rewrites their book, finding and fix projections with version-5 findings, so
  old books gain `junk_title`, `title_noise`, `joined_authors` and
  `junk_publisher` findings. It seals nothing.
- **No rollback past migration 0017.** An older binary refuses to start against
  schema 17 (a database migrated by a newer binary), and could not open a
  package sealed at version 5 (`ErrUnknownVersion`) either.

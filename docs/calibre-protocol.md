# Calibre content-server protocol, as bookwarden uses it

Sources:
- Calibre's source, read on 2026-10-12: `srv/cdb.py`, `srv/content.py`, `srv/auth.py`, `db/cli/cmd_list.py`, `db/cli/cmd_export.py`, `utils/serialize.py`.
- A live 9.16.0 server, checked by the milestone 0 probe (removed in M2.1; the integration test replaces it) and by hand for M2.1.

## Authentication
- The server runs as `calibre-server --enable-auth` with a user database. Over plain HTTP the default is digest authentication: MD5 or MD5-sess, with qop `auth` or `auth-int`. Unauthenticated requests get 401.
- Write endpoints need an account without the read-only flag. A read-only account gets 403.
- `/cdb/*` refuses accounts with per-library restrictions.
- Bookwarden never follows a redirect: the digest credentials would answer another host's challenge. A 3xx counts as Calibre being unreachable.
- Every request except a file or cover export and a set-fields write gets 60 seconds (`library.DefaultMetaTimeout`); after that Calibre counts as unreachable and a run waits for it. Exports last as long as the file needs, within the run's per-format limit; a write is bounded by the fix service (2 minutes).

## Library id
`GET /ajax/library-info` returns `{"library_map": {...}, "default_library": "<id>"}`. The id is the library's folder name.

## Listing
Request: `POST /cdb/cmd/list/0?library_id=<id>`, with the JSON body `[fields, sort_by, ascending, search_text, limit]`. For example:

```json
[["title","authors","languages","identifiers","formats","last_modified","uuid"], "id", true, "", -1]
```

The response is either:
- `{"result": {"book_ids": [...], "data": {"<field>": {"<book_id>": value}}, "metadata": {...}, "fields": [...]}}`, or
- `{"err": "...", "tb": "..."}`.

Notes:
- Remote `formats` values are format names, such as `["AZW3", "EPUB"]`, not paths.
- Typed values are wrapped as `{"jPoAv3zOyHvQ5JFNYg4hJ9": <type>, "v": <data>}`:
  - 0: datetime, as an ISO 8601 string;
  - 1: set, as a list;
  - 2: metadata object;
  - 3: field metadata;
  - 4: tag.

## Raw format bytes
Request: `POST /cdb/cmd/export/0?library_id=<id>`, with the JSON body `["fmt", <book_id>, "<FORMAT>", null]` and `Accept: application/x-msgpack`.

The response is a msgpack map, `{"result": <bin>}`. The bytes come from `db.format()` and are identical to the stored file. Calibre reads the whole file into memory to send it.

Always request msgpack here. Calibre's JSON encoder decodes bytes as UTF-8, so it can't carry binary data.

## /get rewrites metadata
`GET /get/<FORMAT>/<book_id>/<library_id>` embeds Calibre's current metadata into EPUB, MOBI, AZW3 and PDF before sending (`plugboard_content_server_formats`). Never use it for inspection.

## Writes
Request: `POST /cdb/set-fields/<book_id>/<library_id>`, with the JSON body `{"changes": {"title": "...", ...}, "loaded_book_ids": [<book_id>]}`.

The response is `{"<book_id>": <the book as JSON>}`, with the updated values. Calibre maps language names to codes on the server side.

Checked against Calibre 9.16.0's source for M4.1 and pinned by `TestCalibreWriteBehaviour` (ADR-021):
- The changes apply in body order, each through `db.set_field`. The answer (`book_as_json`) leaves out empty values and unset dates, and gives dates in ISO 8601 UTC; the title sort is `sort`.
- Every effective change moves `last_modified`, a sort-only one too. Writing the current value again moves nothing.
- Writing `title` recomputes the title sort (`The X` → `X, The`), and `authors` the author sort. A `sort` after the `title` in the same body wins.
- `identifiers` replaces the whole map.
- `pubdate` goes through `parse_only_date`: a date alone is stored as midnight UTC, day 1 becomes day 2, and the last day of a month other than December moves back one day (`2001-07-31` → `2001-07-30`, `2001-12-31` stays). A timestamp keeps its instant; `null` unsets.
- In `authors`, `&` becomes `;` and `|` becomes `,`.
- Writing an author that differs from an existing one only in case renames that author for every book.
- `GET /interface-data/field-names/{field}?library_id=` lists every author or publisher name, for read-only accounts too.
- A read-only account gets HTTP 403 from set-fields.

## Version coupling
Every `/cdb/cmd/<name>/<version>` module carries a version; `list` and `export` are both 0. A version mismatch returns 404 with "not available in version".

## Records, sizes and missing files
Checked by hand on 2026-10-12 against Calibre 9.16.0, for M2.1:
- `cdb list` fields bookwarden uses: `title`, `authors`, `author_sort`, `languages`, `identifiers`, `formats`, `last_modified`, `uuid`, `publisher`, `pubdate`, `cover`, `series`, `series_index`. There's no title-sort field.
- `search_text` `id:3` returns just that book; an unknown id returns `book_ids: []`.
- An unknown field gives HTTP 200 `{"result": "Unknown fields: sort"}`: a **string** where the listing object belongs.
- `publisher` can be `null`. An unset `pubdate` is the typed placeholder `0101-01-01T00:00:00+00:00`. `cover` is 0 or 1. `size` is the largest format's size, and `null` when a book has no formats.
- Per-format sizes come from `GET /ajax/books/{library}?ids=1,2,99`, which returns `{"1": {…, "format_metadata": {"epub": {"path": …, "size": 2340, "mtime": "…"}}}, "99": null}`, keyed by lower-case format. bookwarden discards `path` when decoding. `/ajax/book` disagrees with `list` (`pubdate` is `"None"`, `isbn` is `null`) and isn't used for records.
- `export` success is `81 a6 "result" c5 <uint16 len> <bytes>` (bin8, bin16 or bin32). A missing book, a missing format and a missing cover are all `{"result": nil}` with HTTP 200. Bad arguments are `{"err": str, "tb": str}` with HTTP 200.
- **A listed format whose file is gone from disk** still appears in `list`'s `formats`, and shows up in one of two ways:
  - the file was already gone when calibre-server started: `format_metadata` holds an empty entry, `{"epub": {}}`, because Calibre's `format_abspath` finds no file. bookwarden never requests such a format;
  - the file vanished while the server ran: `format_metadata` keeps the cached size, and `export` answers `{"result": <bin8 of length 0>}`.

  bookwarden records both as `missing` (`missing_on_disk`), never `empty`. The integration test's book 5 covers the first case; the second was checked by hand on 2026-10-12.
- A version mismatch is HTTP 404, text/plain, `The module list is not available in version: 1.Make sure…`.
- Reads (`list`, `export`, `/ajax/books`) leave every library file byte-identical, including `metadata.db` and `.calnotes`.

## Files Calibre produces
Checked on 2026-10-13 against Calibre 9.16.0, for M2.2's inspectors. The fixture library's files come from these tools:
- `ebook-convert` EPUB: `mimetype` stored first, a `META-INF/` directory entry, and `META-INF/container.xml` pointing at `content.opf` in the zip's root. The OPF has `dc:title`, `dc:language` `en`, `dc:creator opf:role="aut"`, a `dc:identifier opf:scheme="uuid"` and a `dc:identifier opf:scheme="ISBN"`; there is no `dc:date` when none is set. `calibredb add` doesn't rewrite the file, so book 2's EPUB still says "Lighthouse Signals".
- `ebook-convert` MOBI (MOBI header version 6, 232 bytes) and AZW3 (version 8, 264 bytes): PalmDB type `BOOKMOBI`, PalmDOC compression, 4096-byte text records, no encryption, text encoding 65001, locale `0x9`, EXTH flag `0x50`, and extra-data flags 3 (a multibyte overlap and one trailing entry). EXTH holds 524 (`en`), 503 (title), 100 (author), 104 (ISBN) and 106 (the conversion time when no date is set).
- Calibre's own PDF output needs QtWebEngine, which the test image lacks (`ImportError: cannot import name 'QWebEnginePage'`). Book 6 comes from Ghostscript's `pdfwrite` instead: PDF 1.7, a classic xref table, an Info dictionary from `pdfmark`, and an XMP packet.
- `calibredb add` of an EPUB tries to render a cover from the first page, fails the same way, prints a traceback and carries on; the book is added.
- `calibredb add --duplicates` of a file already in the library adds a second book whose stored file is byte-identical to the first (fixture book 9 is book 1's EPUB again), so the two share a file SHA-256 ([ADR-024](decisions/ADR-024-duplicates.md)).

## Covers

- `export` with `["cover", book_id]` answers in the same envelope as a format: `{"result": bin}` with the stored bytes, or `{"result": nil}` when the book has no cover. There is no listed size, so bookwarden reads at most 20 MiB, or the per-file limit if lower ([ADR-023](decisions/ADR-023-cover-checks.md)).
- Calibre stores every cover as JPEG (a PNG added with `calibredb add --cover` comes back as JPEG). Through `set-fields` it also scales anything above 1650 × 2200 px down; see the series and covers section.
- `calibredb add` makes a cover from a PDF's first page (1240 × 1755 for an A4 page) and from a comic archive's first image. EPUBs built with `--no-default-epub-cover` get none.
- `set-fields` takes a new cover as a data URL in the `cover` field, and a new cover moves `last_modified`, so the second snapshot read of a check sees a cover replaced mid-check.

## Series, series index and covers (Calibre 9.16, spike S1, 2026-11-04)
Pinned by `TestSeriesAndCoverWrites` (`internal/fix/series_cover_integration_test.go`) against `test/calibre`, on books 4 and 5, which start with no series and no cover. Every request is `POST /cdb/set-fields/<book_id>/<library_id>` with `{"changes": {...}, "loaded_book_ids": [<book_id>]}`. The test reads values back through `cdb list` with the fields `series`, `series_index`, `last_modified` and `cover` (the library client reads `series` and `series_index` since evidence schema 5: the index becomes a canonical decimal string, `1.0` is `"1"` and `2.50` is `"2.5"`; a `null` series is `""`), and covers through `export` with `["cover", id]`.

### Series and series index
| Request `changes` | Result |
|---|---|
| `{"series": "Spike Series"}` | `series` is `"Spike Series"`; `series_index` stays `1.0` (an unset index reads `1.0`, and an unset series reads `null`) |
| `{"series": "Spike Series [3]"}` | the suffix is parsed: `series` is `"Spike Series"` and `series_index` is `3.0`; `[2.5]` gives `2.5` |
| `{"series": "X [4]", "series_index": 6}` | `6.0`: entries apply in body order, so the later one wins; with `series_index` first, `"X [4]"` wins and the index is `4.0` |
| `{"series_index": 2.5}` | JSON number `2.5`, in the answer and in `list` |
| `{"series_index": 2}` | JSON number `2.0` (Python float representation, so a whole number keeps `.0`) |
| `{"series_index": "3.25"}` | accepted, stored as `3.25` |
| `{"series_index": null}` | `1.0` |
| `{"series_index": "abc"}` | HTTP 500 with an empty body; the index (`2.5` in the test) and `last_modified` don't change |
| `{"series": ""}` or `{"series": null}` | `series` becomes `null` in `list`, and the answer **omits the `series` key**; **`series_index` keeps its value** (clearing `4.5` leaves `4.5`, in the answer and in `list`) |
| `{"series_index": 7}` on a book without a series | accepted: the answer and `list` give `7.0`, and the series stays `null` |

- Series are one shared item per case-insensitive name. With book A in `"The Spike Saga"`, writing `"the spike saga"` to book B **renames the item for both books**: A reads `"the spike saga"` too, and `GET /interface-data/field-names/series` lists that one spelling only. The last spelling written wins; writing `"THE SPIKE SAGA"` to A renames B again. A book's index is its own and isn't touched by the rename. B's `last_modified` moves when A's write renames its series.
- When the last book leaves a series, the name disappears from `field-names/series`.
- Undo needs both fields. To go back to "no series, index 1.0", write `{"series": "", "series_index": 1.0}`; clearing the series alone leaves the old index behind. To go back to `Old` at `6`, write `{"series": "Old", "series_index": 6.0}`; the listing then equals the earlier state.
- Writing the same series and index again moves nothing; any change to either, a `[n]` suffix included, moves `last_modified`.

### Covers
`set-fields` takes the cover as a data URL in `cover`: `{"cover": "data:image/jpeg;base64,<...>"}` (or `image/png`). `{"cover": null}` removes it. Calibre decides by the bytes, not the declared type (a PNG labelled `image/jpeg` is a PNG).
- Refusals are HTTP 400 and change nothing, `last_modified` included: `""`, text that isn't a data URL, a data URL with truncated or non-JPEG/PNG bytes (GIF too) give `Cover data must be either JPEG or PNG`; a non-string such as `123` gives `Cover data is not valid base64 encoded data`.
- A JPEG that fits within 1650 × 2200 px is stored **byte for byte** (pinned for 64 × 96, 600 × 900, 1000 × 1500 and 1200 × 1800; the 600 × 900 case was 26885 bytes in and out with the same sha256).
- With the default tweaks of the `test/calibre` image (probably Calibre's `maximum_cover_size`, not checked), a larger image is **scaled down to fit 1650 × 2200**, keeping the aspect ratio, and re-encoded: JPEG 1600 × 2400 comes back 1466 × 2200, 3600 × 2400 comes back 1650 × 1100, and 2400 × 3600 comes back 1466 × 2200 as JPEG and as PNG. The dimensions are pinned; the byte counts seen in the test log (132344 → 91355 for the first) are not.
- A PNG is **re-encoded as JPEG** at its own size: 600 × 900 PNG comes back as a 600 × 900 JPEG. The sizes and hashes in the test log (11457 bytes in, 21258 out) are observations, not pinned.
- `list`'s `cover` goes 0 → 1 with a cover, and back to 0 with `null`; `export` answers `nil` for a book without one.
- Undo: writing back the bytes read earlier restores them exactly (a stored JPEG is within the limits by construction, so the rewrite is byte for byte; checked on fixture book 1). "No cover" is restored with `{"cover": null}`.
- Every cover write moves `last_modified`, including rewriting identical bytes and removing a cover that isn't there.

### last_modified
`list` gives `last_modified` with microseconds after any write (`2026-11-04T11:22:26.451746+00:00`); a never-written fixture book (7) shows whole seconds. Both are asserted. A write that changes a value moves it, in particular a first series, a new index, a `[n]` suffix, a case-only rename and any cover write. Writing an unchanged series and index moves nothing.

### Source records
Wikidata's series and series ordinal (P179 and P1545, deprecated statements ignored) are sealed on the source record from evidence schema 5. The source mapping version was not bumped for them, because that would ask every source again for every ISBN: a Wikidata answer cached before then has no series, and gains one only as its 30-day cache row expires.

## Integration test
`scripts/integration-test.sh` builds `test/calibre`, starts it next to a throwaway Postgres, copies a static test binary into the Calibre container and runs a whole check there. The test hashes every file under `/library` before and after the run and fails on any difference. CI runs the same script.

M2.3 also opens every stored result through the versioned findings reader and verified report repository. The fixture proves book 2's title mismatch, book 5's missing file and book 4's absent formats. Book 3's valid ISBN and equivalent English language codes across EPUB/MOBI/AZW3 do not create false conflicts. A filtered CSV contains book 2 only, and a recheck using its frozen library/UUID freezes exactly that one book. The final library hash includes both checks.

M4.1 adds a second binary, run after the read-only proof with the `writer` account: `TestCalibreWriteBehaviour` pins the write facts above on books 1 and 3, and `TestFixesOnRealCalibre` checks book 2 with fixed DNB and Open Library records, then applies and undoes five fields, proves a stale proposal writes nothing, and forces a readback mismatch that is restored and opens an incident.

### Title-sort carry-forward to M2.4

Calibre 9.16's [`cmd_list.py`](https://github.com/kovidgoyal/calibre/blob/v9.16.0/src/calibre/db/cli/cmd_list.py) excludes `sort` from allowed fields, consistent with the protocol failure above. The Go snapshot therefore does not request or infer it through `calibredb list`. Resolved in M2.4: the title sort and each author's sort come from the [`/ajax/books` JSON producer](https://github.com/kovidgoyal/calibre/blob/v9.16.0/src/calibre/srv/ajax.py) (`title_sort` and the author sort map), sealed with the book (`internal/library/books.go`).

## Verified
- 2026-10-12, Calibre 9.16.0: all 6 probe subtests passed.
- 2026-10-12, Calibre 9.16.0: list, /ajax/books and export shapes checked by hand for M2.1.
- 2026-10-13, Calibre 9.16.0: `scripts/integration-test.sh` passed (a whole run, byte-identical library).
- 2026-10-13, Calibre 9.16.0: `scripts/integration-test.sh` passed for M2.2 (7 books; EPUB, MOBI, AZW3, PDF and CBZ inspected `ok` with the files' own metadata; byte-identical library).
- 2026-10-13, Calibre 9.16.0: M2.3 reports, filtered CSV and identity-bound one-book recheck passed; existing format hashes and complete library bytes remained unchanged.
- 2026-10-19, Calibre 9.16.0: M4.1 write pins and the fixing scenarios (fix, readback and undo; a stale proposal; a forced readback mismatch with restore and incident) passed after the read-only proof.
- 2026-10-19, Calibre 9.16.0: M5.1 cover export, JPEG storage, generated covers for PDF and CBZ, and `last_modified` moving on a new cover passed; covers of the fixture books measured as expected.
- 2026-11-04, Calibre 9.16.0: `scripts/integration-test.sh` passed with `TestSeriesAndCoverWrites` (spike S1).

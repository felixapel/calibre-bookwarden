# Bookwarden in Go: design spec

- **Status:** Approved. Felix approved each section during the brainstorm, and approved the written spec on 2026-10-12. Amended on 2026-10-12 by the M2.1 plan (sizes source, scratch and memory limits). Amended on 2026-10-13 by the M2.2 plan (inspection rules and states) and approved M2.3 plan (versioned findings, comparison/report contracts and title-sort carry-forward). Amended on 2026-10-29 by the [v2 design](2026-10-29-bookwarden-v2-design.md): its rule changes take effect as each ADR lands.
- **Date:** 2026-10-12
- **Replaces:** Certificate A, the Python implementation on `main` of the same repository (`felix/calibre-bookwarden`), at the cutover in section 17.

## 1. Why

Felix wants bookwarden to be lean, correct, light and state of the art. It should run as **one container** that he adds on Unraid, and be published as open source that other people can use.

Certificate A works, but it's heavy:

| | Today (Certificate A at `c179690`) | Target |
|---|---|---|
| Containers | 4: app, verifier, Postgres, Valkey (plus an optional Caddy edge) | 1, using the Postgres that's already running |
| Images we build | 3: app 901 MB, writer 2.37 GB, edge 105 MB | 1, at most 100 MB |
| Library access | read-only mount of a snapshot copy; Calibre must be "stopped" | Calibre's content server, live |
| Fixes | a writer image that bundles all of Calibre; disabled in production | guided fixing through the content server |
| Code | 12,771 lines of Python in the production closure | one Go module |
| Last full run (2026-10-06) | 3,173 books in about 69 min; **0 Tier A** (only Open Library answered) | national-library sources, so fixes become possible |

## 2. Decisions from the brainstorm

1. **Job: guided fixing.** Bookwarden finds problems and proposes corrections. It applies only what Felix approves, with readback and undo. This is the original product goal in the old repo's `AGENTS.md`.
2. **Library access: Calibre's content server is required.** All reads and writes go through Calibre. There's no library mount and no snapshot copy.
3. **Access: a LAN port with a built-in login.** There's no VPN node and no reverse proxy. Other users can put their own HTTPS proxy in front.
4. **Improvements:**
   - Included: more authoritative sources, and duplicate and cover checks.
   - Not included: a digest and LLM explanations. (Incremental and scheduled runs came with ADR-039.)
5. **Language: Go**, built as one static binary.
6. **Database: Postgres**, on the shared `postgresql18` on `db-net`. There's no Valkey and no SQLite.
7. **Integrations: none.** The agent VM that used Certificate A is not a client. Its SSH tunnel and the root key it uses are removed at cutover.

## 3. Architecture

```
your browser ──login──► bookwarden (LAN port)
                          ├──► calibre:8081 on media-net: reads metadata and raw files, applies approved fixes
                          ├──► book sources over HTTPS: DNB, BNE, Open Library, Google Books
                          └──► postgresql18 on db-net: database "bookwarden"
```

It's one process with five packages, each with a narrow interface:

- **`library`** is the only code that talks to Calibre. `Reader` exposes list, format export and cover export. `Writer` exposes set-fields, and only `fix` ever gets one.
- **`verify`** schedules runs and executes the per-book pipeline: snapshot, fetch each format, inspect, look up, evaluate, then seal the evidence.
- **`inspect`** holds the pure-Go parsers for EPUB, PDF, MOBI/AZW3, CBZ and images, each with resource limits. PDF ISBN hints run `pdftotext`, `pdftoppm` and `tesseract` in a sandboxed child process (section 12); EPUB hints are read in-process from the front matter ([ADR-032](../../decisions/ADR-032-front-matter-evidence.md)).
- **`fix`** handles proposals, approvals, applying with readback, undo and incidents. It allows one write at a time.
- **`web`** serves pages rendered with `html/template`, plus a vendored htmx. There's no npm toolchain. No JSON API can write; changes happen only through the logged-in UI.

Supporting packages: `store` (Postgres via `pgx`, with embedded migrations), `sources` (one adapter per source plus a shared, polite HTTP client), `evidence` (canonical JSON, sealing, tier evaluation), and `config`.

## 4. Settings

All settings are environment variables, so they appear as fields in the Unraid template. Secrets are masked fields.

| Variable | Required | Meaning |
|---|---|---|
| `BOOKWARDEN_DATABASE_URL` | yes | Postgres URL, e.g. `postgres://bookwarden:…@postgresql18:5432/bookwarden` |
| `BOOKWARDEN_CALIBRE_URL` | yes | Content server base URL, e.g. `http://calibre:8081` |
| `BOOKWARDEN_CALIBRE_USER` / `BOOKWARDEN_CALIBRE_PASSWORD` | yes | The Calibre account bookwarden uses |
| `BOOKWARDEN_CALIBRE_LIBRARY` | no | Library id. Defaults to the server's default library |
| `BOOKWARDEN_ALLOWED_HOSTS` | yes | Host names and IPs the UI may be reached by. Anything else is refused |
| `BOOKWARDEN_GOOGLE_BOOKS_KEY` | no | Without it, Google Books is recorded as `not_configured` |
| `BOOKWARDEN_HARDCOVER_TOKEN` | no | Hardcover API token. Without it, Hardcover is recorded as `not_configured` (ADR-035) |
| `BOOKWARDEN_TRUSTED_PROXIES` | no | Only needed behind a reverse proxy |
| `BOOKWARDEN_METRICS_TOKEN` | no | Enables `/metrics`, protected by this bearer token |
| `BOOKWARDEN_LISTEN` | no | Defaults to `:8080` |

The **"Allow changes to Calibre"** switch is a setting in the UI, stored in the database and off by default. Every change to it is recorded.

## 5. Calibre content-server client

All of this was confirmed against Calibre's source (`srv/cdb.py`, `db/cli/cmd_*.py`, `utils/serialize.py`) on 2026-10-12. Milestone 0 confirms it end to end.

- **Reads** go through `POST /cdb/cmd/{command}/{version}`, which accepts JSON requests.
  - `list` returns the library listing.
  - `export` with `["fmt", book_id, fmt, null]` returns `db.format()`: the **stored bytes, unchanged**.
  - `export` with `["cover", book_id]` returns the cover.
  - Calibre's JSON encoder can't carry binary data, so byte responses are requested as msgpack (`Accept: application/x-msgpack`). The client decodes Calibre's typed values: datetimes, sets and metadata objects.
  - The msgpack payload is streamed straight to scratch, so bookwarden never holds a whole file in memory.
- **`/get/{fmt}` is not used for EPUB, MOBI, AZW3 or PDF.** Calibre rewrites their embedded metadata on download (`plugboard_content_server_formats` in `srv/content.py`), which would defeat the embedded-metadata checks.
- **Memory:** remote `export` loads the whole file into memory on the Calibre side. So bookwarden never requests a format above the size limit (per-format sizes come from `format_metadata` in `/ajax/books`, because `list` reports only the largest format), and it caps the bytes in flight (section 10).
- **Writes** go through `POST /cdb/set-fields/{book_id}/{library_id}`, the JSON endpoint Calibre's own web editor uses. The body is `{"changes": {…}, "loaded_book_ids": [book_id]}`, and the response carries the updated record.
- **Auth:** digest, which is Calibre's default over plain HTTP; basic also works.
  - The account must have **no per-library restrictions**, because Calibre refuses `/cdb` for restricted users.
  - Writes need a read-write account.
- **Version coupling:** every `/cdb/cmd` module has a version, and a mismatch returns 404. At startup bookwarden probes `list` and `export` and reports a clear "incompatible Calibre" error instead of failing halfway through a run.
- **Book identity:** the triple `(library_id, book_id, uuid)`. A changed `uuid` means a different book.
- **Snapshot rule:** at the start of a book, bookwarden hashes its listing record (`last_modified`, formats, sizes, identifiers). If the record changes before the book is sealed, the book ends as `source_changed` and its evidence can't support a proposal.
- **Read-only proof:** in CI, a full read-only run against a disposable library leaves every library file byte-identical, checked by SHA-256 before and after.

## 6. Data model (Postgres)

Bookwarden uses one role that owns the `bookwarden` database. Migrations are embedded and forward-only. Each runs in a transaction at startup, under a session advisory lock, so two instances can't migrate at once. The lock is taken by polling `pg_try_advisory_lock` for at most two minutes (the log names the pid holding it about every 10 seconds); after that startup fails instead of waiting for ever. Every migration transaction sets `lock_timeout = 10s`, so one that needs a strong table lock fails fast instead of queueing behind a long reader; a lock timeout (SQLSTATE 55P03) rolls the migration back and is retried up to 3 times, then startup fails. If `schema_migrations` holds a version higher than the newest embedded migration, a newer bookwarden migrated the database: startup fails and applies nothing, because an older binary must not run against it.

| Table | Purpose |
|---|---|
| `runs` | One per check: scope (whole library or one book), status, counters, timing, app version |
| `run_books` | The frozen book list of a run and each book's state: `pending`, `checked`, `source_changed`, `failed`. The tier (A, B or C) lives in the evidence |
| `evidence` | One sealed package per book per run (`jsonb`), with its SHA-256 seal and schema version |
| `excerpts` | Bounded front-matter text, content-addressed by the SHA-256 a package seals ([ADR-032](../../decisions/ADR-032-front-matter-evidence.md)) |
| `findings` | One row per problem, derived from evidence, for filtering and CSV export |
| `proposals` | Per-field approvals: current value, proposed value, supporting sources, status |
| `operations`, `operation_events` | Append-only write ledger: the header with the changes and the before snapshot, then one row per state with the after snapshot |
| `incidents` | Failed writes that pause all writes until acknowledged |
| `source_cache` | Validated source responses with a time-to-live |
| `covers` | A projection of each decoded cover: SHA-256, difference hash, dimensions, and whether it compares for duplicates |
| `duplicate_keys` | A projection of each book's exact duplicate keys (ISBN, file hash, title and authors) ([ADR-024](../../decisions/ADR-024-duplicates.md)) |
| `settings`, `users`, `sessions`, `setup` | The writes switch (an append-only log; the newest row wins), the admin account, hashed session tokens, the hashed one-time setup code |

**Evidence packages** record:
- the Calibre record as read, including its title sort and, since ADR-028, its authors' sorts from Calibre's authors table;
- for each format: size, SHA-256, readability state (`ok`, `corrupt`, `empty`, `drm`, `unsupported`, `not_inspected`, `missing`), and embedded metadata (title, authors, language, ISBNs, publisher, date);
- ISBN hints from a PDF's text layer or OCR, or an EPUB's front matter (method, state, pages or spine positions, ISBNs, and since schema 2 each ISBN's landmark and label qualifier and the OCR language; never text), and, since schema 3, for an EPUB's front matter or a PDF's text layer, an excerpt entry (method, the units with position, landmark and rune offsets, SHA-256, length, truncated, complete) whose text lives in `excerpts`;
- for each looked-up ISBN, the seed classes that produced it (`file`, `calibre`, `text_layer`, `ocr`, `epub_text`), and each source's outcome (`ok`, `no_match`, `not_configured`, `unavailable`, `not_queried`) with, if `ok`, its validated records;
- the tier and its reasons, the findings, and the proposals.

No file copies are stored. The packages hold no book text either: only the hash of each bounded front-matter excerpt (at most 12,000 characters per format; EPUB front matter and PDF text layers, never OCR), which is kept in the `excerpts` table and read with its hash checked.

**Sealing:** each package is serialised as canonical JSON (RFC 8785) and hashed with SHA-256. Every load re-checks the seal and recomputes the tier from the stored evidence, so a stored or caller-supplied tier is never trusted. As in ADR-002, the seal detects accidental or out-of-contract changes; it is not a signature. The schema version was 1 until the cutover; since then every change to the package bumps it. Schema 2 ([ADR-032](../../decisions/ADR-032-front-matter-evidence.md)) adds hint contexts, the OCR language and the `epub_text` method; schema 1 packages still open and derive as before, and a schema 1 package holding a schema 2 field is refused. Schema 3 (ADR-032 part 2) adds the optional `excerpt` of a format; schema 1 and 2 packages still open as before, and one that carries an `excerpt` is refused.

**Retention:** the last 10 runs are kept (`BOOKWARDEN_RETAIN_RUNS`; 0 keeps all), applied when a run ends and once a day, together with the newest completed run and anything newer. Evidence that a proposal or operation points to is kept regardless: pruning skips those runs. Excerpts no remaining package refers to are deleted with them ([ADR-039](../../decisions/ADR-039-scheduled-incremental-runs-and-retention.md)).

**Local findings (M2.3):** additive `findings_version`/`findings` fields preserve
schema-1 legacy seals. Version 0 derives in memory; sealed versions 1 and 2 are
recomputed with their immutable rules and compared on load, including a clean
empty result. Version 2 adds bounded capture-completeness flags and conservative
qualifications for older evidence at capture limits. Historical version-1 seals
and projections remain unchanged; reports derive a separate current view after
verifying them. Future changes retain version-specific historical readers. Evidence and its
complete ordered projection are committed atomically with frozen library/book/
UUID binding. A whole-run seal check verifies that complete projection once per finished run
(at its end, in an hourly sweep of checks older than a day, and with `bookwarden verify-seals`), and Findings, Fixes, Books
and CSV pages read the projection that check kept, falling back to verifying the run
when it has no check and failing when its check found damage
([ADR-038](../../decisions/ADR-038-verified-projection.md)); the single-book page and
every write verify their own package. Missing evidence or projection is an integrity error. Run reads, exports and
recovery probes use the captured library ID rather than a shared default selector.
[ADR-017](../../decisions/ADR-017-sealed-findings-and-reports.md) records the contract and tradeoffs.

**Tiers and proposals (M3.2):** additive `decision_version`, `tier`, `tier_reasons`
and `proposals` fields, omitted when empty, keep earlier seals. Every load verifies
the findings, then recomputes the decision; a sealed version-1 decision must match
exactly, and version 0 derives it in memory.
[ADR-020](../../decisions/ADR-020-tiers-and-proposals.md) records the rules.

**Backups:** the server's existing nightly database dumps, which an off-site backup copies. The cutover verifies that it picks up the new database. The README documents `pg_dump` for other users.

**Old data is not migrated.** The final Certificate A dump (`backups/20261012T094957Z-certificate-a/bookaudit.dump`) and the retirement archive preserve it. The first full run rebuilds the evidence.

## 7. Identity rules

These are ADR-002's rules, unchanged except where marked.

- An ISBN is an identifier only after its checksum validates.
- Sources are queried only by an exact, checksum-valid ISBN. A record counts only if its own identifiers contain that ISBN.
- **Roots and classes:**

  | Root | Class |
  |---|---|
  | each format's embedded metadata (`file`) | internal |
  | `dnb`, `bne` | **official** (new) |
  | `openlibrary`, `googlebooks` | external |
  | `ocr`, `text_layer`, `epub_text` | hint only: it seeds lookups and is never authority |
  | Calibre's current value | the subject under audit, never evidence |

  An Open Library record whose `source_records` all come from one national library's MARC records **shares that library's root**, so the two can't count as two sources. It **keeps the external class**, because Open Library records are community-editable.
- **Tiers:**
  - **A:** every format is readable; all formats identify one ISBN, with complete and agreeing title, author and language; the file content and one exact structured record agree on ISBN and those fields.
  - **B:** evidence is missing or incomplete.
  - **C:** formats or exact records conflict.
- **Field rule:** a field is proposed only when at least two independent roots agree on the exact value, and at least one of them is internal or official.
- **Change from ADR-002:** Tier B books get proposals for fields that pass the field rule on their own. Tier C books get none. Previously Tier B got none.
- **Dates:** proposed only at full-date precision. A year alone stays a finding, so bookwarden never invents a January 1.
- **M3.2 rules** ([ADR-020](../../decisions/ADR-020-tiers-and-proposals.md)):
  - All metadata-eligible formats together are the one `file` root. Several records from one source are one root; a root whose members disagree on a field is split for it.
  - Only records for the ISBN the files establish count (every format that states an ISBN states exactly one, and they agree). Records reached through Calibre's ISBN or a hint are shown, never authority.
  - Values compare under the M2.3 rules. A record title is compared without its subtitle, and record values at the capture cap are unverified.
  - Tier C also covers two identity records disagreeing on title, authors or languages, whether from one source or two.
  - **Veto:** a value isn't proposed when an internal or official root states another value for that field, or is split.
  - Differences only in case or whitespace aren't proposed. Languages are proposed as ISO 639-3 codes. An ISBN is proposed when a record from another root agrees with the files' title.
  - A title proposal never shortens Calibre's title: if the proposed title's words are a shorter contiguous run of Calibre's, there is no proposal ([ADR-026](../../decisions/ADR-026-title-proposals-never-shorten.md)).
  - An authors proposal must bring in a surname Calibre doesn't list: if every proposed author's surname is already one of Calibre's authors' surnames, counting repeats, there is no proposal ([ADR-027](../../decisions/ADR-027-author-proposals-never-drop-or-reform.md)).
  - Sorts restate Calibre's own rules, with the single root `calibre_rule`, in every tier ([ADR-028](../../decisions/ADR-028-sort-fixes-from-calibre-rules.md)):
    - a title sort the title-sort finding flags gets Calibre's `title_sort` of the title and first language;
    - an author sort gets Calibre's authors-table entries joined with " & ", only when every entry is Calibre's default `author_to_author_sort` of the name;
    - neither is proposed beside a proposal for a field its rule reads (title or languages; authors), since it would be computed from the old value;
    - these are the one exception to the two-root rule and to Tier C getting no proposals.

## 8. Guided fixing

- **Fields in scope:** title, authors, languages, the ISBN identifier, publisher, publication date, title sort and author sort (ADR-028).
- **Out of scope:** series, tags, comments, covers and merging books. Those stay in Calibre.

1. **Proposals** are created when a book's evidence is sealed. Each one lists its supporting sources. They live in the sealed evidence; the `proposals` table, which holds approval status, arrives with M4.
2. **Review:** the Fixes page lists books with their current → proposed values. Felix ticks fields and approves or rejects them. "Approve all shown" works on a filtered list, for example only language fixes, capped at 50 per click.
3. **Apply** runs one book at a time, under a session-level Postgres advisory lock: one writer, even if two instances run by mistake. The operations ledger is write-ahead: each state (`requested`, `writing`, `verifying`, `completed`, `failed`, `restoring`, `restored`) is committed before the next step starts. After a crash, an operation found in `writing` or `verifying` is treated like a readback mismatch and gets a fresh readback and incident review.
   1. Re-read the book from Calibre. If `last_modified`, or any target field, differs from the evidence snapshot, mark the proposal `stale` and write nothing.
   2. Record the operation with the before snapshot.
   3. Call set-fields with only the approved fields.
   4. Read the book back, from both the set-fields response and a fresh `list`, and compare every written field with the approved value. Record the after snapshot.
   5. **On a mismatch:** write the before values back only where the book still holds a value the operation knows (the before value, one it wrote, or one Calibre answered with), leave and name any field that holds anything else, read back again, record an incident, and pause all writes until Felix acknowledges it. The operation ends failed when a field was left or can't be put back.
4. **Undo** is available on every completed operation, as long as the book is unchanged since that operation. It writes the before values with the same steps and is recorded as its own operation.
5. **Safety:** writes are blocked while the "Allow changes to Calibre" switch is off. Only the logged-in admin can approve, or, with agent fixing on, a panel of three LLM judges from three vendors when all three agree (ADR-029). The panel decides only undecided proposals; its approvals go through the same Apply, stale check, readback and undo.

**M4.1 rules** ([ADR-021](../../decisions/ADR-021-guarded-writes.md)): approvals copy their values from the verified sealed decision, and only the newest checked evidence for a book can be decided. The stale check compares the whole record, the format snapshot and the stored proposals, not just the target fields. A field is written only when Calibre stores both the approved and the current value exactly, and when no existing author or publisher differs from a written name only in case; otherwise it is blocked. Restores and undo write before values in pairs (title then sort, authors then author sort); a sort written alone goes back alone (ADR-028). Any set-fields error or failed readback is a mismatch, and every operation that doesn't complete has one incident. After a crash, recovery restores only the fields that still hold the value it wrote, each with its sort, and every restore records the book as found before writing. A field the before record can't express is blocked as `calibre_cannot_restore`, and an undo that couldn't put every field back refuses. After a mismatch or write error, a field holding a value the operation doesn't know was edited since: it stays and the operation ends failed with its incident, so a write Calibre altered before an error stays too. A restore Calibre refuses for good ends failed; every Calibre call has a deadline. Undo is allowed while the book's record equals the operation's after record; a record captured before bookwarden read the authors table is compared without it (ADR-028).

## 9. Sources

| Source | Request | Class | Probe on 2026-10-12 |
|---|---|---|---|
| DNB | `https://services.dnb.de/sru/dnb?version=1.1&operation=searchRetrieve&query=num%3D{isbn}&recordSchema=MARC21-xml` | official | MARC21-xml ✅ 2026-10-18 (it failed on 2026-10-12; RDF has no author names, only GND links) |
| BNE | `https://catalogo.bne.es/view/sru/34BNE_INST?version=1.2&operation=searchRetrieve&query=alma.isbn%3D{isbn}&recordSchema=marcxml` | official | ✅ (3 records for the test ISBN) |
| Open Library | `https://openlibrary.org/isbn/{isbn}.json` (a same-host redirect to the edition), then the first work and its authors when the edition has none | external | ✅ |
| Google Books | `https://www.googleapis.com/books/v1/volumes?q=isbn:{isbn}`, key in the `X-Goog-Api-Key` header | external | 429 without a key, so it's queried only when a key is set |
| Wikidata | POST `https://query.wikidata.org/sparql`: items whose P212 (or P957) equals the hyphenated ISBN, hyphenated from the ISBN Agency's range table | recorded only | ✅ 2026-10-29 (ADR-035); decisions up to version 5 ignore it |
| Hardcover | POST GraphQL `https://api.hardcover.app/v1/graphql`, `editions(where: {isbn_13: {_eq: …}})`, token in the `Authorization` header | recorded only | queried only when `BOOKWARDEN_HARDCOVER_TOKEN` is set; field names unverified against a live token (ADR-035) |

**Excluded:**
- The Library of Congress SRU: it's plain HTTP only, and it answered "System temporarily unavailable".

**Client rules:**
- HTTPS only, to a fixed host allowlist.
- Redirects that leave the host are refused, and so are private and link-local addresses.
- Responses have a size cap and are parsed with strict schemas. Malformed or empty responses produce no candidate.
- Each host gets its own pacing, 1 request/s by default.
- Retries use jitter and honour `Retry-After` within a per-request time budget.
- A per-run circuit breaker marks a source that keeps failing as `unavailable` for the rest of the run.
- Validated responses are cached for about 30 days. `no_match` is cached for about 7 days. Failures are never cached. Expired answers are served within a refresh budget per run (ADR-044, amended 2026-11-22).
- The User-Agent is `bookwarden/<version> (+<repository URL>)`.
- Every lookup records which seed classes produced its ISBN (`file`, `calibre`, `text_layer`, `ocr`, `epub_text`), at most 4 ISBNs per book. [ADR-019](../../decisions/ADR-019-source-lookups.md) records the adapters, the cache and the breaker.

## 10. File inspection and checks

Files stream from Calibre into the scratch tmpfs. The book's scratch directory is deleted when the book finishes.

| Format | Inspection |
|---|---|
| EPUB | Zip structure, OPF metadata (identifiers, title, creators, language, publisher, date), DRM detection (`encryption.xml`, `rights.xml`), text present. ISBN hints from the front matter (before the first body landmark, else the first 6 documents, at most 12) and the last 3 documents, each with its landmark and label qualifier, under a 256 KiB text bound; a scan that hit a bound is `failed` |
| PDF | Structure and xref sanity, Info and XMP metadata, encryption, text layer. With a text layer: ISBN hints from `pdftotext` of pages 1 to 12 and the last 3. Without one: OCR of pages 1 to 5 and the last 5, in the book's language (`eng`, `deu` or `spa`, else `eng`) |
| MOBI/AZW3 | PalmDB and MOBI headers, EXTH metadata (ISBN, author, title, language, publisher, date), DRM flag, first text record decompresses |
| CBZ | Zip readability and the first images decode |
| Images (JPEG, PNG, GIF, WebP) | Header and full decode; above 24 megapixels or the decoder memory allowance the header only |
| CBR and anything else | `not_inspected`, recorded explicitly |

**Limits** are defaults and configurable in code:

| Limit | Default |
|---|---|
| File size, taken from Calibre's listing before download | 512 MiB. Larger formats are not downloaded and are recorded as `not_inspected` (too large) |
| Bytes in flight across parallel books | 1 GiB. This also bounds Calibre's memory, since remote export loads whole files |
| Scratch tmpfs | 1.5 GiB (`--tmpfs /tmp:size=1536m`) |
| Total decompressed size | 4 GiB, checked while streaming |
| Compression ratio | 100:1 |
| Zip entries | 10,000 |
| Image decoding, estimated as RGBA16 pixels plus two PNG rows | 256 MiB; above it, `ok/header_only`, including sampled CBZ pages |
| PDF cumulative parsed allocations / constructed values | 32 MiB / 100,000 per document; not refunded after eviction or failed parsing |
| PDF decoded object-stream cache | 32 MiB of backing capacity, at most 16 streams; larger streams remain usable uncached |
| PDF unique xref entries / repair trailer candidates | 131,072 / 4,096 |
| PDF encoded input / decoded object or xref stream | 64 MiB / 64 MiB |
| Time per file | 120 s |
| OCR time per book | 180 s |

tmpfs pages count toward the container's memory limit. Each format's file is deleted as soon as it's handled, so scratch holds at most the bytes in flight plus OCR renders, and the template sets `--memory 2g`.

When a limit is hit, the format's state becomes an explicit finding; it is never silently skipped.

**Inspection rules.**
- Each file is inspected as what its bytes are when that is recognisable (PDF, EPUB, zip, MOBI, RAR, image), otherwise as the format Calibre declares. The detected type is recorded, so a mismatch becomes the "extension doesn't match the content" finding. Bytes that are neither recognisable nor readable as the declared format are `corrupt`.
- States: `ok` (parses and the checks pass, possibly qualified: `xref_repaired`, `encrypted`, `header_only`), `corrupt` (the container or a required part is broken), `drm` (locked to a reader or account), `unsupported` (a known container in a variant bookwarden can't read), `not_inspected` (no inspector, a timeout, an inspector error, or a limit), and `empty` and `missing` as in section 6.
- EPUB: font obfuscation (`http://www.idpf.org/2008/embedding`, `http://ns.adobe.com/pdf/enc#RC`) is not DRM. Any other algorithm in `META-INF/encryption.xml`, or a `META-INF/rights.xml` or `META-INF/sinf.xml`, is.
- PDF: the `Standard` security handler is `ok` with reason `encrypted`, an unverified text layer and no metadata; any other handler is `drm`. Document-wide resource limits remain `not_inspected/decompressed_too_large` even when reached while loading optional metadata or repairing an object. The first 50 pages share a 64 MiB decoded content budget, with 16 MiB per stream; unread or undecodable content is `unverified` unless inspected content already proves text present. Form XObjects are visited in resource-name order. [ADR-016](../../decisions/ADR-016-inspector-memory-bounds.md) records the bounds and compatibility costs.
- MOBI: HUFF/CDIC compression is `unsupported` (`huff_cdic`); a non-zero encryption type is `drm`.
- The 120 s per file covers download and inspection.

**Checks** produce findings with stable codes:
- **Files:** corrupt, empty, DRM, an extension that doesn't match the content, a format missing in Calibre.
- **Identity:** an invalid ISBN checksum; formats disagreeing on ISBN, or a format disagreeing with Calibre; title, author or language mismatches; a missing ISBN or language; placeholder or imprecise dates.
- **Duplicates:**
  - the same ISBN on two books;
  - the same file hash on two books;
  - the same normalised title and author;
  - near-identical covers by perceptual hash (a hint only).
  M5.2 compares the books of one whole-library run: ISBNs from Calibre or embedded metadata (never hints), non-empty files, Calibre's title and authors under the M2.3 rules, and comparable covers within difference-hash distance 4. Groups are a re-verified projection, not sealed findings, and never authorize a merge ([ADR-024](../../decisions/ADR-024-duplicates.md)).
- **Covers:** missing, low resolution (shortest side under 600 px), a generic placeholder (a near-uniform or tiny image), or corrupt. M5.1 exports the cover through the content server, capped at 20 MiB (or the per-file limit if lower), and seals its dimensions, a difference hash and a flatness measure ([ADR-023](../../decisions/ADR-023-cover-checks.md)).
- **Junk Calibre values (findings v5, [ADR-035](../../decisions/ADR-035-identity-v6-and-more-sources.md) part 3):** a title that is a file name or application artefact (`junk_title`), title noise from a closed list: imprint series, translator credit, author-name prefix, edition credit (`title_noise`), several people in one author entry (`joined_authors`) and a placeholder, website or scanner-site publisher (`junk_publisher`). They read only Calibre's sealed record, add findings and propose nothing. `role_as_author` waits until creator roles are sealed, and `placeholder_pubdate` until the raw Calibre pubdate and a check time are.
- **Sorting:** author-sort and title-sort anomalies, using the old repo's authority rules (particles, epithets, compound authors, corporate names). M2.3 ports the pinned author-sort algorithm. M2.4 snapshots Calibre's `title_sort` from `/ajax/books` and compares it with a port of Calibre 9.16's `title_sort` (default tweaks, the book's first language, English fallback): an empty sort is `title_sort_missing`; a sort equal to the title while the expected value moves an article is `title_sort_article_not_moved` (info); a sort matching neither the expected value nor any other language's articles is `title_sort_mismatch`. [ADR-018](../../decisions/ADR-018-ocr-sandbox-isbn-hints-title-sort.md) records the policy.

**M2.3 comparison rules:** titles and ordered authors compare at NFC/Unicode
case/whitespace normalization, preserving accents, punctuation and edition
labels. Languages compare explicit primary codes (`eng` equals `en`); malformed
and undetermined languages cannot establish a conflict or agreement. A whole
comparison field has a 512-rune allowance; larger fields stay unverified. Only
checksum-valid normalized ISBNs enter comparisons, and multiple candidates are
not selected into an identity. Publication dates compare only at valid full-date
precision; a year/month stays imprecise and Calibre January 1 is a review hint,
not proof of a wrong date. These findings authorize no fix or source query.

## 11. Runs and UI

**Runs:**
- A run is started from the UI, for the whole library or as "check this book again". Only one run is active at a time.
- Each run freezes its book list. After a restart it resumes the books that have no terminal state. It can be cancelled.
- Concurrency: 4 books in parallel by default, with per-source pacing. OCR runs one at a time.

**Pages:**
- Setup and Login.
- Overview: library status, the last run, totals.
- Runs: start, progress (htmx polling), history.
- Findings: filters and CSV export.
- Book: formats, source records, tier reasons, findings, proposals, possible duplicates.
- Duplicates: a run's groups by kind, 25 per page, each with its first 10 books.
- Fixes: the approval queue. Each row shows its status; ticks with approve or reject, and "approve all shown" for the page's rows (at most 50) except rejected ones, which stay rejected unless ticked. A value Calibre can't store or put back exactly shows why and can't be approved. "Apply approved" starts the writer in the background ([ADR-022](../../decisions/ADR-022-fixing-pages.md)).
- History: operations, newest first, 50 per page, and whether an apply is running. Each operation shows its before, written and after values, its steps and its incident, and offers undo on a completed operation that isn’t undone yet; the undo is refused if the book changed since.
- Incidents: open ones first, each acknowledged with a button.
- Settings: the writes switch with its log, a reminder to make the Calibre account read-write only while writes are on, and source status.
- While an incident is open, Fixes, History, Incidents and Settings say that writes are paused.

## 12. Security

**Admin claim:** the first start prints a one-time setup code in the container log. The first visit asks for it and sets the admin password. There's one admin account; multi-user support is out of scope.

**Passwords and sessions:**
- Passwords are hashed with argon2id. Login attempts are rate-limited per IP with exponential backoff.
- Session tokens are random 256-bit values, stored hashed.
- The cookie is `HttpOnly` and `SameSite=Strict`, plus `Secure` when the request arrives over HTTPS through a trusted proxy.
- Sessions expire after 12 hours idle or 7 days total.

**Request checks:**
- Every state-changing request needs a CSRF token and a matching `Origin`.
- Requests whose `Host` isn't in `BOOKWARDEN_ALLOWED_HOSTS` are refused. This blocks DNS rebinding from websites Felix visits.

**Headers:**
- A strict Content-Security-Policy: `default-src 'self'`, no inline scripts, `frame-ancestors 'none'`.
- `X-Content-Type-Options: nosniff` and `Referrer-Policy: same-origin`. Not `no-referrer`: browsers then send `Origin: null` on form posts, and over plain HTTP no `Sec-Fetch-Site`, so the cross-origin check would refuse every form.

**Secrets:** the Calibre password, the database URL and the Google key are never logged and never stored in evidence. Logs identify books by id, not by title or path.

**Least privilege:**
- The Calibre account is read-only until fixes are turned on, and only then switched to read-write.
- The Postgres role owns only the `bookwarden` database.
- The container runs as UID 10001 with a read-only root filesystem and a tmpfs `/tmp` for scratch. It has `--cap-drop ALL`, `no-new-privileges`, and memory and PID limits set in the template.

**Untrusted files:**
- Go parsers are memory-safe and run in-process, under the limits in section 10.
- `pdftoppm`, `pdftotext` and `tesseract` run in a child process re-executed from the bookwarden binary. Before `execve`, the child sets:
  - a seccomp filter that blocks networking (`socket`, `socketpair`, `connect`, `bind`, and `io_uring_*`, which can open sockets without `socket`), `ptrace`, `process_vm_readv/writev`, `pidfd_getfd` and `mount`, and blocks new processes (`fork`, `vfork`, and `clone` without `CLONE_THREAD`; `clone3` returns `ENOSYS` so libc falls back to `clone`);
  - rlimits on CPU, address space, file size and open files, and no core dumps;
  - no inherited environment (exactly `OMP_THREAD_LIMIT=1`), and the book's scratch directory as its working directory.

  Bookwarden makes itself non-dumpable before its first child, so the same-UID tool can't read its environment (the secrets) from `/proc` or reach its sockets or memory; Unraid's kernel has no Yama to stop that otherwise. A compromised tool can still read files its UID can read, signal bookwarden, or fill the scratch tmpfs (ADR-018).

  The parent kills the child at the time limit.
- Landlock isn't used: Unraid 7.3.3's 6.18 kernel returns `ENOSYS` for it (tested 2026-10-12).

**Outbound traffic:** only Calibre and the four source hosts. Sources receive ISBNs only, never titles or text. There's no telemetry.

**Endpoints:** `/healthz` is open: process and database liveness. Calibre's reachability shows in the UI and doesn't mark the container unhealthy. `/metrics` is available only when `BOOKWARDEN_METRICS_TOKEN` is set.

**Supply chain:**
- The Go toolchain, Alpine base and actions are pinned by version or digest.
- `go mod verify`.
- CI runs golangci-lint, `govulncheck`, gitleaks and Trivy, and fails on any HIGH/CRITICAL finding.

## 13. Container and Unraid template

- **Image:** Alpine (pinned digest), CA certificates, `tesseract` with the `eng`, `deu` and `spa` fast models, `poppler-utils` (for `pdftoppm`), and the bookwarden binary built with `CGO_ENABLED=0`. Target size is at most 100 MB.
- **No volume is needed.** Postgres holds everything, and scratch is a 1.5 GiB tmpfs.
- **Networks:** `media-net` for Calibre and `db-net` for Postgres. An Unraid template selects one network, so the second goes in Extra Parameters (`--network db-net`). Docker 25+ accepts multiple networks, and the server runs 29.8.2; the cutover verifies it works.
- **Port:** 8080, mapped to a LAN port Felix picks.
- **Health:** `HEALTHCHECK` calls `/healthz`.
- **Registry:** images are published to the Gitea registry, and to GHCR once Felix publishes the rewrite on the GitHub mirror.
- **For other users:** the README ships a `docker run` example and a two-service compose example (bookwarden + Postgres).

## 14. Observability

- Structured JSON logs (`log/slog`), identifying books by id.
- `/healthz`, plus optional token-protected `/metrics` with run, source-outcome, write and incident counters. Labels never come from book data.

## 15. Testing

- **Unit tests:** table-driven, run with `-race`.
- **Fuzzing:** Go fuzz targets for the EPUB, PDF, MOBI and CBZ parsers and the source parsers. A short fuzz smoke runs in CI.
- **Fixtures:** synthetic files generated by the tests. No private books or titles go into git.
- **Integration tests in CI:**
  - real Postgres 18;
  - a disposable official Calibre content server with a fixture library, pinned by checksum like the old disposable lab.
- **Integration scenarios:**
  - a read-only run leaves the library byte-identical;
  - export bytes equal the files on disk;
  - fix, readback, and undo restore the exact before values;
  - a stale proposal writes nothing;
  - a forced readback mismatch triggers restore and an incident;
  - Calibre's write behaviour that the writer relies on (dates, sort recomputation, `last_modified`, author separators, shared-name renames, identifiers), pinned against the tested version.
- **Sources:** tested against recorded responses. An optional live probe never blocks CI.
- **Security tests:** zip bombs, oversized files, CSRF, Origin and Host checks, login rate limiting, the seccomp child (no network), and setup-code single use.

## 16. Delivery

**Repository:** the existing `felix/calibre-bookwarden` on Gitea. This was decided on 2026-10-12 and replaces an earlier plan for a new repository.
- **Branch:** the rewrite lives on `go-rewrite`, created from `main`.
  - Its first commit removes the Python tree, keeping `LICENSE`, `docs/decisions/` and `docs/superpowers/`.
  - `main` keeps serving Certificate A until the cutover merges `go-rewrite` into it (section 17).
- **Visibility:** the Gitea repository is public. The GitHub mirror (`github.com/felixapel/calibre-bookwarden`) is updated by hand, so nothing reaches GitHub until Felix asks.
- **Names:** the Go module is `github.com/felixapel/calibre-bookwarden`, matching the mirror. The binary, image and CLI are all named `bookwarden`.
- **ADRs:** they stay in `docs/decisions/`. A new ADR-015 records this rewrite and supersedes the ADRs that describe the Python implementation. These still apply:
  - ADR-002: tiers, amended by section 7.
  - ADR-003: scope.
  - ADR-014: metrics.
- **The old roadmap's delivery rules don't carry over.** That covers both the "about 400 lines per PR" cap and the "Codex owns…" ownership rule. The implementation plan names who implements.
- **Process:** each milestone is one or more PRs into `go-rewrite`. Each is green in CI and reviewed by the read-only `reviewer` agent, then Felix merges.

| # | Milestone | Done when |
|---|---|---|
| 0 | Protocol probe (throwaway) | Digest auth, JSON list, msgpack format bytes byte-identical to disk, set-fields with readback, and undo all work against a disposable Calibre |
| 1 | Skeleton | Settings, Postgres with migrations, setup and login, `/healthz`, CI gates, image build, Unraid template |
| 2 | Read-only checking | Library reader, runs, inspection, sealed evidence, findings, CSV export, Book and Findings pages; M2.4 completes OCR, the PDF text-layer ISBN authority decision and proven title-sort snapshot/policy/findings |
| 3 | Sources and tiers | DNB, BNE, Open Library and Google Books adapters, the cache, tiers, proposals |
| 4 | Guided fixing | Fixes page, apply with readback, undo, incidents, writes switch |
| 5 | Duplicates and covers | Duplicate and cover findings |
| 6 | Parity and cutover | Section 17 complete |

## 17. Cutover and rollback

Certificate A was retired on 2026-10-12, before the shadow run. [ADR-025](../../decisions/ADR-025-cutover-after-early-retirement.md) changes steps 2, 4 and 5 below: the shadow run is compared with its archived last full run, and there is no rollback window. The runbook is kept with the maintainer's internal docs.

1. **Prepare.**
   - Create the `bookwarden` role and database on `postgresql18`.
   - Felix creates a **read-only** Calibre user, in Calibre → Preferences → Sharing over the net → Users.
   - Add the container from the template on `media-net` and `db-net`.
2. **Shadow run.**
   - Run a full read-only check.
   - Compare it with Certificate A's results (`c179690`): file states, extracted ISBNs, findings.
   - Every difference must be explained, for example by the new sources, before going on.
3. **Turn on fixes.**
   - Switch the Calibre user to read-write and enable "Allow changes to Calibre".
   - Apply a few fixes and verify them in Calibre and CWA.
   - Undo one, and verify that too.
4. **Retire Certificate A.**
   - Tag the last Certificate A commit on `main` as `certificate-a-final`.
   - Switch `main`'s required status checks from the Python jobs to the Go CI jobs. Then merge `go-rewrite` into `main` through a PR.
   - Stop its 4 containers.
   - Archive its appdata together with the final `pg_dump`, with SHA256SUMS, as was done for earlier retired apps.
   - Remove the agent VM's tunnel unit and its root SSH key. That needs Felix on that VM and in the server's `authorized_keys`.
   - Confirm the db-dump job includes `bookwarden`.
5. **Rollback window.**
   - The old containers stay stopped, not removed, for 2 weeks. Their images stay in the registry, and the `certificate-a-final` tag keeps their code.
   - Rolling the app back doesn't roll back library changes. Undo those in bookwarden first.

## 18. Non-goals

The v2 design (2026-10-29) lifts the LLM, scheduled-run, series, tags, comments and cover items below, each as its ADR lands. Merging books stays a non-goal.

- An API that writes, API keys, and agent integration.
- A digest or notifications, and LLM or vision features. Scheduled and incremental runs are in since ADR-039.
- Replacing covers, merging books, and fixing series, tags or comments.
- A library mount, a snapshot copy, or bundling Calibre.
- SQLite, Valkey, and multiple users or roles.
- Becoming a general library manager. The old repo's product boundary still applies.

## 19. Risks and open validations

| Risk | Handling |
|---|---|
| Content-server protocol details (digest auth, msgpack typed values) | Milestone 0 probe before anything else |
| A Calibre upgrade changes a `/cdb/cmd` version | Startup probe with an explicit "incompatible Calibre" error; pin the tested Calibre version in docs and CI |
| Source outages (DNB's MARC export was failing on 2026-10-12) | Explicit `unavailable` outcomes, circuit breaker, RDF schema for DNB |
| CWA writes the same library outside Calibre | Stale check before every write and readback after; races inside CWA's own ingest are outside bookwarden's control |
| No Landlock on Unraid | seccomp, rlimits, timeouts, and pure-Go parsers for everything except OCR |
| Two networks in an Unraid template | Verified at cutover. Fallback: a `docker network connect` post-argument |
| Image size target with tesseract and poppler | Fast tessdata models only; measured in milestone 1 |

## 20. Acceptance

- One bookwarden container runs on the server. The image is at most 100 MB; idle memory is a target of at most 100 MB, measured and reported.
- A read-only run leaves the library byte-identical, proven in CI and spot-checked on the shadow run.
- A full check of the library completes. Differences from the last Certificate A run are explained.
- On the server, a fix is applied, read back, verified in Calibre and CWA, and undone exactly.
- CI is green: tests, fuzz smoke, golangci-lint, `govulncheck`, gitleaks, and Trivy with 0 HIGH/CRITICAL.
- Certificate A is retired: containers stopped, data archived with checksums, and the agent VM's tunnel and root key removed.

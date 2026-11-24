# Bookwarden

Bookwarden checks every book in your Calibre library against the files themselves and national-library records, and applies only the fixes you approve, through Calibre's content server.

The design is in [`docs/superpowers/specs/2026-10-12-bookwarden-go-rewrite-design.md`](docs/superpowers/specs/2026-10-12-bookwarden-go-rewrite-design.md), amended by the [v2](docs/superpowers/specs/2026-10-29-bookwarden-v2-design.md) and [v1.0 production](docs/superpowers/specs/2026-11-04-bookwarden-v1-production-design.md) designs. Certificate A, the Python version, is retired.

Bookwarden is review-first: it never writes to Calibre until you turn writes on, and then only what you approved.

- [Operator guide](docs/operator-guide.md): install, first run, the review workflow, backups, upgrades, troubleshooting.
- [Security policy](SECURITY.md): reporting a vulnerability, supported versions.
- [Threat model and privacy](docs/threat-model.md): what Bookwarden protects, and which text leaves your machine.
- [Changelog](CHANGELOG.md).

## Quick start

Requirements:

- Docker 25 or newer, with Compose v2.20 or newer for the compose file.
- A linux/amd64 host. The image is built for no other platform: the OCR sandbox has no other implementation.
- A Calibre content server (`calibre-server`) that the container can reach, with an account for Bookwarden. Keep the account read-only until you want fixes.
- Postgres 18. The compose file brings its own; on Unraid you run it yourself.

**Docker Compose** runs Bookwarden with its own Postgres 18:

```bash
cd deploy/compose
cp .env.example .env     # then edit it: every value that says change-me
docker compose up -d
docker compose logs bookwarden | grep setup_code
```

Open `http://<host>:8399/` and enter the setup code. Browsers send cookies to every port of a host, so on a server that runs other services give Bookwarden a host name of its own, open it by that name, and put only that name in `BOOKWARDEN_ALLOWED_HOSTS`. Otherwise every other service on that address sees the admin's session cookie ([threat model](docs/threat-model.md#known-limits)). The example sets writes off and has no way to turn on automatic apply. Besides the variables in the [environment reference](#environment-reference), `.env` holds these, which only the compose file reads:

| Variable | Default | Meaning |
|---|---|---|
| `POSTGRES_PASSWORD` | `change-me` | The database password, for Postgres and for Bookwarden's database URL. Letters and digits only. |
| `BOOKWARDEN_VERSION` | `v2.0.0` | The image tag. Pin a release. |
| `BOOKWARDEN_PORT` | `8399` | The host port of the web UI. `127.0.0.1:8399` serves it on this host only. |
| `TZ` | `Europe/Berlin` | The time zone of the container, which the schedules are read in. |

Details and the rest of the settings: [operator guide](docs/operator-guide.md#install). `scripts/quickstart-test.sh` follows these steps in CI, against a disposable Calibre.

**Unraid:**

1. Run a Postgres 18 container, if you don't have one, and create Bookwarden's role and database in it. `createuser` asks for the password; use letters and digits only, because it goes into a URL:
   ```bash
   docker exec -it <postgres container> createuser -U <superuser> --pwprompt bookwarden
   docker exec <postgres container> createdb -U <superuser> -O bookwarden bookwarden
   ```
2. Copy `deploy/unraid/bookwarden.xml` to `/boot/config/plugins/dockerMan/templates-user/my-bookwarden.xml`.
3. In the Docker tab, choose Add Container and pick the template. It pins a release tag, never `:latest`.
4. Choose the networks. The template starts on `bridge`, where Bookwarden reaches Calibre and Postgres by the server's LAN address and the ports they publish. If they are on custom Docker networks, add those at the end of Extra Parameters, `--network <Calibre's network> --network <the database's network>`, and reach them by container name. Unraid then ignores Network Type.
5. Fill in the database URL (`postgres://bookwarden:PASSWORD@HOST:5432/bookwarden`), the Calibre URL, account and password, and the allowed hosts (the names or IPs you open the web UI by, such as the server's LAN IP).
6. Apply, then read the setup code from the container's log and open the web UI.

See the [operator guide](docs/operator-guide.md#install).

## First start

Bookwarden needs a Postgres database and the settings in the [environment reference](#environment-reference). On first start, the container log shows a one-time setup code in a line like this:

```json
{"level":"WARN","msg":"first-time setup: open bookwarden in your browser and enter this setup code","setup_code":"K7QM-2XWD-9RTA-HP4V"}
```

Open bookwarden in your browser and enter the code. Then choose the admin username, and a password of at least 12 characters. If the container restarts before setup is finished, it prints a new code and the old one stops working.

## Upgrading

Schema migrations run at start-up. Take a dump first; with the database URL in your environment as `BOOKWARDEN_DATABASE_URL`:

```bash
pg_dump --format=custom --file="bookwarden-$(date +%F).dump" "$BOOKWARDEN_DATABASE_URL"
```

- **Migration lock.** Instances migrate one at a time. A second one polls the lock and logs `waiting for the migration lock` with the pid of the session holding it, about every 10 seconds. After 2 minutes it gives up with an error naming the holder, instead of waiting for ever.
- **Lock timeout.** Each migration runs in a transaction with `lock_timeout = 10s`, so one that needs a strong table lock fails fast instead of queueing behind a long reader and blocking every query after it. On a lock timeout (SQLSTATE 55P03) the migration is rolled back and retried up to 3 times, with a short backoff and a `migration hit a lock timeout; retrying` log line each time. Then start-up fails with the error. The failing migration is rolled back; earlier ones from the same start stay applied, and starting again is safe.
- **Newer schema.** If the database was migrated by a newer bookwarden, an older binary refuses to start and applies nothing. The error names both versions. Run the newer version, or restore the dump taken before the upgrade.

### Deploying on Unraid

`scripts/deploy-unraid.sh` does a repeatable deploy of an image pinned by digest. Copy it to the Unraid host (for example `/boot/config/bookwarden/deploy-unraid.sh`, which survives reboots) and run it there as root, **inside tmux or screen**: a dropped ssh session must not interrupt a cutover (the script also rolls back on SIGHUP, SIGINT and SIGTERM).

```bash
export BW_PG_USER=<postgres role>            # required
export BW_REGISTRY_USER=<registry user>      # optional: log in to the registry for the pull
export BW_CHANGELOG=/path/to/changelog.txt   # optional: one line is appended per deploy
echo "$REGISTRY_TOKEN" | ./deploy-unraid.sh registry.example/owner/bookwarden@sha256:<digest> <short-sha> "what changed"
```

The label is at least 7 characters: use the short commit sha; for a release image that is the sha of the tagged commit. `bookwarden version` prints the version and the first 12 characters of the commit the image was built from, for example `bookwarden v2.0.0 (abc1234def56)`, and both the pulled image (before anything changes) and the new container must print the label. The token is read from stdin only when `BW_REGISTRY_USER` is set; it goes to `docker login --password-stdin`, and the script logs out right after the pull. Other settings, with their defaults: `BW_CONTAINER=bookwarden`, `BW_TEMPLATE=/boot/config/plugins/dockerMan/templates-user/my-$BW_CONTAINER.xml`, `BW_PG_CONTAINER=postgresql18`, `BW_DB=bookwarden`, `BW_DUMP_DIR=/mnt/user/appdata/bookwarden-backups`, `BW_HEALTH_TIMEOUT=300` (seconds; the migrations run before the server listens), `BW_POLL_INTERVAL=2`, `BW_KEEP_BACKUPS=10` (after a successful deploy, the script's own dumps and template backups beyond the newest 10 are removed; `0` keeps all). Dumps are counted per directory, so give each Bookwarden deployment its own `BW_DUMP_DIR`. `DRY_RUN=1` prints every mutating step and runs none of them.

The order is: pull by digest; preflight (first the pulled image's own `version`, run with no network); stop the old container and rename it to `<name>-pre-<label>`; dump; pin the template; recreate; check; remove the renamed container; remove all but the newest `BW_KEEP_BACKUPS` of the script's own dumps and template backups.

- **Preflight** exits 3 when the pulled image's `version` does not contain the label ("the image's version does not contain the label; nothing was changed"), when the database cannot be queried, a run is `running` or `waiting`, a write is in progress, the template's `<Repository>` is not the image the container runs now (a rollback would start the wrong version), the template does not hold exactly one `<Repository>`, or the dump or template backup file already exists.
- **Dump.** After the stop, nothing can write, so the dump is consistent with the cutover. It is `pg_dump -Fc` to `$BW_DUMP_DIR/pre-<label>-<timestamp>.dump` (mode 600), checked by reading every block with `pg_restore -f /dev/null` (on the host if it has `pg_restore`, otherwise inside the Postgres container). A failed dump starts the old container again and exits 1.
- **Template.** It is backed up next to itself as `.<name>.bak-pre-<label>-<timestamp>` and `<Repository>` is pinned to `image@sha256:...`. The container is recreated from that file through Unraid's own template code.
- **Checks.** The container must report `healthy` within the timeout (a missing healthcheck, `unhealthy` or an exit fail at once), run exactly the pulled image, and `bookwarden version` must print the label.

**Failure.** The script puts the template back, removes the new container, renames the old one back and starts it, then waits for it to be healthy and checks it runs the image it ran before (exit 1; exit 5 if any of that fails). If the new container may have migrated the database, the old binary cannot start on it. The script then stops the new container, reads the schema version (if it cannot, it assumes the schema changed), does not touch the database, and exits 4 with the restore commands below.

**RPO and RTO.** The recovery point is the pre-deploy dump: nothing is written between the dump and the cutover, so a deploy loses no data (RPO 0). Outside deploys it is the age of the nightly dump, up to 24 hours. The recovery time is an estimate, not a measurement: about 15 minutes to stop the container, restore the dump and start the previous version.

**Restoring the database.** `pg_restore --clean` does not drop tables a newer migration added, so restore into a fresh database, with the Bookwarden container stopped. The owner is the one the script printed before the deploy:

```bash
docker exec <postgres container> dropdb -U <role> bookwarden
docker exec <postgres container> createdb -U <role> -O <owner> bookwarden
docker exec -i <postgres container> pg_restore -U <role> --exit-on-error --single-transaction -d bookwarden < pre-<label>-<timestamp>.dump
```

**Manual rollback** when the new version misbehaves after the script finished: the old container is already removed on success, so copy the template backup over `my-bookwarden.xml` (or set `<Repository>` to the previous image), restore the database as above if the new version migrated it, then recreate the container from the template (Docker tab, Apply) and check `/healthz`.

## Forgot the admin password

Delete the account, then restart the container. The log then shows a new setup code. With the official Postgres image:

```bash
docker exec -it postgresql18 psql -U bookwarden -d bookwarden -c 'DELETE FROM users;'
```

## Checking your library

Open **Runs** and choose **Check the library**, or enter one book's Calibre id to check just that book. A check only reads: it lists the library through Calibre's content server and downloads each format through it, never touching the library folder or `metadata.db`. Four books are checked at a time, and one check runs at a time.

For each book, bookwarden records Calibre's record and, for each format, its size, its SHA-256 and what the file itself says, sealed as one evidence package in Postgres. Formats above 512 MiB aren't downloaded, and formats that Calibre lists but whose file is gone are recorded as missing. A book edited in Calibre while it was being checked is marked as changed, and gets no evidence.

Every downloaded format is inspected inside the container by parsers written in Go, then deleted:
- **EPUB:** the zip and every checksum in it, the OPF metadata (title, authors, languages, ISBNs, publisher, date), DRM, and whether the chapters hold text. Obfuscated fonts aren't DRM.
- **PDF:** the cross-reference data (rebuilt when it's broken, as readers do), the Info and XMP metadata, encryption, and whether the first 50 pages hold text. The first and last 5 pages are then searched for ISBNs: through the text layer when there is one, by OCR when there isn't (see below).
- **MOBI and AZW3:** the headers, the EXTH metadata, DRM, and the first text record.
- **CBZ:** the zip, and the first three pages decoded.
- **JPEG, PNG, GIF and WebP:** the whole image decoded; above 24 megapixels or the decoder memory allowance, only its header. This also covers the pages sampled from a CBZ.
- Anything else, CBR included, is recorded as not inspected.

A file is inspected as what its bytes are, whatever Calibre calls it, and the detected type is recorded. States such as corrupt, DRM or unsupported describe the file itself, not Calibre's record of it. Downloading and inspecting a file share a limit of 120 seconds, and archives are refused past 10,000 entries, 4 GiB decompressed or a 100:1 compression ratio. A format that hits a limit is recorded as not inspected, with the limit as the reason.

**ISBN hints from PDFs and EPUBs.** For a readable, unencrypted PDF, `pdftotext` (text layer) reads pages 1–12 and the last 3, and `pdftoppm` and `tesseract` (OCR) read pages 1–5 and the last 5, in the book's Calibre language when it is English, German or Spanish and in English otherwise. For a readable EPUB without DRM, the inspector reads the front matter (the documents before the first body landmark, else the first 6, at most 12) and the last 3 documents, reading the first 32 KiB of text of each except copyright and title documents, which are read whole (a longer chapter or a single-file book is read only at its start, which is not a failure, and an ISBN printed deeper is not seen); a scan that hit a read limit, the total bound of 15 x 32 KiB or a document it couldn't parse is recorded as failed. Each hint keeps where it was found (copyright page, title page, other front or back matter) and what its label says (`ebook`, `pdf`, `hardcover`, `paperback`, `print`, from an English, German and Spanish table). Only checksum-valid ISBNs are kept, at most 16; the hints hold no text (the excerpts below are separate). These ISBNs are hints, never evidence: a PDF's text layer is whatever its producer mapped the glyphs to, and an EPUB's front matter may list other editions or other books, so they seed source lookups (below) but never confirm a record ([ADR-032](docs/decisions/ADR-032-front-matter-evidence.md)). The tools run one at a time in a sandbox: a child re-executed from the bookwarden binary with no network, no new processes, no ptrace or mount, no namespaces, keyrings, bpf, userfaultfd or perf events, no inherited environment, and limits of 60 s CPU, 768 MiB of address space, 64 MiB per file and 64 open files. Each book gets 180 s of tool time, counted only while its tool runs. At startup bookwarden OCRs a built-in test page (`bookwarden ocr-check` runs the same check); if that fails, the log says so and PDFs record their hints as unavailable. Encrypted PDFs and PDFs whose text layer couldn't be verified get no hints ([ADR-018](docs/decisions/ADR-018-ocr-sandbox-isbn-hints-title-sort.md)).

**Front-matter excerpts, and privacy.** So that an admin can see where a hint came from, each EPUB and each PDF with a text layer also keeps a bounded excerpt of the text those scans read: the front matter and last 3 documents of an EPUB, pages 1 to 12 and the last 3 of a PDF's text layer, at most 12,000 characters per format, in NFC with LF line endings. OCR text is never kept. This is book text, and it is handled as such. The sealed package holds only the excerpt's SHA-256 and a map of which page each part came from; the text itself is in the `excerpts` table of bookwarden's own Postgres database, stored by that hash, and is checked against the hash whenever it is read (`bookwarden verify-seals` checks every excerpt a run refers to). It is shown only on a book's page, for a signed-in admin, as escaped text in a collapsed block, with hint ISBNs marked. It is not written to logs, metrics or error messages, it is not in the repository's test fixtures, and it goes to one place only: when extraction is configured, the excerpts of EPUB formats are sent to the extractor endpoint you set (`BOOKWARDEN_EXTRACTOR_BIFROST_URL` or the opencode-go endpoint, see [Privacy](docs/threat-model.md#privacy-which-text-leaves-the-machine)); they never go to the metadata sources or the agents, and without extraction they stay in the database. Retention deletes the excerpts no remaining package refers to. Expect the database to grow by roughly 20 to 80 MB for a library of 3,200 books ([ADR-032](docs/decisions/ADR-032-front-matter-evidence.md)).

PDF parsing and cached streams have additional memory and count limits ([ADR-016](docs/decisions/ADR-016-inspector-memory-bounds.md)). When the text scan runs out of its content budget, unread streams make the text layer unverified. Form resources are scanned in name order so the same bytes produce the same evidence.

**Source lookups.** After a book's files are read, bookwarden looks up to 4 of its ISBNs in the Deutsche Nationalbibliothek (SRU, MARC21), the Biblioteca Nacional de España (SRU, MARC21), Open Library and, when `BOOKWARDEN_GOOGLE_BOOKS_KEY` is set, Google Books. Wikidata (one SPARQL query for the hyphenated ISBN-13 and ISBN-10; an ISBN in an unassigned range is `not_queried`) and, when `BOOKWARDEN_HARDCOVER_TOKEN` is set, Hardcover (GraphQL, 1.1 s apart) are looked up too, but their records are only sealed and shown: decisions up to version 11 ignore them, and the agent panel doesn't see them ([ADR-035](docs/decisions/ADR-035-identity-v6-and-more-sources.md)). The ISBNs come from the files' own metadata first, then Calibre's `isbn` identifier, then PDF and EPUB hints; the book page shows which classes each one came from. A source receives only the ISBN: no title, author or file. A record counts only if its own identifiers carry the queried ISBN, and its fields are kept as the source states them, capped at 512 characters per value and 16 per list. Each source answers `ok`, `no_match`, `not_configured` or `unavailable`, and the answers are sealed with the book's evidence. `ok` answers are cached for about 30 days and `no_match` for about 7 (each ±25 % at random, so answers written together don't expire together); failures are never cached, so the next check asks again. An expired answer is kept for 60 days and served as it was cached once the check has used its refresh budget: an incremental check asks at most 100 expired answers again per source, a full check and a one-book recheck ask all of them; a source that answers `unavailable` to a refresh has its old records served, but not an old `no_match`; a source whose key was removed answers `not_configured` and its cached answers are not used ([ADR-044](docs/decisions/ADR-044-source-cache-refresh-budget.md)). After 5 consecutive `unavailable` answers, a source is skipped for the rest of that check. Google Books is also probed at the start of each check with a volume it certainly holds. If that comes back empty, as every `isbn:` query has since late September 2026, or fails, Google Books is skipped for the whole check: it is not asked, a `no_match` it answered earlier isn't used, and Settings shows since when. Its records cached earlier still count. Requests are paced at one per second per host and retried on 429 and 5xx within 30 seconds; a 429 or 503 with `Retry-After` holds that host for every worker for as long as it asked, up to 15 seconds, and a request that can't wait that long gives up at once. Requests are refused if they redirect off the host or resolve to a private or special-purpose address, Tailscale's 100.64.0.0/10 included. Cached records are dropped when the rules that read a source change ([ADR-019](docs/decisions/ADR-019-source-lookups.md)). Wikidata's series (P179, with the ordinal P1545) were added without changing that version, so a Wikidata answer cached before schema 5 shows no series until its cache row is refreshed.

**Outbound network.** Bookwarden's container must reach `services.dnb.de`, `catalogo.bne.es`, `openlibrary.org`, `www.googleapis.com`, `query.wikidata.org` and `api.hardcover.app` over HTTPS. If it can't, lookups are recorded as `unavailable` and checks still complete.

If Calibre stops answering during a check, for example while its container updates, the check waits and continues once Calibre is back. A check that was running when bookwarden stopped continues after a restart. Bookwarden keeps the last 10 checks by default. Calibre being unreachable shows on the Overview page; it never makes the bookwarden container unhealthy.

## Scheduled and incremental checks

A **full** check downloads and inspects every format, about 80 minutes for 3,200 books. An **incremental** check (the second choice on the Runs form, or the schedule below) reuses a book's file evidence (what each format's file said about itself, its ISBN hints, its excerpt and the cover measures) from that book's newest sealed package, and downloads nothing for it, when all of these hold:

- Calibre's record of the book is as it was: the same last-modified time, identifiers, and the same formats with the same sizes and file times, and the same cover flag;
- the package is of the current schema version, and the book's languages are the same;
- it was made by a build with the same file inspectors and limits, the same Go toolchain and image and text libraries, and the same `pdftotext`, `tesseract` and tessdata (stored with each package as its inspector key, not sealed);
- its file evidence was measured at most 8 days ago (a reuse never renews that date);
- nothing in it came from a timeout or a failed read; and
- every excerpt it refers to is still stored and matches its hash.

Anything else is checked in full, in the same run. The source lookups, the findings and the proposals are always computed again, at the current rules, and the package is sealed again in the new run. A reused package is byte-identical to the one a full check would seal from the same files. The run page says how many books reused their file evidence. The first incremental check after an upgrade that changes the inspectors checks everything in full. A file changed in place without its size or Calibre's last-modified time changing (bit rot, `cp -p`, a cover replaced behind Calibre's back) is not noticed by an incremental check; the 8-day limit means the next full check, at the latest, reads it again ([ADR-039](docs/decisions/ADR-039-scheduled-incremental-runs-and-retention.md)).

**Schedule.** Both are off by default. Settings shows what is set, the next due times and the last scheduled run; it does not change them. Times are in the container's time zone (`TZ`, which Unraid sets; the image carries its own copy of the zone database).

| Variable | Meaning |
|---|---|
| `BOOKWARDEN_SCHEDULE_INCREMENTAL` | A daily time, `HH:MM` (24-hour), for an incremental check. Empty turns it off. |
| `BOOKWARDEN_SCHEDULE_FULL` | A weekday and a time for a full check: `sun 04:00` (`mon` to `sun` in lower case, then `HH:MM`). Empty turns it off. On that weekday the incremental check is left out. |
| `TZ` | The time zone the two times are read in, such as `Europe/Berlin`. Unset means the container's own zone, UTC in this image. An unknown name stops the start, but only when a schedule is set. |
| `BOOKWARDEN_RETAIN_RUNS` | How many of the newest runs to keep (default `10`). `0` keeps every run. |

A value that doesn't fit stops the start with a message that names the variable. A scheduled check starts only when nothing else is going on: if a check is running, changes are being written to Calibre, an undo is running, an agent review is running or Calibre isn't ready, that slot is skipped and logged (`schedule: skipping the slot`), and not retried later. A slot whose start fails for another reason, such as the database being away or Calibre's book list failing, is tried again every 30 seconds for 15 minutes, then dropped, logged (`schedule: slot dropped`) and shown in Settings as skipped; if another slot falls due within those 15 minutes, the newer one is tried instead. After a restart, a slot that was missed within the last 12 hours and that no scheduled run served starts once; older ones are dropped. At each clock change, a time that is repeated counts once and a time that is skipped runs just after the jump, so every day has exactly one slot.

**Retention.** After each check ends, and once a day, bookwarden deletes the runs beyond the newest `BOOKWARDEN_RETAIN_RUNS`, with their books and evidence, and the stored excerpts no remaining package refers to. It always keeps the newest completed run and every run newer than it, every run a decided fix or an operation points to, and any run in progress. The log says how many runs and excerpts went, never which.

## Findings and historical books

**Findings** opens the latest completed whole-library check, or the latest completed, cancelled or failed check if there is no completed whole-library check. Choose a particular run from **Runs** to inspect its frozen books and findings. Active runs show partial results. Both lists display 50 rows per page; clean books and operational outcomes remain reachable from the run.

Book pages show the Calibre snapshot, file metadata, hashes and inspection qualifications recorded during that check. Metadata that exceeded the inspector's capture limits is marked incomplete and excluded from comparisons. Older evidence at a capture boundary remains unverified because its completeness is unknown. Local findings cover file states, conservative metadata comparisons, checksum-valid ISBN identities, date precision, the preserved author-sort rules and, from findings version 5, Calibre values that are junk by a closed list: file-name or artefact titles, imprint, translator, author-prefix and edition noise in titles, several people in one author entry, and placeholder, website or scanner publishers ([ADR-035](docs/decisions/ADR-035-identity-v6-and-more-sources.md)), and, from findings version 6, a series the file names that Calibre lacks (`series_missing`) a series name that differs from Calibre's (`series_name_mismatch`, info; case, punctuation and a leading English article are ignored) or an index that differs numerically (`series_mismatch`); these never propose a fix. They are observations for review. Source records, the book's tier and its proposed fixes appear on the same page. Title sorts are compared with Calibre 9.16's own rule for the book's first language (English when it has none): an empty sort, a sort that keeps the leading article, and a sort that matches neither the title nor any language's articles, which is what a retitle leaves behind, since Calibre doesn't refresh it. A sort that another language's articles explain is accepted, because Calibre-Web and non-English Calibre installs use other article sets.

Every report checks the evidence seal, frozen identity and complete finding projection before applying filters. A damaged or missing checked result makes the report unavailable. Historical findings and projections retain their original derivation rules; the current report view adds capture qualifications in memory without rewriting sealed evidence or stored projections. A whole-run seal check does that verification once when a run ends, in an hourly sweep of the stale ones and on `bookwarden verify-seals [--run N]` (it checks one run or all, prints a line per run and exits 1 on damage); Findings, Fixes, Books and CSV pages then read what the check kept, so they stay fast, and a run without a check is verified page by page as before (ADR-038). Writes always verify their own package.

**Download this selection as CSV** exports the current code, format and book-id filters, starting from the whole selection rather than the visible page. An export allows 50,000 finding rows and 32 MiB including the header, with a 20-second budget and one export at a time. Narrow the filters when it exceeds these limits. The complete verified file is staged before download starts. Text cells normalize whitespace, remove other control/format characters, cap at 512 runes and prefix an apostrophe for leading `=`, `+`, `-`, `@` or their full-width forms; CSV quoting preserves commas and quotes. This specifies the encoded file, not behavior after arbitrary spreadsheet resaves.

**Recheck this book** starts a check of that frozen book only. Bookwarden resolves the historical library and UUID itself and compares them with Calibre before creating the run. A removed book, reused id, changed library or active check prevents the recheck. All reads, exports and recovery probes remain bound to the run's captured library, even if another probe observes a different default library.

**Covers** are checked too ([ADR-023](docs/decisions/ADR-023-cover-checks.md)): a book without one, a cover that doesn't decode, one whose shortest side is under 600 px, and one that looks like a placeholder (a near-uniform image, or under 50 px) each get a finding. The Book page shows the cover's size, type, dimensions and hashes, not the image. Checks made before covers were checked show "Not checked".

**Cover candidates** ([ADR-043](docs/decisions/ADR-043-cover-candidates.md), findings version 6) measure the cover image an EPUB's own OPF names, and never write a cover. The image is the manifest item with the `cover-image` property, else the EPUB 2 `cover` meta, else a guide `cover` reference that points at an image (an XHTML page is skipped). It is measured like Calibre's cover (dimensions, 64-bit difference hash, flatness) under the same decode limits and 20 MiB cap, and only the measures, the image's SHA-256, its type and a state (`ok`, `missing`, `corrupt`, `too_large`, `not_inspected`) are sealed, never the image. It costs no network request. The finding `cover_better_available` (info, reason `epub`) appears when Calibre's cover is missing, under 600 px or a placeholder and the candidate is `ok`, at least 600 px on its shortest side and not a placeholder. When Calibre has a cover, the candidate must also be at least 1.5 times its shortest side and show the same artwork: a Hamming distance of at most 12 bits (`CoverMaxHamming`) between the two difference hashes. A near-uniform Calibre cover (flatness under 3.0) has no artwork to compare, so it is treated like a missing one; a tiny cover that isn't flat keeps the checks. Open Library covers were measured and rejected (redirects to archive.org, at most 500 px tall; see the ADR). The Book page lists the measures next to the cover's, not the image.

**Possible duplicates** ([ADR-024](docs/decisions/ADR-024-duplicates.md)) are compared within a whole-library check: books with the same ISBN (from Calibre or a file's own metadata, never OCR), the same file, the same title and authors, or a near-identical cover (a hint only; plain, blank and placeholder covers aren't compared). The Duplicates page lists the groups by kind, and each Book page lists the book's own. Bookwarden never merges or deletes a book: merging is done in Calibre. Checks made before duplicate detection say so.

## Tiers and proposed fixes

Each checked book gets a tier, recomputed from its sealed evidence whenever it's shown, so a stored tier is never trusted:
- **A:** every format is readable, all formats carry the same single ISBN, their title, authors and language are complete and agree, and an exact source record for that ISBN agrees with all of them. A record may leave out the language, list translators or editors too, keep the subtitle in its own field, or differ by a trailing parenthetical that names no volume, a leading "The", "A" or "An", or "&" for "and" ([ADR-030](docs/decisions/ADR-030-record-match-tolerances.md)).
- **B:** something is missing: an unreadable format, no ISBN or several, an incomplete field, or no source record that matches. The book page lists the reasons.
- **C:** something conflicts: two formats disagree on the ISBN, title, authors or language, or two sources' records do, or the ISBN's records describe another work. Tier C books get no proposed fixes.

A fix is proposed for the title, authors, languages, the ISBN identifier, publisher or publication date when two independent roots agree on a value that differs from Calibre's, and at least one of them is the files or a national library (DNB, BNE). The files count once, however many formats agree, and so does each source, however many records it returns. Open Library and Google Books count as roots but never suffice together. A value isn't proposed if the files or a national library say something else. Only records for the ISBN the files themselves carry count: records found through Calibre's ISBN or a PDF hint are shown but never back a fix. Decision version 6 adds one exception ([ADR-035](docs/decisions/ADR-035-identity-v6-and-more-sources.md)): when no format's metadata carries an ISBN, the single ISBN an EPUB's copyright or title page prints for the e-book (else the one unlabelled ISBN, never a hardcover, paperback, print or PDF one) selects whose records are used, once a record matches the files in title (exactly), authors and language. Any record conflict, a garbled metadata ISBN, a failed scan of another EPUB or a cut-off hint list leaves the book without an identity. Such a book is tier B with the reason `identity_frontmatter`, the values still need the usual two roots, and the page's ISBN is never proposed itself. If the records for the identity agree on a title and authors that both differ from Calibre's, the ISBN belongs to another work: the book is tier C with `other_work`, gets no proposals at all and appears on the Findings page; empty or placeholder Calibre authors such as "Unknown" are a fill, not another work. Decision version 7 ([ADR-030](docs/decisions/ADR-030-record-match-tolerances.md), "Decision v7 tolerances") stops two records from conflicting over a leading article, "&" for "and", an apostrophe variant, an imprint series in the title, a honorific, initials punctuation, or up to two contributors listed as authors; only tiers move, never a proposed value, and the ISBN proposal still needs an identical title. Wikidata and Hardcover records are shown but still count for nothing. Differences only in case or spacing, subtitles and year-only dates are never proposed, nor is a title that only drops words from Calibre's, such as its subtitle or a leading article ([ADR-026](docs/decisions/ADR-026-title-proposals-never-shorten.md)), nor authors whose surnames Calibre already lists, which would only drop, reorder or re-form its names ([ADR-027](docs/decisions/ADR-027-author-proposals-never-drop-or-reform.md)); languages are proposed as Calibre's three-letter codes. An ISBN is proposed when the files' own metadata carries it and a source record for it agrees with the files' title (from decision version 6 the title key must be identical and the record must also match the files' authors and language; up to decision version 7 an ISBN taken from a copyright page is never proposed; version 8 changes that for the printed text, below) ([ADR-020](docs/decisions/ADR-020-tiers-and-proposals.md)).

Decision version 8 ([ADR-036](docs/decisions/ADR-036-text-root.md)) adds the book's printed text as a root named `text`, for the title, authors, publisher and ISBN. It is read from the sealed extraction of the EPUB formats (M4): only values the extractors agree on count, a field they disagree on gets no proposal at all, and two EPUBs that print different values give no value, veto every proposal for that field and get the reason `formats_text_conflict` (tier B). A printed ISBN never counts if it is labelled hardcover, paperback, print or PDF, or if it sits in back matter, and a text-alone ISBN fill needs the e-book label or the only ISBN printed in the book's front. The text is not proposed when it only differs from Calibre's value by case, punctuation, "&", an article, a corporate suffix of the publisher or names the style guide keeps; otherwise it needs a second root that says exactly the same (the files, or a source record, for an ISBN one that matches the book), and replacing a real title, authors list or ISBN needs a source record among them; a real publisher is never replaced from the text. Or it proposes alone over an empty or junk Calibre value (a file-name title, no authors or a placeholder such as "Unknown", an empty or placeholder publisher, no ISBN) when no other root says something different. A title shortens only by series, edition, translator or author-prefix noise, with a second root, and never loses a subtitle. Where the text exists and the roots can't agree, nothing is proposed and the book page and Findings show `text_roots_disagree`, which does not change the tier; records alone no longer propose a field that has a text value. The text title and authors both differing from Calibre's is also `other_work`. A fix that rests on the text is checked again when it is approved and when it is applied: the extraction is re-derived from its stored excerpt and raw answers, as `verify-seals` does, and the fix is refused if anything differs. This edition's year gets no text value: pubdate proposals are unchanged.

Decision version 9 ([ADR-036](docs/decisions/ADR-036-text-root.md), "Decision v9") fixes two things production run 15 showed. A pubdate proposal that only restates Calibre's timestamp in another time zone is dropped: Calibre stores a date picked at local midnight as UTC (`2005-03-24T23:00:00+00:00` is 25 March in Berlin), so a proposed date that is the local calendar date of Calibre's timestamp in some UTC offset from -12h to +14h is no change; a date without a time of day is still exact. And a publisher the text proposes is no longer shown in capitals: another supporting root's spelling is used when it states the same value, otherwise a printed value with no lowercase letter is title-cased (`HYPERION PRESS, INC.` becomes `Hyperion Press, Inc.`); a value with any lowercase letter stays as printed. Keys, equivalences and dissent are unchanged.

Decision version 10 ([ADR-045](docs/decisions/ADR-045-decision-v10-text-root-corrections.md)) corrects the text root. A printed author list that is not the same people as a catalogue's (another first author, or more than two extra names) is flagged instead of filled. A title, author list or publisher the text alone would propose in capitals takes another root's spelling, or is not proposed (v9's title-casing turned `MIT PRESS` into `Mit Press`). A pubdate Calibre would store as another day (the 1st, or the last day of a month other than December) is not proposed. A catalogue record speaks about the title and authors only when it is the same book in the same language. A record's subtitle field joins its title in the text path; without a text title a title shortens only by its noise; and `Who?` with the subtitle `Sub` reads `Who? Sub`. Packages sealed at earlier versions keep their decisions.

Decision version 11 ([ADR-048](docs/decisions/ADR-048-decision-v11-restorable-proposals.md)) proposes a fix only when an undo could put Calibre's current value back exactly, the rule Apply already enforced: no fix over a pubdate on a day Calibre moves when it stores a date (the 1st, or the last day of a month other than December), over a title, sort, name or publisher with doubled or edge spaces or a control character, over author lists with "&", "|", an empty name or two names that differ only in case, over invalid or repeated language codes or unclean identifiers, or over an empty author list (the full list is in the ADR). The wrong value stays in Calibre; correct it there by hand.

A wrong title sort or author sort gets Calibre's own value, in any tier ([ADR-028](docs/decisions/ADR-028-sort-fixes-from-calibre-rules.md)): the title sort Calibre computes from the title and language, and the author sort it joins from its authors table. An author sort is proposed only when every author's entry in that table is Calibre's default for the name; a customised or wrong entry is left for Calibre's Manage Authors. Neither is proposed beside a fix to a field its rule reads (the title or language for the title sort, the authors for the author sort), because the sort would come from the old value.

**Fixes** lists the proposed fixes of the same run Findings opens, filtered by field, tier, book or decider, searched by title or author (case and accents ignored) and sorted by book, field, tier or latest decision, 50 rows per page; the search, filters and sort apply before the page is cut. Fixes you approved or rejected in an earlier run of the same library come back with your decision when they are identical (when the new run is first checked, before the agents review it), marked "carried from run N"; a carried approval is applied once ([ADR-034](docs/decisions/ADR-034-blind-individual-decisions-are-labels.md)); change one like any other decision. Approve or reject fixes one at a time or by ticking; see [Fixing](#fixing) below.

## Fixing

Bookwarden writes a fix to Calibre only after you approve it, and it is applied ([ADR-021](docs/decisions/ADR-021-guarded-writes.md), [ADR-022](docs/decisions/ADR-022-fixing-pages.md)):

1. **Fixes:** approve or reject each proposed fix of a finished check with its own buttons, or tick several and use "Approve ticked" / "Reject ticked", or use "Approve all shown" for the filtered page (up to 50; fixes you rejected stay rejected). "Approve all open fixes" approves every undecided fix of the run on every page, one fix at a time, except muted types and fixes Calibre can't store exactly; a fix someone decides while it runs keeps that decision, and the page lists what it left out. Nothing is pre-ticked. A fix you approved or rejected that the next check proposes again, identical in book, field, values and roots, keeps your decision there, marked "carried from run N"; an approval carried this way is applied once. A fix Calibre can't store exactly shows why instead of a tick. See [Decision labels](#decision-labels).
2. **Apply approved fixes** runs in the background, one book at a time. **History** shows it running, then its counts, and every operation with each value before, as written and after.
3. **Undo** on an operation's page writes the old values back.
4. **Incidents** lists writes that didn't complete. While one is open, writes pause and every fixing page says so; acknowledge it after checking the book in Calibre.
5. **Settings** holds the "Allow changes to Calibre" switch, its log, agent fixing and the status of the metadata sources.

Stopping bookwarden lets the book being written finish; the rest of the approved fixes stay approved for the next Apply. A stop cut short, for example by Docker's 10-second stop timeout, is finished at the next start, as after a crash.

- **What is written:** only approved fields: title, authors, languages, the ISBN identifier, publisher and publication date. Approvals copy the sealed proposal, and only the newest check of a book can be approved.
- **The switch:** "Allow changes to Calibre" is off until you turn it on, and every change to it is logged. Keep the Calibre account bookwarden uses read-only until then, and give it write access when you turn writes on. A write Calibre refuses because the account is read-only, with the book unchanged, fails those fixes with `calibre_refused_write` and stops the apply; it opens no incident and doesn't pause writes, and the fixes not yet written stay approved.
- **Before each book**, bookwarden reads it again. If anything changed since the check (a field, the title sort, a file, or a newer check of the book), its fixes become stale and nothing is written.
- **Blocked fixes:** Calibre stores some values differently from what it's sent, so these are never written: dates on the 1st of a month or the last day of any month but December, author names with `&` or `|`, and any value Calibre couldn't put back exactly on undo. An author or publisher that differs from an existing name only in case is blocked too, because Calibre would rename it on every book.
- **After each write**, bookwarden reads the book back twice, from Calibre's answer and from a fresh read. On any difference it writes the old values back, opens an incident, and pauses all writes until you acknowledge it. Every step is recorded before the next one, so after a crash bookwarden finishes the write first: it puts back only the fields that still hold what it wrote, with their title or author sort (Calibre recomputes those, so a sort you changed alone goes back too), leaves any other field you changed since, and records the book as it found it before writing anything back. If Calibre refuses that write for good, for example because the account was made read-only, the operation ends failed so you can check the book and acknowledge the incident.
- **Undo** writes the old values back, as its own recorded operation, while the book is unchanged since the fix. A title fix lets Calibre recompute the title sort and undo restores yours. An author that a fix leaves without books is deleted by Calibre with its own sort and link; undo brings the name back, not those extras.

## Keyboard on the Fixes page

With JavaScript on, `j` and `k` move between rows, `a` and `r` press the focused row's own Approve or Reject button (an individual decision, so the label rules below apply unchanged), `s` skips to the next row and `?` shows the help. Keys do nothing while you type in a field, and they act on the focused row only. Without JavaScript the page works the same with the mouse.

## Decision labels

Bookwarden records how each fix was decided ([ADR-034](docs/decisions/ADR-034-blind-individual-decisions-are-labels.md)). The agents' verdicts stay hidden on undecided fixes until you choose "Show agents' verdicts" on the Fixes page. Only a decision made with the per-row buttons and the verdicts hidden counts as a label; ticked batches, "Approve all shown", "Approve all open fixes", decisions made with the verdicts shown and the agents' own decisions never do. The **Precision** page (linked from Fixes) counts the labels per fix type, meaning field, evidence path and fill or replace, with the one-sided 95 % lower bound on precision and the progress toward 598 labels with zero misses. Nothing is automated by it yet.

## Muting fix types

A fix type whose fixes you keep rejecting is muted, so the inbox stays worth reading ([design section 6.3](docs/superpowers/specs/2026-11-04-bookwarden-v1-production-design.md)). Muting only hides: it never decides, never applies and never changes a tier, a seal or a proposal.

- **The type** is the same as for labels: field, evidence path (the roots sorted and joined with `+`) and fill or replace, counted over all runs of a library.
- **What counts:** each fix once, by your latest decision on it, whether you decided it alone or in a batch and whether or not the verdicts were shown. Carried decisions and the agents' decisions don't count, except a carried approval once it is applied: undoing that write is a miss. Approved, applied, stale, blocked and failed are correct; rejected is a miss, and so is undoing an applied fix.
- **The rule:** a type with at least 10 decided fixes is muted when the one-sided 95 % upper bound on its precision is under 50 %. Ten fixes with one approval are muted (39.4 %); with two they aren't (50.7 %).
- **In the inbox,** the default view of Fixes leaves out the fixes of muted types and says how many it hid, with a link. "Muted types only" shows exactly those; they can be approved and rejected like any other fix. They stay in the findings.
- **Unmuting** is a button on the Precision page, next to each muted type's counts. It is recorded with who and when and holds until 10 more fixes of that type have been decided; then the rule applies again. Deciding a fix the same way again, for example with "Approve all shown" on fixes you already approved, counts as deciding it again. A mute itself is never stored: it is worked out from the decisions each time, so it lifts by itself when later decisions improve the type. The Precision page shows when it began (the decision that made the rule true, counting from the latest unmute) and that the rule did it.

## The recall sample

Precision comes from your decisions in the inbox. Recall, what Bookwarden doesn't flag, comes from a blind sample of 200 books ([design section 6.4](docs/superpowers/specs/2026-11-04-bookwarden-v1-production-design.md)). Open **Sample**, optionally type a seed, and draw one.

- **The draw** is from the verified projection of the latest checked whole-library run, stratified by format (EPUB, PDF, other), language (the first Calibre language: the five most common, else other), tier and whether the book has a fix. Each stratum's share of the 200 equals its share of the library, by largest-remainder rounding. Strata too small for a book of their own (under one in 200 of the library) are pooled by fix state first, so every library book has a weight and none is left unrepresented. A library of 200 books or fewer gives every book. The same seed on the same library gives the same books in the same order; a random seed is recorded when you leave it empty. The fixes each sampled book had (and the findings and decision versions) are copied into the sample at the draw, so pruning, a rebuilt projection or a broken seal can't change a result later. The run is also pinned (the run's one pin, however many samples use it) so the blind pages can still read their excerpts; if the evidence is gone anyway, a page says so.
- **Blind answers.** `/sample/{id}/book/{n}` shows Calibre's title, authors, publisher, pubdate, ISBN and languages, and the book's front-matter text from its sealed evidence. It never shows Bookwarden's proposal, findings, tier or roots. For each field you pick Calibre right, Calibre wrong (optionally typing the right value) or skip, then save and move on. The "Calibre right" radio of field N has the access key N, and Save has `s` (Alt+Shift+key in most browsers). A typed value counts as "wrong" whichever radio was left on. Answers can be changed while the sample is open; the latest answer to a field wins and the earlier ones stay in the table.
- **Progress only until you finish.** The recall is Bookwarden's opinion of the books you answer blind, so `/sample` shows only progress until every book has an answer. Showing the results closes the sample (the time is recorded once, append-only) and further answers are refused; draw a new sample to answer again.
- **Confirmations, not labels.** The answers measure recall and never certify, approve or apply anything. They live in their own append-only table and never reach the decision labels, the muting counts or the proposals.
- **Recall** per field is the share of books you called wrong that have a fix for the field in the sample's run. If you typed a value, the fix must propose the same value under ADR-036's equivalences (titles, authors, publisher suffixes, the same year, the same ISBN-13). Each book counts as many library books as its stratum has per sampled book, so strata are reweighted by library share. Skips count nowhere. A book you called right that has a fix is a false alarm; they are counted and listed, and precision itself stays with the inbox decisions. The interval is approximate: a 95 % Clopper-Pearson interval at the effective sample size n<sub>eff</sub> = (Σw)² / Σw² over the wrong books, so unequal weights, a design effect of n / n<sub>eff</sub>, widen it; it is approximate. Strata with no sampled book are counted as unrepresented.

`bookwarden eval --recall [--sample N] [--json]` prints the same table for the newest sample (or sample N) through a read-only connection, with counts and book ids only, and only for a sample that is closed; otherwise it prints progress. It also prints the decision version the sampled run was checked under.

## Measuring a rule change

Before a new decision version runs on the real library, measure what it would change on a run you already have. `bookwarden eval` opens the run's sealed packages, verifies every seal, and derives the decision twice: at `--baseline` (default: the version each package was sealed with) and at `--version`. It reads only `BOOKWARDEN_DATABASE_URL`, through a read-only connection, and writes nothing.

```bash
docker exec bookwarden bookwarden eval --run 42 --version 6
docker exec bookwarden bookwarden eval --run 42 --baseline 5 --version 6 --books
docker exec bookwarden bookwarden eval --run 42 --baseline 4 --version 6 --json
```

The summary lists the books compared and the books with a broken seal, the A/B/C tier transition matrix, per field the proposals at both versions and how many were added, removed or changed (a different proposed value or roots), the reason codes gained and lost, and the fix types (field, evidence path, fill or replace) at `--version` that [ADR-034](docs/decisions/ADR-034-blind-individual-decisions-are-labels.md) counts labels for. `--books` adds a line per changed book with its id, tier transition and fields; `--json` prints the same data as JSON. The output has counts and book ids only, never titles, authors or other library text, so it can be pasted into a review. A broken seal is counted and listed, never skipped, and makes the exit code 1; a version outside 1 to the current one is a usage error (exit 2).

### Measuring the shadow extraction

`bookwarden eval --run 42 --extraction` (add `--json` for JSON) measures the run's shadow extraction (M4) over the same read-only, seal-verified walk:

- **Per extractor:** its calls by state (`ok`, `unavailable`, `quota_exhausted`, `invalid_output`, `timeout`, `model_mismatch`), and how many the cache answered.
- **Per key:** the formats where the voices agreed, where only one voice grounded it, and where they disagreed.
- **Coverage and agreement:** coverage is the share of formats some extractor answered that give the key. Agreement is the share of those where the voices agreed.
- **Comparisons:** each agreed value is compared with Calibre's value and with the exact-ISBN records.
  - Case, diacritics and punctuation are ignored.
  - A record matches when any one of them has the same value. "None" means the reference has no value.
  - The style guide's forms count as matches: Calibre's title as "Main: Subtitle", and Calibre's one ISBN among those printed.
  - Only `year/this_edition` is compared with a date.
  - ISBNs are not compared with records, which were looked up by the book's own ISBNs.

The output has counts only. A broken seal makes the exit code 1, as above.

`bookwarden eval --run 42 --extraction --reverify` (also `--json`) measures a change to the grounding verifier (`internal/extract`, `extract.Version`) over the whole stored run without asking a model again. Since extract v4 ([ADR-046](docs/decisions/ADR-046-extract-v4-folds.md)) the verifier folds letter case, dash variants and U+200B, U+FEFF and U+2060 where a field's transform compares the model's value with the print (titles, subtitles and imprints: case, dashes and the three invisible characters; names, series and editions: case only; ISBNs: any dash between the groups), and the sealed value is still the print. For every format with an extraction it loads the sealed excerpt and each extractor's stored raw answer, runs today's parsers, verifier and agreement over them as the extraction service does after the models answer, including its check that the block can be sealed, and compares the result with what was sealed. An answer that parses today counts as `ok` and one that doesn't as `invalid_output`, whatever state it was sealed in. Nothing is written. It prints the sealed `extract_version` against the current one; per source (the parsers and each extractor) the claims by status, sealed against re-derived with the delta; `value_not_derived` and `role_unproven` by field, sealed against re-derived with the delta, and the re-derived ones again by the role or label the claim asked for (re-derived only: a sealed ungrounded claim has lost its role and label, so there is nothing to compare), which is where the next verifier change is aimed; the models' re-derived `value_not_derived` values by why they were refused and field, as values and books: `crosses_role_line`, else the first of `case_only`, `dash` and `format_char` (folds that add up) under which the field's own comparison would accept the value, `edge_trim`, `value_not_in_quote`, or `other` (a value the verifier's own v4 folds already accept was refused for something else, so it is `other`); a diagnostic that is never sealed; per key the agreed, single and disagree counts, sealed against re-derived; and the formats whose agreed set changed, with their book ids (each book once). It also counts the formats where an extractor's raw answer parses differently from its sealed state, the formats whose re-derived block would not seal, and the formats skipped because the excerpt or a raw answer is missing or corrupt (it no longer matches its hash); a skipped format is not an error. Packages whose seal doesn't hold are left out of all of that, counted and listed as in the plain `--extraction` report, and make the exit code 1. The output has counts and book ids only, never an excerpt, a quote, a claimed value or a raw answer.

## Why did an ISBN scan fail?

A sealed ISBN hint of state `failed` or `timeout` carries no reason. To see why a book's formats scanned the way they did, run the read-only diagnostic. It exports the book's formats through the content server into a temporary directory, under the same limits as a run (512 MiB per file), inspects each one, prints a summary and deletes the files again.

```bash
docker exec bookwarden bookwarden inspect --book 123
docker exec bookwarden bookwarden inspect --book 123 --format EPUB --json
docker exec bookwarden bookwarden inspect --book 123 --timeout 5m
```

Per format it prints the detected kind and state, the ISBN hint's state, method and reason, the spine documents the scan read with their landmarks (`2:copyright`, `(window)` for one read only to its first 32 KiB of text, which is not a failure, and `(partial)` for one it couldn't read to its end), the number of ISBNs found, the excerpt's character count with whether it was truncated and complete, and export and inspection times. The reason is one of `text_budget` (the scan's total text bound, 15 documents of 32 KiB, ran out), `doc_read_limit` (a document was cut at the 4 MiB per-document read limit before its 32 KiB window was filled; `doc_window` marks a document read only to its first 32 KiB, in the JSON's per-document `reason`, and is never the scan's reason), `xml_limit` (an XML entry was larger than that bound), `xml_error` (a document or the package file didn't parse), `nav_unreadable` (a navigation document was missing or didn't parse), `zip_limit` (a zip limit was hit), `deadline`, `drm` or `other`. When several apply, the first the scan met is shown, except that `deadline` replaces an earlier reason, since it says where the scan ended. `--timeout` (default 2 minutes) bounds each format's export and inspection together. A format for which Calibre reports an error shows `reason=calibre_error` and the command goes on to the next one; Calibre's own message is never printed, as it can quote the library path (author and title). Any other failure ends the command with only a class (`calibre_unreachable`, `timeout`, `export_error`).

**Avoid running it during a check: it shares the container's `/tmp` with the check.** It therefore skips formats over 64 MiB (`reason=too_large`; `--large` lifts the cap), refuses a format when `/tmp` has less than twice its size free (`reason=insufficient_space`, with a message on stderr), works in its own `/tmp/bookwarden-inspect-*` directory and removes it afterwards, and at start removes such directories left by an earlier run that was killed, once they are over an hour old. A format whose size Calibre doesn't report is not exported at all.

The output has counts, states and landmarks only: no book text, titles or ISBN values, so it can be pasted into a review. The reason is a diagnostic: it is not part of the sealed evidence, and a run logs it only at debug level (`BOOKWARDEN_LOG_LEVEL=debug`). The command needs only the `BOOKWARDEN_CALIBRE_*` variables, not `BOOKWARDEN_DATABASE_URL`, and writes nothing to Calibre or to the database. PDFs are inspected but not OCR'd, so they show no ISBN hints.

## Agent fixing

Agent fixing lets three LLM judges from three vendors review a check's open fixes ([ADR-029](docs/decisions/ADR-029-agent-fixing.md)). It is off by default, and it never decides anything: the judges' verdicts are a second opinion, shown after you have decided. Mode "on", where a unanimous panel approved fixes and they were applied without a click, was retired on 2026-11-04 (see the amendment in ADR-029). A database that still holds "on" reads as shadow.

- **Shadow:** the judges review and their verdicts are recorded, but nothing is decided: no fix is approved, rejected or applied by them. Everything waits for you on the Fixes page; the verdicts of an undecided fix appear once you choose "Show agents' verdicts", and always on the ones you decided. The filter "Decided: disagree" lists the fixes where the panel was not unanimous.
- **Off:** no review is queued or run.
- **What the judges see:** each judge gets one packet per book, built from the sealed evidence: the open proposals, Calibre's record, the files' metadata and the source records. Packets carry no file text, OCR or covers. A judge answers approve, reject or unsure per field and can't change a value. A fix you decided is never sent.
- **Limits:** tokens over all judges and reviews of one check are capped by `BOOKWARDEN_AGENT_MAX_TOKENS_PER_RUN` (default 20000000). Any judge error leaves the fix for you.
- **Review with agents** on a finished run's Fixes page starts a review by hand. The run page shows how many fixes the judges left for you, with the tokens per judge; it shows approved and rejected counts only for runs the agent decided before the retirement. History marks those old agent decisions.

| Variable | Seat | Default endpoint and model |
|---|---|---|
| `BOOKWARDEN_AGENT_ANTHROPIC_KEY` | `haiku` | `https://api.anthropic.com/v1/messages`, `claude-haiku-5-5`, effort `max` |
| `BOOKWARDEN_AGENT_OPENCODE_KEY` | `deepseek` | `https://opencode.ai/zen/go/v1/chat/completions`, `deepseek-v4.1-flash`, effort `max` |
| `BOOKWARDEN_AGENT_GEMINI_KEY` | `gemini` | `https://generativelanguage.googleapis.com/v1beta/openai/chat/completions`, `gemini-3.8-flash`, effort `high` |

`BOOKWARDEN_AGENT_<SEAT>_URL`, `_MODEL`, `_EFFORT`, `_FAMILY` and `_ALLOW_CIDR` override a seat, for example `BOOKWARDEN_AGENT_GEMINI_MODEL` ([ADR-033](docs/decisions/ADR-033-providers.md)). Shadow needs all three keys. Keys are never logged or shown. `BOOKWARDEN_CALIBRE_PASSWORD` keeps its spaces (only a trailing CR or LF is dropped) and a startup warning notes a leading or trailing space; keys are trimmed.

| Variable | Meaning |
|---|---|
| `BOOKWARDEN_AGENT_<SEAT>_URL` | The seat's endpoint. It must be https, unless the seat has `_ALLOW_CIDR`. |
| `BOOKWARDEN_AGENT_<SEAT>_FAMILY` | The model's vendor family: `anthropic`, `deepseek`, `google`, `openai`, `qwen`, `moonshot`, `minimax` or `zhipu`. Default: `anthropic` for haiku, `deepseek` for deepseek, `google` for gemini. Two configured seats can't share a family. Settings shows it. |
| `BOOKWARDEN_AGENT_<SEAT>_ALLOW_CIDR` | Comma-separated exact addresses (`/32` for IPv4, `/128` for IPv6) the seat may dial. With it, the URL may be plain `http://` or a private address, and the seat can reach only those addresses. Without it, private and special-purpose addresses are refused. Only this seat gets the allowance. |

A seat on plain http logs a startup warning, as its key travels unencrypted on the network. A judge that gets HTTP 429, or a 402 or 403 with a JSON error that says quota, credits, billing or rate limit, fails with `quota_exhausted`, and Settings shows when and the retry-after, until the seat answers again or the next restart. For example, the gemini seat on an OpenAI-compatible proxy on the LAN:

```sh
BOOKWARDEN_AGENT_GEMINI_URL=http://llm-proxy.example.lan:8766/v1/chat/completions
BOOKWARDEN_AGENT_GEMINI_MODEL=gemini-3.8-flash-high
BOOKWARDEN_AGENT_GEMINI_ALLOW_CIDR=<proxy-ip>/32
BOOKWARDEN_AGENT_GEMINI_KEY=the-proxy-key
```

`BOOKWARDEN_LIVE_PROXY_URL` (for example `http://llm-proxy.example.lan:8766`) and `BOOKWARDEN_LIVE_PROXY_KEY` make `go test -tags live ./internal/agent` also probe such a proxy: `/v1/models` and one tiny chat completion.

`go test -tags live ./internal/agent` sends one synthetic packet to every seat whose key is set, to check the endpoint and model. `bookwarden agent-review --run N --calibrate` queues a calibration review of run N: every proposal is judged, including ones already decided, and nothing is decided. Its outcomes can be compared with your own decisions.

## Environment reference

Every variable Bookwarden reads. `TestEnvironmentReferenceIsComplete` fails when this table and the code disagree. A bad value stops the start with a message that names the variable and never shows a secret. Per-seat and per-extractor rows stand for each name: `<SEAT>` is `HAIKU`, `DEEPSEEK` or `GEMINI`, and `<EXTRACTOR>` is `LUNA`, `DEEPSEEK` or `GLM`.

| Variable | Default | Meaning |
|---|---|---|
| `BOOKWARDEN_DATABASE_URL` | required | Postgres connection URL (`postgres://user:password@host:5432/db`). Holds a password. |
| `BOOKWARDEN_CALIBRE_URL` | required | Base URL of the Calibre content server, such as `http://calibre:8081`. No credentials, query or fragment. |
| `BOOKWARDEN_CALIBRE_USER` | required | The Calibre account Bookwarden uses. |
| `BOOKWARDEN_CALIBRE_PASSWORD` | required | Its password. Spaces are kept; only a trailing CR or LF is dropped. |
| `BOOKWARDEN_CALIBRE_LIBRARY` | empty | Library id. Empty means the server's default library. |
| `BOOKWARDEN_ALLOWED_HOSTS` | required | Comma-separated host names or IPs, without ports, that the web UI may be opened by. Other `Host` headers are refused. |
| `BOOKWARDEN_TRUSTED_PROXIES` | empty | IPs or CIDRs of a reverse proxy in front. Only from these is `X-Forwarded-For` and `X-Forwarded-Proto` believed. A CIDR may be no wider than /16 (IPv4) or /48 (IPv6); a wider one stops the start. |
| `BOOKWARDEN_LISTEN` | `:8080` | Address the web server listens on, `host:port` or `:port`. The healthcheck reads it too. |
| `BOOKWARDEN_LOG_LEVEL` | `info` | `debug`, `info`, `warn` or `error`. |
| `BOOKWARDEN_STOP_GRACE` | `25s` | How long a stop waits for the book in hand and a write in progress. Keep it at least 5 s under the container's stop timeout. |
| `BOOKWARDEN_METRICS_TOKEN` | empty | Bearer token that turns on `/metrics`, at least 32 characters (a shorter one stops the start). Without it the path is a 404. |
| `BOOKWARDEN_SCHEDULE_INCREMENTAL` | empty | Daily `HH:MM` for an incremental check. Empty is off. |
| `BOOKWARDEN_SCHEDULE_FULL` | empty | Weekday and time for a full check, such as `sun 04:00`. Empty is off. |
| `BOOKWARDEN_RETAIN_RUNS` | `10` | How many of the newest runs to keep. `0` keeps every run. |
| `BOOKWARDEN_HEARTBEAT_URL` | empty | Uptime Kuma push URL, told how each scheduled run ended. Holds a secret token. Empty is off. |
| `BOOKWARDEN_HEARTBEAT_ALLOW_CIDR` | empty | Exact addresses (`/32`, `/128`) the heartbeat may dial. Needed for plain http or a private address. |
| `BOOKWARDEN_GOOGLE_BOOKS_KEY` | empty | Google Books API key. Without it that source is off. |
| `BOOKWARDEN_HARDCOVER_TOKEN` | empty | Hardcover API token. Without it that source is off; with it, Hardcover is recorded and decides nothing. |
| `BOOKWARDEN_EXTRACTOR_KEY` | empty | Key for the extractors' endpoint (opencode-go). Without a key or a gateway, no extractor runs. Through a gateway it is the gateway's virtual key. |
| `BOOKWARDEN_EXTRACTOR_BIFROST_URL` | empty | Base URL of a Bifrost gateway, `http://host:port`, to send the extractors' calls to instead. It holds the provider keys. Calls then carry `x-bf-disable-content-logging: true`. |
| `BOOKWARDEN_EXTRACTOR_ALLOW_CIDR` | empty | Exact addresses the extractors' endpoint may dial. Needed for a gateway on the LAN or plain http. |
| `BOOKWARDEN_EXTRACTOR_<EXTRACTOR>_URL` | the gateway's | Override one extractor's endpoint. Can't be set together with `BOOKWARDEN_EXTRACTOR_BIFROST_URL`. |
| `BOOKWARDEN_EXTRACTOR_<EXTRACTOR>_MODEL` | `gpt-6-luna`, `deepseek-v4.1-flash`, `glm-5.3-flash` | Override one extractor's model id, sent as given. |
| `BOOKWARDEN_EXTRACTOR_<EXTRACTOR>_EFFORT` | `low`, `none`, `low` | Override one extractor's reasoning effort. |
| `BOOKWARDEN_EXTRACTOR_<EXTRACTOR>_FAMILY` | `openai`, `deepseek`, `zhipu` | Override its vendor family. Two extractors can't share one. |
| `BOOKWARDEN_EXTRACTOR_<EXTRACTOR>_ALLOW_CIDR` | empty | Exact addresses this extractor's own endpoint may dial. |
| `BOOKWARDEN_EXTRACTOR_<EXTRACTOR>_MAX_CALLS` | `5000`, `5000`, `15000` | Most calls this extractor makes in one run. |
| `BOOKWARDEN_AGENT_ANTHROPIC_KEY` | empty | Key for the `haiku` seat of the agent panel. |
| `BOOKWARDEN_AGENT_OPENCODE_KEY` | empty | Key for the `deepseek` seat. |
| `BOOKWARDEN_AGENT_GEMINI_KEY` | empty | Key for the `gemini` seat. Shadow review needs all three keys. |
| `BOOKWARDEN_AGENT_<SEAT>_URL` | per seat, see [Agent fixing](#agent-fixing) | The seat's endpoint. It must be https, unless the seat has an allowance. |
| `BOOKWARDEN_AGENT_<SEAT>_MODEL` | per seat | The seat's model id. |
| `BOOKWARDEN_AGENT_<SEAT>_EFFORT` | per seat | The seat's reasoning effort. |
| `BOOKWARDEN_AGENT_<SEAT>_FAMILY` | per seat | The seat's vendor family. Two seats can't share one. |
| `BOOKWARDEN_AGENT_<SEAT>_ALLOW_CIDR` | empty | Exact addresses the seat may dial. Lets its URL be plain http or private. |
| `BOOKWARDEN_AGENT_MAX_TOKENS_PER_RUN` | `20000000` | Token cap over all judges and reviews of one check. |

`TZ` is not a `BOOKWARDEN_` variable but the schedule uses it; see [Scheduled and incremental checks](#scheduled-and-incremental-checks). Families a seat or extractor may name: `anthropic`, `deepseek`, `google`, `openai`, `qwen`, `moonshot`, `minimax`, `zhipu`, `tencent`.

## Operating

**Health.** `GET /healthz` is liveness only: 200 `ok` when Postgres answers. The Docker `HEALTHCHECK` uses it. It needs no login and no allowed host.

**Readiness.** `GET /readyz` answers 200 `{"status":"ok"}` only when all three hold: the database answers `SELECT 1` within 2 seconds, the migrations are at this binary's latest version, and Calibre answered within the last 5 minutes or answers now (each check gets 2 seconds; Calibre is probed at most every 15 seconds, failures included, however often `/readyz` is hit). Otherwise it answers 503 with the failing checks, for example `{"status":"unavailable","failing":["calibre"]}`; the names are `database`, `migrations` and `calibre`. The causes are logged and never sent. Like `/healthz` it needs no login and ignores `BOOKWARDEN_ALLOWED_HOSTS`. For Uptime Kuma (which alerts to Discord), add an HTTP(s) monitor on `http://bookwarden:8080/readyz` that expects status 200; use `/healthz` for a plain "is it alive" monitor.

**Metrics.** Set `BOOKWARDEN_METRICS_TOKEN` to turn on `GET /metrics` (Prometheus text format, no client library); without it the path is a 404. Send `Authorization: Bearer <token>`; the token is compared in constant time. Like the probes it ignores `BOOKWARDEN_ALLOWED_HOSTS`, so a scraper can use the container name. The database-backed metrics are read per scrape, one query per family within 5 seconds; if one fails the scrape answers 500.

```yaml
scrape_configs:
  - job_name: bookwarden
    authorization:
      type: Bearer
      credentials_file: /etc/prometheus/bookwarden-token
    static_configs:
      - targets: ["bookwarden:8080"]
```

| Metric | Type | Meaning |
|---|---|---|
| `bookwarden_build_info{version}` | gauge | Always 1; the version is the label. |
| `bookwarden_runs{status}` | gauge | Stored runs by status (`running`, `waiting`, `completed`, `cancelled`, `failed`). Retention prunes old runs, so it is a gauge, not a counter. |
| `bookwarden_run_duration_seconds{kind}` | gauge | How long the last finished run took; `kind` is `full`, `incremental` or `book`. Absent before the first. |
| `bookwarden_run_books{state}` | gauge | Books of the last finished run by state (`pending`, `checked`, `source_changed`, `failed`). |
| `bookwarden_proposals_open{field}` | gauge | Proposals of the last finished run that nobody has decided, by field. Needs the run's seal check (a run's end, the nightly sweep or `verify-seals`). |
| `bookwarden_fixes_applied_total` | counter | Completed apply operations in the write ledger, which is append-only. |
| `bookwarden_fixes_undone_total` | counter | Completed undo operations. |
| `bookwarden_incidents_open` | gauge | Unacknowledged write incidents; one pauses all writes. Alert on `> 0`. |
| `bookwarden_seal_checks_broken` | gauge | Runs whose latest seal check found damage. Alert on `> 0`. |
| `bookwarden_source_lookups_total{source,outcome}` | counter | Source lookups since the process started. `outcome` is `ok`, `no_match`, `unavailable`, `not_queried` or `not_configured` for a live answer, `cache_hit` when the cache answered, `stale_hit` when an expired cached answer was served, past the refresh budget, after a failed refresh or while the breaker was open ([ADR-044](docs/decisions/ADR-044-source-cache-refresh-budget.md)) and `breaker_open` when the source was skipped for the run. |
| `bookwarden_source_request_seconds{source}` | histogram | How long each live source lookup took, since the process started; cache answers aren't timed. Buckets 0.1 s to 30 s. |
| `bookwarden_agent_calls_total{seat,code}` | counter | Calls to each agent seat and extractor since the process started, one per call whatever its retries. `code` is empty on success, else `timeout`, `quota_exhausted`, `http_4xx`, `http_5xx`, `bad_answer`, `refused_host` or `transport`. |
| `bookwarden_labels{field,kind}` | gauge | Precision labels (blind individual decisions) by field and kind (`fill` or `replace`). |
| `bookwarden_label_misses{field,kind}` | gauge | The labels that were misses. |

**Log level.** `BOOKWARDEN_LOG_LEVEL` is `debug`, `info`, `warn` or `error` (default `info`); anything else stops the start as a configuration error. When a report page fails unexpectedly, the log line `report unavailable` carries the error, the method, the path and the status, never the query or a form value.

**Heartbeat.** With `BOOKWARDEN_HEARTBEAT_URL` set, Bookwarden tells an Uptime Kuma push monitor how every scheduled run ended, so an unattended schedule is noticed when it stops or goes wrong ([ADR-041](docs/decisions/ADR-041-heartbeat.md)).

1. In Kuma, add a monitor of type **Push**. Set its heartbeat interval a little over the schedule's period, for a daily run 26 hours (93600 s), and point its notification at Discord.
2. Copy the monitor's push URL, for example `http://kuma.example.lan:3001/api/push/<token>`, into `BOOKWARDEN_HEARTBEAT_URL`. The token is a secret: the Unraid template masks the field, and the URL is never logged or shown (only `scheme://host` appears, in the start-up configuration line and in a delivery warning).
3. Plain `http`, or a private literal address, needs the exact allowance, as for an agent seat; so does a host name that resolves to a private address, or every push is refused at dial time and only a warning plus Kuma's missed-interval alert follow: `BOOKWARDEN_HEARTBEAT_ALLOW_CIDR=<proxy-ip>/32` (`/32` for IPv4, `/128` for IPv6). With it the sender can dial only those addresses. A URL whose literal host is outside the allowance stops the start.

After each scheduled run that ended (completed, failed or cancelled), once its seal check has returned, Bookwarden sends `GET <url>?status=up|down&msg=<text>&ping=`. It is **down** when the run failed or was cancelled, when the seal check found a broken seal or could not run, or when more than 5 % of the run's extractor entries were not answered (answers from the cache are not counted; an entry sealed `unavailable` counts even when a quota pause or the per-run cap meant nothing was called, as the sealed data can't tell the two apart; a run without extraction has no rate; below 20 calls the rate is reported but can't turn the beat down). Otherwise it is **up**. A run you start by hand, or one that can't be read back, sends nothing, so a manual run never hides a schedule that stopped. The message is short, for example `run 15 completed: 3207 books, 0 broken seals, extractor failures 0.41 % (12/2950 not answered)`, and never holds book text. A push times out after 10 seconds and is tried once more after 5; if that fails too, a warning is logged and Bookwarden moves on, so a stop never waits for it and a Kuma outage never affects a run. A run that never happens sends nothing, and Kuma alerts when its interval passes without a push.

**Stopping.** On SIGTERM bookwarden stops taking work: the check runner starts no new book and the writer no new apply, but the books in hand finish and the writer completes its current write and readback, so no operation is left half applied (ADR-022). The check run stays active and resumes at the next start. All of this is bounded by `BOOKWARDEN_STOP_GRACE` (default `25s`). The writer is never cancelled mid-operation: past the grace the runner's work is cancelled, a writer or undo still running is abandoned, and the log says so (`stop grace expired` with the active run, whether an apply and how many undos were running, then `workers abandoned`). Bookwarden then exits without closing the database pool (`store left open for the process exit`), up to 2 seconds after the grace. An abandoned write is recovered from the ledger at the next start, as after a crash. Docker's default stop timeout is 10 seconds, so give the container at least 5 seconds more than the grace: the Unraid template sets `--stop-timeout 30` in its extra parameters, and with `docker run` or Compose use `--stop-timeout 30` or `stop_grace_period: 30s`. If you raise `BOOKWARDEN_STOP_GRACE`, raise the stop timeout with it.

## Resource limits

The Unraid template runs bookwarden with `--memory 2g` and a 1.5 GiB tmpfs at `/tmp`. Downloaded files stream into that tmpfs, which counts toward the memory limit. At most 1 GiB of files is in flight at once, each file is deleted as soon as it's been read, and the rest is headroom for the process and one OCR tool at a time (at most 768 MiB of address space) with its page render. The image sets `GOMEMLIMIT=1200MiB`, a soft heap limit near 60 % of that 2 GiB, so Go collects harder before the kernel would kill the process; override it in the container's environment if you change the memory limit, and keep it near 60 % of the new one. If the process still dies while checking a book, the next start counts that against the book, and a book that stopped it twice is failed with the reason `process_crashed` ("Bookwarden stopped unexpectedly twice while checking this book") instead of being inspected again, so the run can end. The books that were in hand with it are checked one at a time after a restart, so only the one that kills the process is given up.

## Releases

A release is a pushed `v*` tag on a commit that is on `main`; the workflow refuses any other. The first release of this program is `v2.0.0` because the retired Python version (Certificate A) used tags up to `v1.3.2`. The design documents call the same milestone "v1.0". Release candidates `v2.0.0-rc.1`, `rc.2` and so on precede it; the default tag `v2.0.0` in the quick start exists once the final release is out.

To cut one from a green `main`:

```bash
git switch main && git pull --ff-only
git tag -a v2.0.0 -m "Bookwarden 2.0.0"
git push origin v2.0.0
```

The tag must look like `vMAJOR.MINOR.PATCH`, optionally with a pre-release suffix such as `-rc.1` (those releases are marked as pre-releases). Never rerun a failed release run; fix the cause and push a new tag.

`.gitea/workflows/release.yml` then, in order:

1. Builds the image with the tag as its version and the commit as its revision, and applies the same size budget, Trivy scan (HIGH and CRITICAL) and smoke test as CI.
2. Writes an SBOM (`sbom.spdx.json`, SPDX JSON, made by Syft).
3. Pushes `<forge-registry>/felix/bookwarden:<tag>` (the `FORGE_REGISTRY` repository variable) to the Gitea registry and records its digest in `digests.txt`. It pushes with `crane`, because the runner's Docker daemon doesn't trust that plain-HTTP registry. This step is never skipped, and needs `REGISTRY_USER` and `REGISTRY_TOKEN`: without them the run fails in its first step, before the build.
4. With the GHCR secrets, pushes `ghcr.io/felixapel/bookwarden:<tag>` too.
5. With the cosign secrets, signs every pushed digest and attaches the SBOM to it as a signed `spdxjson` attestation (`scripts/release-sign.sh`, [ADR-042](docs/decisions/ADR-042-release-signing.md)).
6. Writes the changelog for the tag and creates the Gitea release with it as a draft, attaches the SBOM, `digests.txt` and `cosign.pub`, and then publishes it. The changelog (`cliff.toml`) lists the commits since the previous tag, grouped by Conventional Commit type; for a final tag, "previous" means the previous final tag, so release candidates in between are included.

The digests are printed at the end of the run.

**After the first push to GHCR,** make the package public (on GitHub: Packages, `bookwarden`, Package settings, Change visibility). GHCR creates a new package as private, and `docker pull` then fails for everyone else.

**Dry run.** Starting the workflow by hand (Actions, release, Run workflow) is always a dry run, whatever its `dry_run` input says; a tag push never is. It runs every step above under the version `v0.0.0-dryrun.<run id>`: the build, size budget, Trivy, smoke test and SBOM, the push to the Gitea registry as `dryrun-<run id>`, the signature and attestation (with the cosign secrets), and the changelog for the last tag up to `HEAD`. GHCR is skipped. The Gitea release is created as a draft, gets its assets and is deleted again, and a last step deletes the `dryrun-<run id>` image tag and the `sha256-<digest>` tag cosign keeps the signature and attestation under (a failure there is only a warning; the draft is deleted even when an upload fails). Run one before the first tag of a release, so a pipeline bug doesn't cost a tag.

**Signing rehearsal.** `scripts/release-rehearsal.sh [image]` proves `release-sign.sh` and the three verify commands below on this machine: it starts a registry on 127.0.0.1, pushes the image (default `bookwarden:dev`), makes a throwaway key pair, signs and attests the digest with the cosign that `release.yml` pins, and runs the README's commands as written against it. It ends with `rehearsal: OK, all 3 verify commands passed`. It is not part of CI.

Repository variables (Settings, Actions, Variables): `FORGE_REGISTRY` is the forge's registry as `host:port`, and `FORGE_API` its API base URL, such as `http://<host>:<port>/api/v1`. `release.yml` fails in its first step without either, and `renovate.yml` without `FORGE_API` once `RENOVATE_TOKEN` is set.

Repository secrets (Settings, Actions, Secrets); `REGISTRY_USER` and `REGISTRY_TOKEN` are required, and a step whose other secrets are missing is skipped with a notice in the run log:

| Secret | Enables |
| --- | --- |
| `REGISTRY_USER`, `REGISTRY_TOKEN` | Required. Gitea registry push, release creation, and the dry run's clean-up. The token needs `write:package` and `write:repository`. |
| `GHCR_USER`, `GHCR_TOKEN` | The push to GHCR. The token needs `write:packages`. |
| `COSIGN_KEY`, `COSIGN_PASSWORD` | Signing and the SBOM attestation. `COSIGN_KEY` is the content of the `cosign.key` file made by `cosign generate-key-pair`, the private half of the `cosign.pub` committed in the repository root, which every release attaches. |
| `RENOVATE_TOKEN` | The Renovate workflow. |
| `RENOVATE_GITHUB_COM_TOKEN` (optional) | A read-only GitHub token without scopes. Renovate needs it to look up GitHub-hosted actions and the Go pin in `mise.toml`, so those are updated only once it exists. |

Signatures are not uploaded to a transparency log (ADR-042), so verification needs `--insecure-ignore-tlog`. Take `cosign.pub` from the release's assets (or the repository root at the tag). The key signs the digest, so verify by digest, not by tag:

```bash
cosign verify --key cosign.pub --insecure-ignore-tlog ghcr.io/felixapel/bookwarden@sha256:<digest>
cosign verify-attestation --key cosign.pub --type spdxjson --insecure-ignore-tlog ghcr.io/felixapel/bookwarden@sha256:<digest>
# the Gitea registry is plain HTTP:
cosign verify --key cosign.pub --insecure-ignore-tlog --allow-http-registry <forge-registry>/felix/bookwarden@sha256:<digest>
```

Without a transparency log, a signature does not prove when it was made: whoever holds a leaked key can sign a new image that looks like an old release.

**Public mirror.** The GitHub repository has one commit per release and no other history ([ADR-047](docs/decisions/ADR-047-public-mirror-snapshots.md)). Once the release run is green, on a clean checkout of `main` with the tag:

```bash
BW_PUBLIC_NAME='<public name>' BW_PUBLIC_EMAIL='<id>+<user>@users.noreply.github.com' scripts/mirror-export.sh v2.0.0
```

It exports the tag's tree, stops on any `scripts/leak-check.sh` hit, commits the tree to the local branch `public` with the tag's release notes, tags it `refs/public-tags/<tag>`, and prints the `git push` command. It never pushes, and it needs `git-cliff` on `PATH`.

**Pinned tools.** `crane`, `cosign` and `git-cliff` are release binaries pinned by SHA-256 in `release.yml`, and Docker Compose, for the quick start test, in `ci.yml`; Renovate cannot recompute a hash, so bump the version and the hash together. Actions and container images (Trivy and Syft too, as `name:tag@sha256`) are pinned by commit or digest.

**Dependency updates.** `renovate.json` configures Renovate for the Go modules (minor and patch updates in one PR), the Dockerfile's pinned images, the pinned actions and images in the workflows, the `name:tag@sha256` images in `scripts/` and `deploy/compose/`, and the Go pin in `mise.toml`. The `go` directive in `go.mod`, the Go pin in `mise.toml` and the `golang` image in the Dockerfile are one grouped PR; the `mise.toml` pin is looked up on GitHub, so without `RENOVATE_GITHUB_COM_TOKEN` it stays behind and needs bumping by hand. `golangci-lint` and `gitleaks` in `mise.toml` are not updated by Renovate: CI installs them from SHA-256-pinned downloads in `ci.yml`, so bump both files together. `.gitea/workflows/renovate.yml` runs every Monday and on demand. It needs the `RENOVATE_TOKEN` secret, a Gitea token of the account that opens the PRs, with `read:user`, `write:repository` and `write:issue`; without it the run is skipped. The schedule in `renovate.json` (Monday before 6 am) also applies to a manual run, so one started at another time opens no new PRs.

## Third-party

[htmx](https://htmx.org) 2.0.11 (0BSD) is vendored in `internal/web/static/htmx.min.js`; `TestHtmxIsVendored` pins its SHA-256. [THIRD_PARTY.md](THIRD_PARTY.md) lists everything the binary and the image include, with licences: the Go modules, Tesseract, Poppler, the tessdata models and the Alpine base. Contributions: [CONTRIBUTING.md](CONTRIBUTING.md).

## License

GPL-3.0-or-later.

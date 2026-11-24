# Operator guide

How to install, run and look after Bookwarden. The [README](../README.md) has the reference for every setting (see its [environment reference](../README.md#environment-reference)); this guide is the order to do things in.

## Install

You need:
- a Calibre content server (`calibre-server`) with an account for Bookwarden. Keep the account read-only until you want fixes;
- Postgres 18, empty database and role included;
- a host that can reach both.

Bookwarden is one container. Its design reaches the library only through the content server, so it needs no access to the library's files.

### Docker Compose

`deploy/compose/docker-compose.yml` runs Bookwarden and Postgres 18 together. Postgres is on an internal network and isn't published.

```bash
cd deploy/compose
cp .env.example .env
$EDITOR .env
docker compose config -q && docker compose up -d
```

The file forwards the settings most installs need. For anything else in the [environment reference](../README.md#environment-reference), add it under `environment:`.

Set at least `POSTGRES_PASSWORD`, `BOOKWARDEN_CALIBRE_URL`, `BOOKWARDEN_CALIBRE_USER`, `BOOKWARDEN_CALIBRE_PASSWORD` and `BOOKWARDEN_ALLOWED_HOSTS` (the names you open the UI by). The password goes into a URL, so use letters and digits. Everything else has a default. The image tag is `BOOKWARDEN_VERSION`; pin a release.

The container has a read-only root file system, a 1.5 GiB tmpfs for downloaded files, no capabilities, `no-new-privileges`, a 2 GiB memory limit and 256 processes, as on Unraid. If a check fails with out-of-memory errors, raise `mem_limit` and the tmpfs size together.

### Unraid

1. Create the role and database in your Postgres 18 container, for example `bookwarden` (the [README](../README.md#quick-start) has the commands).
2. Copy `deploy/unraid/bookwarden.xml` to `/boot/config/plugins/dockerMan/templates-user/my-bookwarden.xml`.
3. In the Docker tab, add the container from that template. It starts on `bridge`; if Calibre and Postgres are on custom Docker networks, add `--network <name>` for each at the end of Extra Parameters (Unraid then ignores Network Type). Fill in the database URL (`postgres://bookwarden:PASSWORD@HOST:5432/bookwarden`), the Calibre URL, user and password, and the allowed hosts. The extra parameters also set the same hardening as above.
4. Apply.

The template pins a release tag (`ghcr.io/felixapel/bookwarden:v2.0.0`), never `:latest`. Change the tag to upgrade.

Later upgrades use [`scripts/deploy-unraid.sh`](#upgrades).

## First run

1. Read the log: `docker compose logs bookwarden` or the Unraid log icon. It shows a line with `setup_code`.
2. Open the web UI and enter the code. Choose the admin name and a password of at least 12 characters. If the container restarts before you finish, it prints a new code.
3. Open **Settings**. Check the status of the metadata sources.
4. Open **Runs** and start a full check. A library of 3,200 books takes about 80 minutes. The run page shows progress.

Writes are off. Nothing changes in Calibre until step 3 of the next section.

## The review-first workflow

Bookwarden checks, proposes and waits. It never writes a fix you didn't approve, and no setting changes that.

1. **The inbox** is the **Fixes** page of a finished run. Each row is one proposed change to one field of one book, with the current and proposed value, the evidence and its tier. Approve or reject each row with its own buttons. Use the filters and the keyboard (`j`, `k`, `a`, `r`) to work through it. "Approve all shown", ticked batches and "Approve all open fixes" (every undecided fix of the run on every page, except muted types and values Calibre can't store exactly) work too, but only a decision on a single row counts as a label (ADR-034). A fix you approved or rejected that the next check proposes again unchanged keeps your decision there, marked "carried from run N".
2. **Muting.** A fix type (field, evidence path, fill or replace) that you keep rejecting is muted: its fixes leave the default view and the page says how many it hid. They stay as findings and under "Muted types only", where you can still decide them. Muting only hides.
3. **Apply.** Turn on **Allow changes to Calibre** in Settings, once, and give Calibre's account write permission. Then press **Apply approved fixes**. It writes one book at a time. Before each book it reads the book again, and a book that changed since the check is skipped as stale. After each write it reads the book back, and a difference puts the old values back and opens an incident.
4. **History** lists every operation with each value before, as written and after.
5. **Undo** is a button on an operation's page. It writes the old values back while the book is unchanged since.
6. **Incidents** lists writes that didn't complete. Writes pause while one is open. Look at the book in Calibre, then acknowledge it. If Calibre couldn't be reached while the old values were being put back, the incident says so and can't be acknowledged yet: Bookwarden retries the restore every minute while writes are on (**Retry now** queues an attempt at once), and once the old values are back the incident can be acknowledged. With writes off, restoring waits until they are on again.
7. **`/precision`** (Precision, linked from Fixes) shows every fix type with its decided count, misses, the one-sided 95 % bound on precision and its mute state. An unmute button sits next to each muted type. Look at it after a batch of decisions: a type with poor precision needs a rule fix, not more rejecting.

The agent panel is a second opinion shown after you decide, and it is off by default. See [Agent fixing](../README.md#agent-fixing) and the [privacy notes](threat-model.md#privacy-which-text-leaves-the-machine) before turning it on.

## Schedule

Both schedules are off by default. Set them with variables; Settings shows them but doesn't change them.

| Variable | Meaning |
|---|---|
| `BOOKWARDEN_SCHEDULE_INCREMENTAL` | Daily `HH:MM`. Reuses unchanged books' file evidence. |
| `BOOKWARDEN_SCHEDULE_FULL` | Weekday and time, `sun 04:00`. Replaces that day's incremental check. |
| `TZ` | The zone both times are read in, such as `Europe/Berlin`. Unset is UTC. |
| `BOOKWARDEN_RETAIN_RUNS` | How many newest runs to keep. Default `10`; `0` keeps all. |

A good start is an incremental check at night and a full one weekly. A slot is skipped, and logged, when a check, a write, an undo or a review is running or Calibre isn't ready. If the database is away at slot time, the slot is tried again for 15 minutes before it is dropped. A slot missed in the last 12 hours that no scheduled run served starts once after a restart. The rules for reusing evidence and for retention are in the README's [Scheduled and incremental checks](../README.md#scheduled-and-incremental-checks).

### Burn-in and the run ledger

Retention deletes old runs, but every run that ended keeps one row in the run ledger: its kind, status, start, duration, books, re-used books and the broken seals its last seal check found. The Runs page reads the ledger for three numbers, each against its v1.0 target: scheduled runs in a row with no broken seal (14), the 95th percentile of the daily run's duration over the last 30 days (10 minutes), and the longest scheduled full run in the last 30 days, finished or not (2 hours). A scheduled run that failed, was cancelled or never got a seal check ends the streak; the newest run's check may still be running, so it is shown as pending instead. Runs started by hand don't count either way. A skipped slot leaves no run, so it doesn't break the streak: missed runs are the heartbeat's job, and the daily-run count beside the 95th percentile shows gaps. If the ledger can't be read, the panel is hidden and a warning is logged.

### Where a run's time went

Each run's page has a "Where the time went" table: the time spent reading Calibre records, downloading and inspecting files, on OCR and text layers, on catalogue lookups, on model extraction and storing results; the lookups by source and outcome; and the extractor calls, cached answers and failed calls. Each stage's time is summed over the workers, so the stages can add up to more than the run took. A run that was interrupted and resumed adds each attempt's numbers; an attempt that ends while the database is away loses its own. Waiting for the download budget counts as downloading, re-reading a book's earlier evidence counts in no stage, and an extractor call cut off by a stop counts as failed. The ledger row keeps a copy when the run ends, so the numbers outlive retention. When a run ends, the log line `run summary` carries the run id, kind, status, duration, books, re-inspected books and the same numbers (`calibre_seconds`, `lookups_dnb_ok`, `extractor_calls` and so on), never book data.

## Heartbeat

With `BOOKWARDEN_HEARTBEAT_URL` set, Bookwarden tells an Uptime Kuma push monitor how every scheduled run ended ([ADR-041](decisions/ADR-041-heartbeat.md)). A run you start by hand sends nothing, so it can't hide a schedule that stopped.

1. In Kuma, add a monitor of type **Push**. Set its interval a little over the schedule's period (26 hours for a daily run) and point its notification where you want alerts.
2. Put the monitor's push URL into `BOOKWARDEN_HEARTBEAT_URL`. The token in it is a secret: it is never logged or shown.
3. For plain `http`, a private address, or a host name that resolves to a private address, set `BOOKWARDEN_HEARTBEAT_ALLOW_CIDR` to the exact address, such as `<kuma-ip>/32`.

Each ended scheduled run reports **up**, or **down** when the run failed or was cancelled, when a seal is broken or couldn't be checked, or when more than 5 % of extractor calls got no answer (counted from 20 calls; below that the rate is only reported). A run that never ends is caught by Kuma's own missed-interval alert. For a monitor that only asks "is it alive", add an HTTP monitor on `/healthz`; `/readyz` also checks the migrations and Calibre.

### The nightly CI signal

The repository's nightly workflow fuzzes every parser and scans `main` (govulncheck, and Trivy on a fresh image) at 02:17 in the runner's time zone. When both pass, it pushes a beat to a second Kuma push monitor, so a red or missing nightly is noticed. In Kuma, add a **Push** monitor with an interval of 26 hours, and put its push URL into the Gitea repository secret `KUMA_NIGHTLY_URL`. Without the secret the nightly only prints a notice. The URL is never printed.

## Backups and the restore drill

**The nightly dump is the host's job**, not Bookwarden's. Run `pg_dump` on the Postgres container every night, and keep dumps off the machine too. On Unraid, a user script on the Postgres container does it; with Compose, use a cron job on the host:

```bash
docker compose exec -T postgres pg_dump -U bookwarden -Fc bookwarden > "bookwarden-$(date +%F).dump"
```

The dump holds the sealed evidence, the decisions, the write ledger and the book excerpts. Treat it like the library (see the [threat model](threat-model.md)). A deploy also takes its own dump (see below).

A backup you haven't restored is a guess. **The restore drill**, `scripts/restore-drill.sh`, restores the newest nightly dump into a scratch database and runs `bookwarden verify-seals` against it. It never touches the live database, and it removes the scratch database again, also on failure. It fails on a dump that restores to fewer than `BW_MIN_RUNS` runs, because `verify-seals` passes on an empty database. Run it **weekly**, and after any change to the backup. Give it `BW_KUMA_URL`, the URL of an Uptime Kuma push monitor with a heartbeat interval of 8 days: a drill that fails or stops running is then noticed.

Copy it to the Unraid host and run it there as root. It takes no arguments, only environment variables:

```bash
export BW_PG_USER=<postgres superuser>
./restore-drill.sh
```

It finds the newest dump, refuses one that is too old or still being written, creates the scratch database, restores with `pg_restore --single-transaction`, runs `verify-seals` against it in a read-only container from the running image, prints row counts and drops the scratch database. Run it away from the nightly dump window.

| Variable | Default | Meaning |
|---|---|---|
| `BW_PG_USER` | required | The Postgres superuser that restores. |
| `BW_DUMP_ROOT` | required, unless `BW_DUMP` is set | The directory with the nightly dump directories, laid out as `$BW_DUMP_ROOT/postgresql18_YYYYMMDD-HHMM/bookwarden.dump`. On the maintainer's server, a nightly User Script writes this layout; Unraid itself makes no database dumps. |
| `BW_DUMP` | newest dump under `BW_DUMP_ROOT` | A dump file to use instead. |
| `BW_PG_CONTAINER` | `postgresql18` | The Postgres container. |
| `BW_DB` | `bookwarden` | The live database the dump is of. |
| `BW_DRILL_DB` | `bookwarden_drill` | The scratch database. It must not be `BW_DB`, and must not already exist. |
| `BW_MAX_AGE_HOURS` | `36` | Refuse a dump older than this. |
| `BW_MIN_AGE_MINUTES` | `15` | Refuse a dump younger than this, as it may still be written. |
| `BW_CONTAINER` | `bookwarden` | The running Bookwarden container, for its image and database URL. |
| `BW_NETWORK` | `db-net` | The network that reaches Postgres. |
| `BW_CHANGELOG` | none | A file that gets one line appended, with the result, the dump, its age in hours and the row counts. |
| `BW_MIN_RUNS` | `1` | Fail when the restored database has fewer runs: an empty or wrong dump. |
| `BW_KUMA_URL` | none | An Uptime Kuma push URL, called after a pass (`curl -fsS -m 10`, the URL passed on stdin). It holds a token and is never printed or put in a command line; a failed push is only a warning. |

Exit codes: `0` restored and every run verified; `1` the restore or the verification failed, or the database has fewer than `BW_MIN_RUNS` runs; `2` usage error; `3` preflight refused (no dump, a stale or growing dump, or the scratch database exists). A `3` for a stale dump means the nightly dump has stopped: fix that first.

**With Compose,** the nightly dump is yours to schedule, and the script takes the file directly. The names below are what the example compose file produces (check them with `docker compose ps` and `docker network ls`):

```bash
BW_PG_USER=bookwarden \
BW_DUMP=/path/to/bookwarden-2026-11-08.dump \
BW_PG_CONTAINER=bookwarden-postgres-1 \
BW_CONTAINER=bookwarden-bookwarden-1 \
BW_NETWORK=bookwarden_db \
./restore-drill.sh
```

The dump command above writes exactly such a file. It must be at least `BW_MIN_AGE_MINUTES` old, and newer than `BW_MAX_AGE_HOURS`.

To really restore, stop Bookwarden, restore into a fresh database, then start it. The commands are in the README's [Deploying on Unraid](../README.md#deploying-on-unraid) section. `pg_restore --clean` doesn't drop tables a newer migration added, so don't use it over the old database.

## Upgrades

On Unraid, upgrade with `scripts/deploy-unraid.sh`. It deploys an image pinned by digest and can roll back.

```bash
export BW_PG_USER=<postgres role>
./deploy-unraid.sh registry.example/owner/bookwarden@sha256:<digest> <short-sha> "what changed"
```

Copy it to the host (for example `/boot/config/bookwarden/`), and run it as root inside tmux or screen.

- **Dry run.** `DRY_RUN=1` prints every action that would change something, and runs none.
- **Digest pin.** The image must be `repo@sha256:<64 hex>`. The label is at least 7 characters (the short commit sha; for a release image, the sha of the tagged commit), and `bookwarden version`, which prints the version and the first 12 characters of the build's commit, must print it: once from the pulled image before anything changes (exit 3 if not), and again from the new container. The template's `<Repository>` is pinned to the digest.
- **Preflight** stops the deploy with exit 3 when the pulled image's `version` lacks the label, the database can't be queried, a run is `running` or `waiting`, a write is in progress, the template doesn't match the running image, or a dump or backup file already exists.
- **Dump.** After it stops the old container, it takes `pg_dump -Fc` to `$BW_DUMP_DIR/pre-<label>-<timestamp>.dump` and checks it by reading every block. A failed dump starts the old container again and exits 1.
- **Retention.** After a successful deploy it keeps the newest `BW_KEEP_BACKUPS` (default 10; `0` keeps all) of its own `pre-<label>-<timestamp>` dumps and template backups, and removes the older ones. Files it didn't name that way are never touched, and template backups are counted per template. Dumps are counted per directory, so give each Bookwarden deployment its own `BW_DUMP_DIR`. A failed deploy removes nothing; a dry run prints what it would keep.
- **Rollback.** If the new container isn't healthy, runs another image or prints another version, the script restores the template, removes the new container and starts the old one (exit 1). If that doesn't work, exit 5.
- **Exit 4** means the new version may have migrated the database, so the old binary can't start on it. The script stops the new container, puts the template back from its backup, does not start the old container and does not touch the database, and prints the restore commands. Restore the pre-deploy dump into a fresh database.
- **Exit 0** deployed. **Exit 2** is a usage error.

The script, its settings and a manual rollback are described in the README's [Deploying on Unraid](../README.md#deploying-on-unraid). With Compose, upgrading is: take a dump, change `BOOKWARDEN_VERSION`, run `docker compose pull && docker compose up -d`. Migrations run at start; an older binary refuses a newer schema and names both versions.

Releases are signed. To verify one, see [Releases](../README.md#releases).

## Troubleshooting

| What you see | What it means |
|---|---|
| The container is `unhealthy`, or `/healthz` fails | Postgres doesn't answer. Check the database URL and the Postgres container. |
| `/readyz` answers 503 with `{"status":"unavailable","failing":["calibre"]}` | Calibre didn't answer in the last 5 minutes. Check `BOOKWARDEN_CALIBRE_URL`, the account and the network. Names `database` and `migrations` work the same way. The cause is in the log. |
| The start stops with a message naming a variable | A setting is wrong or missing. It lists every problem at once and never shows a secret. |
| An apply stopped with "Calibre refused the write" | The Calibre account bookwarden uses has no write access. Nothing was written and writes aren't paused. Give the account write permission in Calibre's user settings, then check the books again: the refused fixes failed, the others are still approved. |
| "This host name is not in BOOKWARDEN_ALLOWED_HOSTS" (HTTP 421) | You opened the UI by a name that isn't in the list. Add it, without a port. |
| `waiting for the migration lock` in the log | Another instance is migrating. It gives up after 2 minutes and names the holder. |
| The old version refuses to start: schema is newer | A newer version migrated the database. Run the newer version, or restore the dump from before the upgrade. |
| Forgot the admin password | Delete the account and restart: `DELETE FROM users;` (see the README). The log then shows a new setup code. |
| Fixes show as stale | The book changed in Calibre after the check. Run a new check of it (the book page has Recheck) and decide again. "A newer check carried this approval" needs nothing: the approval moved to the newer run and is applied from there. |
| An incident is open and writes are paused | A write didn't read back. Open the book in Calibre, confirm it, then acknowledge it on Incidents. |
| An incident says Calibre could not be reached while restoring | The restore is retried every minute while writes are on; **Retry now** queues an attempt at once. Once Calibre answers and the old values are back, acknowledge it. |
| A write is refused by Calibre | The account is read-only. Give it write permission, or leave writes off. |
| The schedule didn't run | Look for `schedule: skipping the slot` in the log. Another job was running, or Calibre wasn't ready. A skipped slot isn't retried; one whose start failed (the database away) is retried for 15 minutes, then logged as `schedule: slot dropped after failing for 15 minutes`. |
| The heartbeat never arrives | Plain http or a private address needs `BOOKWARDEN_HEARTBEAT_ALLOW_CIDR`. The log has a delivery warning with `scheme://host` only. |
| A book failed with `store_error` | Bookwarden could not store this book's result. The book failed alone and the run went on; the log line `a book's result could not be stored` names the run and the book. If Postgres itself is away, the run waits and resumes instead, retrying at 5 s rising to 60 s. |
| A book failed with `process_crashed` | Bookwarden stopped unexpectedly twice while checking this book (an out-of-memory kill, most likely). The run went on without it; the books that were in hand with it were checked one at a time. Check the container's memory limit and `GOMEMLIMIT` (about 60 % of the limit). |
| Checks end with out-of-memory kills | Downloaded files stream into the tmpfs, which counts toward the memory limit. Raise both. |
| Extractor calls fail with `quota_exhausted` | The upstream quota is out. The run goes on without them, and the heartbeat of a scheduled run goes down above 5 % failures. |

Logs are JSON on stdout. Set `BOOKWARDEN_LOG_LEVEL=debug` for more, and put it back. Logs never hold excerpts, model answers, keys or the heartbeat URL.

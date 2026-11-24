# ADR-039: Scheduled and incremental runs, and retention

- Status: Accepted
- Date: 2026-11-01
- Amends: [ADR-017](ADR-017-sealed-findings-and-reports.md) (what a run re-reads), [ADR-032](ADR-032-front-matter-evidence.md) (excerpts are now deleted when nothing refers to them) and the go-rewrite spec section 18 (no scheduled runs) and section 6 (retention)

## Context

A full run downloads and inspects every format of every book: about 80 minutes for
3,200 books. Most books don't change between two runs. The spec made runs manual
and the last 10 kept; the v2 design (M7) wants a daily run that doesn't redo the
work, a weekly run that does, and retention that also frees the excerpt text, which
nothing deleted so far. `PruneRuns` ran after each run with a fixed 10.

## Decision

### 1. Incremental runs reuse file evidence

A run whose scope is `Incremental` reuses a book's **file evidence** from that
book's newest sealed package: each format's size, hash, state, hints, excerpt
reference and metadata, and the cover measures. It reuses them only when all of
these hold:

- the Calibre snapshot is the one sealed in that package. `evidence.Snapshot`
  covers uuid, last_modified, identifiers, and each format's name, size and mtime;
- the package is the current schema version, of the same library, book id and uuid;
- the **inspector key** stored with the package equals the running build's;
- the book's languages are the ones sealed (they steer which OCR models are used);
- the evidence was measured at most 8 days ago (`measured_at`, see "Known gaps");
- the formats and cover Calibre lists now are the ones sealed: the same names, the
  same sizes, the same "missing on disk" flags, and the same `has_cover`;
- no result in it depends on timing instead of the file: a timeout, a size
  mismatch, an inspector failure, ISBN hints that timed out, failed or were
  unavailable, a format the export said wasn't in Calibre, a cover that timed out;
- every excerpt it refers to is present and hashes back to what the package seals.

Anything else is a full check of that book, in the same run. Reuse is an
optimisation and never an error: a prior package that doesn't open, or a store that
fails to answer, means a full check.

**What is recomputed.** Source lookups run again through the normal path (the
source cache makes them cheap), the record is read fresh, and findings and the
decision are derived at the current versions. The package is sealed anew in the
new run, with the lookups of that run. Excerpt bodies come back from the store with
the prior package (read and verified there), travel in the `Outcome` exactly as
after a full check, and are written again by `FinishBook`; a body can't be pruned
between the check and the write.

**Determinism.** Given the same files, the same Calibre record and the same source
answers, the reused package is byte-identical to the one a full check seals, seal
included. The package has no run id, no timestamp and no "reused" marker. The test
`TestIncrementalPackagesAreByteIdenticalToFullOnes` runs both on one library
through Postgres and compares the canonical bytes and the seals. What differs lies
outside the seal: `run_books.reused` (true), `evidence.inspect_version` (same),
`created_at` and the run id the rows belong to.

**The inspector key (ruling).** `inspect.Version` is a constant of the code. The
**key** is the text `inspect=<Version>;file=…;entries=…;decompressed=…;ratio=…;pixels=…;hints=<time>;ocr=<bool>;build=<stamp>;tools=<versions>`:
the version, the limits a run was given and whether a hinter is configured, plus
what the constant can't see: the **build stamp** (the Go toolchain version and the
module versions of `golang.org/x/image` and `golang.org/x/text`, read with
`debug.ReadBuildInfo`) and the **tool versions** (`pdftotext -v`, `tesseract
--version` and the tessdata commit that the Dockerfile writes to a `COMMIT` file),
probed once at startup and passed in. A new image with a new decoder, toolchain or
tesseract therefore never reuses what the old one measured. The key is stored in the
column `evidence.inspect_version` and sealed nowhere. A package from before this
change has `''` and is never reused, so the first incremental run after the upgrade
is a full one.

- **Why a column on the package, not run metadata.** The reusable package of a book
  can come from any earlier run, and an upgrade can happen between two runs. The
  key has to travel with the package it describes.
- **Why not sealed.** A sealed key would change the bytes of every package and
  break byte-identity.
- **Bump `inspect.Version`** whenever a change could make a fresh check of an
  unchanged file seal different file evidence: the scan window of #96 was one, a new
  detector or a fixed extractor is another. Changes to OCR (package `ocr`) and to how
  `verify` assembles evidence count. Refactors don't. Findings and decision versions
  don't, because they are recomputed on every run.
- **A golden test backs the rule.** `internal/inspect/version_test.go` hashes the
  non-test sources of `internal/inspect/...`, `internal/ocr`, the ISBN scanner files
  of `internal/findings` and the file-evidence part of `internal/verify` (its own
  file, `fileevidence.go`) and pins the hash next to `Version`. When a hashed file
  changes, the test fails and tells the developer to decide on a bump, then update the
  pin. It can't decide for them; it makes the decision unavoidable.
- **Cost if the rule is wrong.** A forgotten bump leaves unchanged books with file
  evidence from the old behaviour until the next full run, which the weekly schedule
  provides (and which is why incremental runs need one, below). A needless bump costs
  one slow run.

**Known gaps.** The snapshot has no cover mtime, no languages and no `Missing`
flag. Languages and cover edits change `last_modified` in Calibre; the flag, the
languages and `has_cover` are checked separately above. A listed cover that now
reads as missing is a full check of that book (there is no signal when the file
returns).

*A file changed in place* (bit rot, `cp -p` that keeps size and mtime, a cover file
replaced behind Calibre's back) is not visible to any cheap check. This is bounded
rather than closed: each package records `evidence.measured_at`, the time the file
evidence was measured. It is outside the seal, a reuse carries it forward unchanged
(a reuse never renews it), and evidence older than `verify.ReuseMaxAge` (8 days) is
never reused, so at most one weekly full run (or an 8-day-old incremental chain)
passes before the book is read again. A missing, zero or future `measured_at` is
never reused.

**Recording.** `runs.incremental` says the run may reuse, `run_books.reused` says
the book did. The run page shows "N of M books reused their file evidence".

### 2. The scheduler

Off by default, configured from the environment, in the container's time zone
(`TZ`):

- `BOOKWARDEN_SCHEDULE_INCREMENTAL`: a daily local time `HH:MM`, or empty.
- `BOOKWARDEN_SCHEDULE_FULL`: a weekday and a time, `sun 04:00`, or empty.

Parsing is strict (24-hour `HH:MM`, lower-case three-letter weekday, one space). An
invalid value, or a `TZ` the image can't resolve while a schedule is on, stops the
start with a message that names the variable. The binary embeds the time zone
database, because the image has none.

- **Full wins.** On the weekday of the full run there is no incremental slot at all,
  whatever its time: a full run at 04:00 and an incremental one at 02:00 would
  otherwise start the short one, and the full one would find a run active.
- **One slot per local day, through DST.** A time that happens twice (clocks going
  back) is the first one. A time that doesn't happen (clocks going forward) is read
  with the offset the day started with, so 02:30 on the day Berlin jumps from 02:00
  to 03:00 is 03:30. There is no double run and no skipped day.
- **A slot that can't start is dropped, not retried.** A run is active, writes are in
  progress, an undo is running, an agent review is running, Calibre isn't ready, or
  the runner is stopping: the slot is logged (`schedule: skipping the slot`, with the
  reason) and Settings shows the last one. Nothing catches up later, so there is no
  storm. A queued review doesn't count, because one that waits for a judge could hold
  the schedule back for ever.
- **After a restart (ruling).** The scheduler looks back 12 hours when it starts; the
  latest slot in that window that no scheduled run served is started once. Older slots
  are dropped. A scheduled run records the slot it was started for
  (`runs.scheduled_for`), so a restart during a run, or a run that was started and
  finished, doesn't start it again. A full run covers an incremental slot but not the
  other way round, and **any** whole-library full run, a manual one too, started at or
  after a slot serves it (`RunRepo.LastFullRun`) Cost if wrong: with a longer window an hours-late full run
  could land in the middle of Felix's day; with none, a restart over 04:00 loses the
  day. A slot dropped because a run was active is not saved for later in this
  process, but it counts as missed if the process restarts within the window.
- **An incremental schedule needs a full one.** Without the weekly full run nothing
  would ever bound the in-place gap above, so `BOOKWARDEN_SCHEDULE_INCREMENTAL` without
  `BOOKWARDEN_SCHEDULE_FULL` stops the start with a message that names both.
- **Days start at local midnight.** The offset a day started with comes from
  `time.Date(y, m, d-1, 23, 59, …)`, not from noon, so zones that jump at midnight
  (Asia/Beirut, 2026-03-29 00:30) still get one run per day.
- **Scheduled runs never start an agent review (ruling).** Nobody is there to look
  at what the panel approves, and bookwarden stays out of unattended writing. A run
  with `ScheduledFor` set queues no automatic review; a person can start one by hand
  from the Fixes page. A run that can't be read is not reviewed either (fail closed).
- **Settings** shows "Last scheduled check" in the configured zone and names it, not
  "Local".
- **Lifecycle.** The scheduler is a `lifecycle.Go` worker on the stop context that a
  drain cancels (ADR-022); the retention sweep is the same.

### 3. Retention

`BOOKWARDEN_RETAIN_RUNS` (default 10; 0 keeps everything) is applied after each run
ends and once a day, by `RunRepo.Prune`, which is `PruneRuns` plus the excerpt sweep,
in one transaction. The runner no longer prunes; its `Finished` hook is the
caller's point. What is kept, in addition to the newest N runs:

- runs that a decided, non-carried proposal or an operation points to
  (ADR-034, #93; unchanged), and runs that are active;
- **pinned runs** (`runs.pinned`, below), always;
- **the newest completed full-library run and every run newer than it** (for N > 0).
  A string of failed or cancelled runs can't push out the last good one, and a run
  that is newer than it is the evidence of why it failed. One-book runs don't count,
  so ten of them can't push out the last full run. With no completed full-library
  run, nothing is pruned.

`Prune(0)` and `PruneRuns(0)` keep everything: the exported API has no way to wipe
all runs. Tests that need the wipe use an unexported helper.

**Pinning (ruling).** Scheduled runs shorten the effective history: with a daily
incremental run and `BOOKWARDEN_RETAIN_RUNS=10`, the oldest run kept is about ten
days old, not ten manual runs old. The baseline of a rule-change measurement
(`bookwarden eval`) would be pruned within days. A run page has **Keep this run** and
**Unpin**, an admin-only POST with the CSRF token; a pinned run is never pruned, and
the Runs list marks it. Pin the baseline before scheduling.

**Excerpts (ruling): a link table.** A package refers to its excerpts only inside
jsonb. Finding the unreferenced ones by extracting the hashes from every package
would read every stored package on each prune. `evidence_excerpts(run_id, book_id,
sha256)` is written in `FinishBook`, cascades with the package, is backfilled by
migration 0019 from the stored packages, and makes the sweep one indexed anti-join.
Measured on a disposable Postgres 18 with 32,000 packages of about 14 KB (10 runs of
3,200 books, two excerpts each):

| Way | Execution |
|---|---|
| `jsonb_path_query` over every package | 594 to 820 ms warm, and it reads the whole TOAST table |
| anti-join on `evidence_excerpts` (index on `sha256`) | 15 ms |

The one-off backfill took 0.8 s on its own; the whole migration, with the backfill
and the new columns and indexes, took about 3.4 s for 10 x 3,200 packages, holding
ACCESS EXCLUSIVE on `runs`, `run_books` and `evidence` for that time. Nothing else
reads them while the old binary is refused, so the cost is the start-up delay. A prior-package lookup uses the new index
`evidence (book_id, run_id DESC)` and takes 0.07 ms.

**Safety of the sweep.** The prune takes `LOCK TABLE evidence_excerpts IN SHARE ROW
EXCLUSIVE MODE`; `FinishBook` writes its links before it writes the bodies. A package
being stored therefore either commits its links before the sweep looks, or waits and
writes its bodies again afterwards. `TestPruneRacesFinishBookSafely` fails without
the lock. Cost if wrong: a book's package would refer to an excerpt that is gone,
which `verify-seals` reports as damage; it is only text, and the next full check
rewrites it.

**Logging.** Counts only: `retention: pruned runs=… excerpts=…`.

### Migration 0019

All additive: six columns with constant defaults (no rewrite; `runs.incremental`,
`runs.scheduled_for`, `runs.pinned`, `run_books.reused`, `evidence.inspect_version`,
`evidence.measured_at`), the link table and
two indexes, and the backfill. The only constraints are in `CREATE TABLE`, on an
empty table, so the `NOT VALID` rule of #90 isn't needed; the statements are written
to be run twice. An older binary refuses the migrated database, as always.

## Consequences

- An incremental run takes the time of the lookups plus the books that changed; the
  weekly full run keeps the unlikely cases honest.
- `inspect.Version` is a rule contributors have to remember. It is next to the
  inspector code and in its doc comment.
- The v2 spec's row "No scheduled runs" is answered by this ADR.

> Amended 2026-11-22: the source cache makes the lookups cheap by rule now. An incremental run refreshes at most 100 expired answers per source and serves the rest as cached ([ADR-044](ADR-044-source-cache-refresh-budget.md)).

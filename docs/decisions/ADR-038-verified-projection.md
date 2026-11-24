# ADR-038: Serve report pages from a verified projection

- Status: Accepted
- Date: 2026-10-29
- Amends: [ADR-017](ADR-017-sealed-findings-and-reports.md) ("every page verifies the selected run")

## Context

ADR-017 makes every Findings page and CSV export open and verify every sealed
package of the run, including the books before the cursor and beyond the page, so
a damaged projection can't hide a finding. Fixes and Books pages inherited the
same walk. Verifying a book costs one package load, a seal check, the decision
re-derivation and a findings comparison. On a library of 3,196 books one page
costs up to its 15-second budget, and the v2 target for these pages is a
95th percentile of 300 ms.

The writes don't depend on that page-time check. Approve, Apply and the agent
packet builder each load and verify the one package they act on (`decide.Open`),
and the single-book page does too.

## Decision

**A whole-run check.** `report.Service.CheckSeals(run)` walks a finished run once
with exactly the code a page walk uses (the seal; package, row and frozen
identity; the schema; the decision re-derivation; the stored findings rows
against the re-derived findings, so a missing or edited row is damage; the
ADR-017 legacy projection rules). A damaged book is counted and the walk goes on.
The latest result per run is one row of `seal_checks`: when, how many books, how
many broken, the first broken book and its error, and the findings and decision
versions it ran under.

**The projection.** The same transaction that saves the check replaces the run's
projection: `book_projection` (title, tier, item counts), `finding_projection` and
`fix_projection`, all written from the verified book. The projection holds the
report's view (the current derivation and decision), which is what the pages
return, so it needn't match the stored `findings` rows of an older projection
version. Writing at check time rather than in `FinishBook` also fills in runs that
finished before this migration, and leaves one code path that produces pages'
data. Keeping rows rather than reading `evidence.package` means paging, filtering
and counting run in SQL on the primary keys, without parsing a package per row.
A damaged run keeps no projection.

**Pages.** Findings, CSV, Fixes and Books read the projection, with the same
filters, ordering (book id, then ordinal) and `Cursor` as the walk, only when
the run's check passed, is not older than the run's end (`runs.finished_at`) and
ran under the findings and decision versions of the running binary. A binary with
other rules would otherwise show a derivation that the single-book page and the
writes (which re-derive) don't agree with.
Without a check they fall back to today's verifying walk, so correctness never
depends on a check having run. With a failed check they return the integrity
error naming the first broken book; a page for one book (`book_id`) walks that book
only, as before. The Books page follows the same rules: with no check it keeps
today's walk of the page's 51 books, and with a failed check it shows the
integrity error. The single-book page and the Duplicates page are unchanged.

**When checks run.**

- When a run reaches a terminal status, in the background.
- In `serve`: five minutes after startup and then every hour, over every finished
  run whose check is missing, older than 24 hours, older than the run's end or made
  under other findings or decision versions (so a deploy that changes the rules is
  followed by fresh checks); a shutdown stops it.
- `bookwarden verify-seals [--run N]` checks one run or all, prints a line per run
  and exits 1 when a run is damaged.

## Consequences

- A request is a few indexed queries (about 5 ms for a Fixes plus a Findings page
  on a synthetic 3,000-book run, against 1.5 s walking it), and no longer scales
  with the size of the run.
- Damage to the database between two checks can change what a page shows, until
  the next check, which comes at most about 25 hours later (24 hours of age plus the
  hourly sweep; a manual `verify-seals` closes it). It can't change what is written: Approve, Apply and the agent packet
  builder each verify the package they act on, and Book verifies its own.
  Editing a package or the stored findings after a check turns the next check
  red, and from then on every page of that run fails.
- A check runs automatically after every run, so a run's pages are fast as soon as
  it finishes. A run whose check hasn't run yet is as slow as before.
- A check costs a whole walk (about 0.85 s for 3,000 synthetic books) and holds its
  projection in memory.
- Three tables and an index per primary key are added, about one row per finding
  and proposal. The migration is additive and rewrites nothing.
- ADR-017's other rules stand: the seal, the legacy rules and the failure on any
  damaged or missing projection row are the check's, not the page's.

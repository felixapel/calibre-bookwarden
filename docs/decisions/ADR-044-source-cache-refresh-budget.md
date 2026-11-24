# ADR-044: Source cache: stale answers and a refresh budget

- Status: Accepted
- Date: 2026-11-22
- Amends: [ADR-019](ADR-019-source-lookups.md) (the cache TTLs), [ADR-039](ADR-039-scheduled-incremental-runs-and-retention.md) §1

## Context

Every run looks every book up again, incremental runs included: ADR-039 §1
relies on the source cache to make that cheap. Answers were cached for a fixed
30 days (`ok`) or 7 days (`no_match`) and were gone the moment they expired.
The answers a run writes all expire together, at the same hour a week or a
month later, and runs start at the same hour every day. So the wave comes back
on the same weekday, forever.

Run 24 (2026-11-16) met such a wave: 11,444 `no_match` answers from run 17
had expired, every one was asked again at one request a second per host, and
a daily run that usually takes a minute took 64 minutes and ran into the
server's weekly backup. The same rows were due again a week later. The daily
target is a p95 of 10 minutes.

## Decision

1. **An expired answer is stale, not gone.** The cache returns it with a
   stale mark, after the same validation and mapping-version check as a
   fresh row. A row more than 60 days past its expiry is a miss and is
   deleted by the prune at the start of a run.
2. **A refresh budget per run and source.** A stale answer is asked again
   only while the run's budget for that source lasts; otherwise it is served
   as it was cached. An incremental run of the whole library has 100
   refreshes per source; a full run and a one-book recheck refresh every stale
   answer. An ISBN that isn't cached at all is always asked and never counts
   against the budget. A resumed run gets its budget again.
3. **A failed refresh.** If a source answers `unavailable` to a refresh, a
   stale `ok` is served; a stale `no_match` is not, and the result is
   `unavailable`. The same holds while the source's breaker is open. This
   keeps ADR-019's rule that an empty answer from a failing source isn't
   trusted; a source that failed its probe never has a `no_match` served,
   fresh or stale.
4. **A source that needs a key** and has none answers `not_configured`
   before the cache is read, so its old answers aren't served and it uses
   no budget.
5. **Counting.** Each lookup is counted once in the metrics: by the answer
   a source gave, or as `stale_hit` when a stale answer was served (past the
   budget, after a failed refresh, or while the breaker was open).
6. **Jitter.** Each answer is cached for 75 % to 125 % of its TTL, chosen at
   random, so answers written in one run spread over several days.
7. **Sealed evidence does not change.** A stale answer is sealed with the
   outcome and records it was cached with. Whether it was stale is visible
   only in the metrics (`stale_hit`) and never in a package.

## Consequences

- A daily run asks at most 100 expired answers per source, about two minutes
  at one request a second, whatever day the waves fall on. The Sunday full
  run refreshes everything, so no answer served is older than its TTL plus
  about a week.
- A record that appears in a catalogue after a book's `no_match` was cached
  is seen up to a week later than before.
- The cache table keeps up to 60 days of expired rows. That is a few thousand
  small rows for a library of this size.
- No schema change.

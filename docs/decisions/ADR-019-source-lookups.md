# ADR-019: Look up ISBNs through one hardened client and seal the validated records

- Status: Accepted (M3.1)
- Date: 2026-10-18
- Decision: approved in the M3.1 implementation plan

## Context

M3 adds external evidence. A probe on 2026-10-18 with public ISBNs showed that
DNB's MARC21-xml export works again while its RDF has no author names; BNE can
return several records for one ISBN (different editions with different
publishers and years); DNB and BNE dates are years only; Open Library editions
often carry no authors, which come from the linked work and author records;
Open Library's MARC imports come from LoC, Harvard, Columbia and similar
libraries, none from DNB or BNE; Google Books answers 429 without a key.

## Decision

All outbound requests go through one client: HTTPS only, four allowed hosts,
same-host redirects (at most 3), no loopback, private, link-local, multicast or
unspecified addresses, no proxy from the environment, body caps (4 MiB SRU,
1 MiB JSON), one request per second per host, 3 attempts with jittered backoff
and `Retry-After` within a 30 s budget, and a fixed User-Agent. The Google key
travels only in the `X-Goog-Api-Key` header.

DNB and BNE are read as MARCXML by one parser; Open Library through the
edition, its first work and authors; Google Books through `volumes`. A record
counts only when its own identifiers contain the queried checksum-valid ISBN.
Values are stored as the source states them, capped; MARC personal-name
headings are un-inverted for display. Each book is looked up with at most four
ISBNs, from file metadata, then Calibre, then PDF hints, and each lookup
records which seed classes produced its ISBN so that tiers (M3.2) never let a
record seeded only by Calibre or a hint establish identity.

Validated records are cached in Postgres: 30 days for `ok`, 7 for `no_match`;
failures never. A source with 5 consecutive `unavailable` outcomes in a run is
skipped for the rest of the run. The Open Library root-sharing table starts
empty, because no DNB or BNE prefix was observed. Lookups are sealed as an
optional `lookups` field; packages without it seal unchanged, and findings stay
at version 3.

> Amended 2026-10-28: since late September 2026 Google Books answers every `isbn:` query with 200 and no items ([Google issue 569300353](https://issuetracker.google.com/issues/569300353)), which a lookup can't tell from a true `no_match`. A source may now implement a probe that runs at the start of each run: Google Books looks up a volume it certainly holds (9780451524935), and a source that fails its probe is skipped for the whole run as `unavailable`: never asked, never cached, and its cached `no_match` answers aren't served (its cached records are). Settings shows when the probe last failed. A single transient failure of the probe skips the source for that run, stricter than the five-strike breaker; it fails closed and caches nothing.

> Amended 2026-10-29: DNB and BNE are asked for at most 10 records. When an SRU answer reports more matches (`numberOfRecords`) than the page holds, the lookup is `unavailable`, never `no_match` and never a partial `ok`: the record for the ISBN, or another one for the same ISBN that conflicts with those returned, may sit on a page that was never read. Like any failure, it isn't cached and counts toward the breaker.

## Consequences

- If DNB's MARC export fails again, DNB is `unavailable` until it recovers; RDF
  is not used as a fallback.
- A whole-library run is bounded by Open Library's pacing (3–4 requests per
  ISBN at 1 request/s); the cache makes later runs fast.
- If Open Library starts importing DNB or BNE dumps under a new prefix, the two
  count as separate roots until the table is updated; the sealed
  `source_records` allow a later version to correct this.
- Bookwarden's container network must reach the four HTTPS hosts.

> Amended 2026-11-22 by [ADR-044](ADR-044-source-cache-refresh-budget.md): an expired answer is kept for 60 days as a stale answer; a run refreshes stale answers within a budget per source (100 in an incremental run, all in a full run) and serves the rest as cached; TTLs vary by ±25 %.

> Amended 2026-11-22 (B6): a 429 or 503 with `Retry-After` holds its host for every request, not only the one that got it, for at most half the 30 s budget; a request whose budget ends before the hold is over fails at once instead of waiting.

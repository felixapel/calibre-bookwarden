# ADR-041: Report every scheduled run to an Uptime Kuma push monitor

- Status: Accepted
- Date: 2026-11-04
- Builds on: [ADR-039](ADR-039-scheduled-incremental-runs-and-retention.md) (unattended scheduled runs) and [ADR-033](ADR-033-providers.md) (egress rules of outbound clients)

## Context

Scheduled runs are unattended (ADR-039). Nobody noticed when a run failed, a seal
broke or the extractors started failing: on 2026-11-04 an upstream model began
refusing calls and 61 of 207 calls failed silently. Uptime Kuma already alerts to
Discord, and a push monitor alerts both when a push says `down` and when no push
arrives within its interval, which also covers a run that never happens.

## Decision

1. `BOOKWARDEN_HEARTBEAT_URL` (optional) is a Kuma push URL. After every run that
   ended (completed, failed, cancelled; scheduled runs only since the 2026-11
   amendment), once its seal check has returned,
   Bookwarden sends `GET <url>?status=up|down&msg=<text>&ping=`. The push runs on
   its own goroutine, so it never delays the agent review.
2. The status is `down` when the run failed or was cancelled, the seal check found
   broken seals or could not run, or more than 5 % of the run's extractor entries
   were not answered. Entries are counted from the run's sealed packages: those of
   `formats[].extraction.extractors[]` that are not `cached`; one not answered has
   a state other than `ok`. That includes `unavailable` written when nothing was
   called (a quota pause, the per-run cap): the sealed data can't tell it from a
   provider failure, and `down` is the right outcome after a quota hit. The
   message says "not answered", not "failed". A run without extraction reports no
   rate.
3. The message is at most 200 characters and holds counts only, never book text
   and never the run's free-text error.
4. The URL holds a secret token. It is never logged or shown; only `scheme://host`
   is. Egress follows ADR-033: plain `http` or a private literal address needs
   `BOOKWARDEN_HEARTBEAT_ALLOW_CIDR` (exact addresses), and so does a host name
   that resolves to a private address (otherwise every push is refused at dial
   time, and only a warning and Kuma's missed-interval alert follow). A literal
   host outside the allowance is a configuration error, and the client dials only allowed addresses, never
   follows redirects, and checks the address at dial time (`netguard.DialControl`,
   shared with the agent seats).
5. A push times out after 10 s and is retried once after 5 s. After that a warning
   (host only) is logged and the beat is dropped. A stop cancels it.

## Consequences

- A Kuma outage never affects a run. A missed beat is a missed alert, not a
  retry queue.
- ~~A run pruned before its seal check returns beats `down` ("seals not checked").~~
  Replaced by the 2026-11 amendment: it sends nothing.

## Amendment (2026-11): scheduled runs only, and a floor for the rate

- **Only a scheduled run beats.** A run a person started (a manual full run, a
  one-book recheck, a cancelled recheck) sends nothing: a manual run that beat
  `up` would reset Kuma's missed-interval timer and hide a schedule that has
  stopped, and a cancelled recheck would beat `down` for nothing. The hook reads
  the run the way the automatic review does (`ScheduledFor` set). A run that
  can't be read sends nothing and logs one warning; a run pruned before its seal
  check returned sends nothing either (this replaces the last consequence above),
  and Kuma's missed-interval alert covers both.
- **The 5 % rule needs 20 calls.** Below 20 extractor calls the rate is still in
  the message but can't turn the beat `down`: one unanswered call of a one-book
  run is not an outage. From 20 calls the rule is unchanged (1 of 20 is `up`,
  2 of 20 is `down`).
- The Kuma monitor's interval stays a little over the schedule's period: 26
  hours for a daily run.

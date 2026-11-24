# ADR-034: Blind individual decisions are the labels that earn automation

- Status: Accepted for part 1 (labels and the precision page) and for carried decisions. Amended 2026-11-04 (review-first, below): parts 1 to 3 of the later parts are out of v1.0. Amended 2026-11-24: approvals carry too (Carried decisions).
- Date: 2026-10-29
- Decision: v2 design section 5 (Certification); part 1 is milestone M1, PR 2

## Context

The v2 design lets a fix type earn automatic approval by showing, from Felix's
own decisions, that its precision is high enough. A decision only measures
that when it was a considered judgement of the single fix. Until now nothing
recorded how a decision was made, and the Fixes page made careless decisions
the easy ones:

- every pickable row was pre-ticked, so approving a page confirmed everything;
- "Approve all shown" approved up to 50 rows in one click;
- the agents' verdicts were visible before Felix decided, so agreeing with
  them was cheap and the label measured the panel, not the fix.

Labels collected that way would certify nothing.

## Decision

Part 1 changes what the page records and shows, and reports it.

**Recording.** Every proposal carries `decision_mode` (`individual`, `bulk`
or `agent`) and `verdicts_shown`. A decision from a per-row button is
`individual`; ticked rows and "Approve all shown" are `bulk`; the agent panel
records `agent`. `verdicts_shown` is true when the agents' verdicts were
revealed, or when the fix already had a decision (decided rows always show
their verdicts, so deciding one again is never blind). A reveal is
remembered: showing the verdicts records the undecided fixes on screen in
`verdict_reveals`, and a later decision on any of them is recorded as shown,
even after the verdicts are hidden again. Deciding a fix the same way again,
for example by "Approve all shown" over a row approved one by one, changes
nothing and keeps the basis it had. Decisions made before
migration 0013 read as `bulk` (`agent` where the agent made them), with
verdicts shown: their mode is unknown, so they are never labels.

**The page.** No row is pre-ticked. Each pickable row has its own Approve and
Reject buttons. The agents' verdicts and note stay hidden on undecided rows
until Felix follows "Show agents' verdicts" (`?verdicts=1`). Filtering by
"Decided: disagree" is a look at the verdicts, so it reveals them.

**Labels.** A label is a decision with `decision_mode = 'individual'` and
`verdicts_shown = false`. A fix type is (field, path, kind): the path is the
proposal's sealed roots, sorted and joined with `+`, and the kind is `fill`
when the current value is empty, otherwise `replace`. Approved, applied,
stale, blocked and failed proposals count as correct, since Felix approved
them; rejected counts as a miss. A skip gives no label.

A fix proposed again in a later run (because it wasn't applied yet, or was
blocked) is the same fix and counts once: per library, book and proposal,
only the first decision can be a label, since every later one may have seen
its verdicts. Any person's rejection of that fix, in any run, makes it a
miss.

**The precision page** (`/precision`) lists, per type and over all runs, the
labels n, the misses k, the one-sided 95 % Clopper-Pearson lower bound on
precision and the progress toward 598 labels with zero misses. For k = 0 the
bound is 0.05^(1/n) (n = 60: 95.13 %, n = 299: 99.00 %, n = 598: 99.50 %);
otherwise it is found by bisection on the binomial tail. No dependency is
added.

## Carried decisions

A fix a person rejected in one run comes back, undecided, in the next run that
proposes it. Rejections now carry forward (inbox v1, v2 plan M6). Since
2026-11-24 approvals carry as well (Felix): an approval that wasn't applied
before the next run used to go stale as "superseded" at Apply, so every weekly
run asked again for the same fixes.

**What carries.** When a run's first seal check writes its projection, each
projected fix that is the same fix as a person's rejection or approval in an
earlier run of the same library is recorded with that status: `decision_mode = 'carried'`,
`verdicts_shown = true`, `decided_by` the original decider, and `carried_from`
pointing at the original proposal. "The same fix" is the same library, the
same Calibre book uuid (the book id can change), the same field, exactly the
same current and proposed value lists (compared as JSON, in order) and the same
path (the sealed roots, sorted, joined with `+`). Only the latest person
decision on the fix counts: a rejection later changed to an approval carries
as the approval, and the other way round. An approval Apply retired as stale
("superseded"), because the next run had already written the book's evidence
when Apply ran, was never applied and carries as an approval; any other stale
reason (the book or the evidence changed) doesn't carry. A carried row carries on into
the next run, still pointing at the original. Only a run's first check
carries: a later one (the nightly sweep, a rules change) never back-fills a run
a person has already looked at, so runs that were checked before this existed
get no carried rows. The agents' review of a run is queued only after its seal
check has returned (a failed check doesn't hold it back), so a rejection is in
place before the panel can decide that fix; the panel's decisions never
overwrite one, since it decides only fixes that have no decision.

**A carried approval retires the one it copies.** The earlier approval, still
unapplied, becomes `stale` with the reason `carried`, so Apply writes the fix
once, from the newest run's evidence, and the approved count doesn't count it
twice. Apply still checks the book against Calibre and, for a fix rooted in the
printed text, re-verifies it with the current extract version (ADR-036), so a
carried approval whose book changed is refused there like any other.

**What never carries.** An agent decision never
carries (the agent is a veto layer, ADR-029, and its decisions are not
labels); it doesn't block a person's earlier rejection from carrying either.
A fix that differs in any value or in the path is a different fix and comes
back undecided. Nothing crosses libraries.

**Not a label.** A carried decision is never a label: the labels query takes
only `individual` decisions, and a carried row is a copy of a decision that
already counted. Muting counts a carried row only once it is applied: it then
stands for the original, so an undo of that write is a miss, as it would be for
the original. The precision rule is unchanged: a fix counts once, by its
first decision, and any person's rejection in any run is a miss, so a
carried rejection neither adds a label nor a miss.

**Undoing one.** On the Fixes page a carried decision shows "carried from run
N" with a link to the original, and is changed like any other decision. The
change is a normal decision, `individual` or `bulk`, with `verdicts_shown`
true (a decided row always shows its verdicts), and clears `carried_from`.
Deciding it the same way again keeps it carried, as it changes nothing.
Deciding it the other way carries that decision from then on.

**Direction.** Until 2026-11-24 carrying only produced rejections, so a wrong
carry could only keep a fix out of the inbox. A carried approval can now reach
Apply without being looked at again, but only for exactly the fix a person
approved: the same book, field, values and roots. Any difference makes it a new
fix that comes back undecided.

**Retention.** A carried row is a copy of a decision that lives in an older
run, so it doesn't keep its own run from being pruned: the prune ignores
carried rows when it picks the runs to keep and deletes them with the run.
The run holding the original rejection is kept by that rejection, so the
carry goes on. A run where a person changed a carried row holds a real
decision and is kept.

## Later parts

These are in the v2 design and not part of this ADR's acceptance:

1. The certifier: which types have 598 labels with zero misses (about 949
   with one), and the switch that lets a certified type approve on its own.
2. Blind audits after certification, and rule changes that replay labelled
   packages at the new decision version. `bookwarden eval --run N --version V
   [--baseline B]` is the tool: it replays a run's sealed packages at another
   version, read-only, and reports the tier matrix, per-field proposal deltas,
   reason codes gained and lost, and the fix types (field, path, fill or
   replace) at the new version, so a rule change shows which labelled types
   it touches before it runs on the library.
3. Circuit breakers: a miss in an audit, or an undo of an automatic fix,
   trips the type and returns its automatic fixes since the last clean audit
   to the inbox.
4. The gold set, which measures recall and labelling stability but never
   certifies.

## Amendment (2026-11-04): review-first

Felix decided this with the [v1.0 production design](../superpowers/specs/2026-11-04-bookwarden-v1-production-design.md).

- **Why.** Thirteen stored runs (2–14) showed that no fix type in this library can reach 598 labels. The largest field type has had 62 unique fixes, and title sort has 496 in all.
- **Nothing applies on its own in v1.0.** That rules out:
  - the certifier (later part 1);
  - audits (later part 2; the rule-change replay, `bookwarden eval --version`, stays);
  - circuit breakers (later part 3);
  - agent mode "on" (ADR-029).
- **Labels are unchanged.** Blind individual decisions keep counting, so pooled certification can be decided later on real numbers.
- **Muting is new.** `/precision` also mutes a fix type whose decisions show poor precision (design §6.3). Muting hides fixes and never applies any, so individual and bulk decisions count for it, and carried and agent decisions don't.
- **The gold set.** Later part 4 shrinks to a 200-book recall sample (design §6.4). Its answers are confirmations, never labels.

## Consequences

- Deciding takes more clicks than ticking a page. That is the cost of a
  label, and bulk approval remains for fixes Felix does not want to label.
- A decision's basis comes from the request and from the recorded reveals.
  A forged form could still claim a blind decision on a fix whose verdicts
  were never put on a page; with a single admin that isn't a threat.
- In mode "on" the panel decides every unanimous fix, so the fixes left for
  Felix are the ones it disagreed on. Labels on them come from a skewed
  population. While the mode stays off or shadow this doesn't arise; the
  certifier (later part 1) only counts labels from runs whose
  review didn't run in mode "on". (Mode "on" is retired since 2026-11-04,
  see the amendment above.)
- Changing a decision overwrites its basis, so a later reveal-and-change makes
  the earlier blind label disappear. That is deliberate: the final decision
  is the one that is applied.
- Nothing is automated by part 1. The pages and the agent panel behave as
  before apart from the above.

# ADR-048: Decision v11: propose only what an undo can restore

- Status: Accepted
- Date: 2026-11-24
- Amends: [ADR-020](ADR-020-tiers-and-proposals.md) (which fixes are proposed), [ADR-045](ADR-045-decision-v10-text-root-corrections.md) (rule 3 covered the new value only)

## Context

Every write is guarded by an exact undo (ADR-021): Apply refuses a fix when
Calibre couldn't store the value it would put back, with the reason
`calibre_cannot_restore`. Decision v10 stopped proposing a pubdate Calibre
would shift on write, but only looked at the new value. After v2.0.0-rc.3,
run 33 still had 10 open fixes nobody could approve: 9 pubdates whose
current Calibre value sits on a day Calibre moves on write (a year-only
placeholder on the 1st, or a midnight shifted by the time zone onto the
last day of a month) and 1 title sort with a double space. They could never
leave the inbox. Felix chose to stop proposing them (2026-11-24).

## Decision

From decision version 11, after all proposals are made, a proposal is
dropped when an undo couldn't put Calibre's current value back exactly,
by the same rule Apply uses for Calibre 9.16:

- a title needs a non-empty, clean title and, when captured, a non-empty,
  clean title sort (a title write brings the sort back, ADR-028); a title
  sort needs a captured, non-empty, clean one;
- authors need a non-empty list of clean names without "&" or "|" and
  without two that differ only in case, and a clean author sort; an author
  sort or a publisher needs a clean value;
- languages need valid three-letter codes, none repeated and none of
  `und`, `zxx`, `mis`, `mul`;
- an ISBN needs Calibre's identifiers to be clean (the write puts all of
  them back);
- a pubdate needs no date, or one that parses as a date (`2006-01-02` or
  RFC 3339), falls in a year from 1000 on, and whose day Calibre keeps
  (`findings.ShiftedDay` false).

"Clean" means stripped, without whitespace runs or control characters.
`decide.Restorable` is a frozen copy of the fix package's rule as it
stood at version 11; `TestDecideV11RestoreRuleMatchesFix` keeps the two in
step. A later change to Apply's rule needs a new decision version. The copy
still calls `findings.ShiftedDay` and the Unicode case folding of
`golang.org/x/text`, as v10 already does: a change to either changes sealed
v11 decisions and needs a new version too.
`testdata/replay_v11_golden.json`, generated on code without this rule, pins
versions 1 to 10.

Series proposals, planned as v11 (ADR-045), become v12.

Nothing else changes: the tiers, the reasons and every other proposal are
v10's. Wikidata and Hardcover stay recorded only.

## Consequences

- The wrong value stays in Calibre. Bookwarden can't fix it safely; correct
  it in Calibre by hand if it matters.
- The 10 open fixes of run 33 go away when the next check runs under v11.
- An authors fill over an empty author list is no longer proposed (Calibre
  itself never leaves the list empty; it writes "Unknown").
- Packages sealed at v1–v10 keep their derivation.

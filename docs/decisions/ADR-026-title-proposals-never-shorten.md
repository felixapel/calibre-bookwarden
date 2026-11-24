# ADR-026: A title proposal never shortens Calibre's title

- Status: Accepted
- Date: 2026-10-26
- Decision: approved in the title-proposals plan; amends ADR-020's field rule

## Context

Cutover run 2 on the real library had 28 Tier A title proposals. Most of them
dropped what Calibre's title has beyond the main title: a subtitle ("Battle
Cry of Freedom: The Civil War Era" → "Battle Cry of Freedom") or a leading
article ("The Art of Loving" → "Art of Loving"). The field rule saw two roots
agree, the files and Open Library, but Open Library keeps the subtitle in its
own field, and most files state the main title alone. Their agreement on the
main title is no evidence that Calibre's longer title is wrong. None of these
proposals were applied.

## Decision

**The guard.** There is no title proposal when the proposed title's words are
a shorter contiguous run of Calibre's title words. Words are the case-folded
title split at every character that is neither a letter, a combining mark nor
a digit, so punctuation never joins or splits words and a vowel sign stays
part of its word.

**What stays proposed.** A title with more words than Calibre's ("Recognitions"
→ "The Recognitions"), one that differs only in punctuation ("… Routledge
Revivals" → "… (Routledge Revivals)"), and one whose words differ, such as a
typo fix.

**Versions.** The proposal rule changes, so the decision version becomes 2.
A package sealed under version 1, such as cutover runs 1 and 2, still opens
with the version 1 rule and shows its proposals as sealed; a new run gets the
guard.

## Consequences

- A typo fix that also loses Calibre's subtitle ("Edmund champion: jesuit and
  martyr" → "Edmund Campion") is still proposed, with both values shown.
  bookwarden never builds a title from parts that different roots state.
- Junk Calibre keeps after the main title ("… Pulitzer Prize Winner",
  "… 10th Anniversary Edition") is no longer proposed away; it stays Felix's
  edit in Calibre.
- Old runs keep showing their version 1 title proposals until a new run
  replaces them on the Fixes page.

> Amended by [ADR-045](ADR-045-decision-v10-text-root-corrections.md): from decision version 10, a title proposed without a text title shortens Calibre's only by its noise (ADR-036 §3.4), not by the two shapes above.

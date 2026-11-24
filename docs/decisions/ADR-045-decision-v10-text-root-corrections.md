# ADR-045: Decision v10: corrections to the text root

- Status: Accepted
- Date: 2026-11-23
- Amends: [ADR-020](ADR-020-tiers-and-proposals.md) (rule 4), [ADR-026](ADR-026-title-proposals-never-shorten.md) (rule 5's record path), [ADR-036](ADR-036-text-root.md) §3.3 and "Decision v9" (rules 1, 2, 6)

## Context

A review of the truth layer (plan v4, `audit-decisions.md` Q1-A to Q1-F)
reproduced six kinds of wrong proposal in decision v9. Each is a rule that
lets a value through that the evidence doesn't back, or mangles one it does.
In production, 8 of the 23 open pubdate fixes were refused at write time
because Calibre would store another day, so they could never leave the
inbox. Decisions are sealed per version (ADR-017, ADR-038), so the
corrections are a new version; packages sealed at v1–v9 keep their
derivation, pinned by `testdata/replay_v10_golden.json`.

## Decision

From decision version 10:

1. **Author dissent needs the same people.** In the text-alone path
   (ADR-036 §3.3) a root that may back a fix dissents on authors unless the
   text covers the root's names *and* the two lists are the same people under
   rule 1 of §3.1 (`authorsConsistentV7`: equal, or the same first author and
   at most two extra names). A root listing a subset of the printed people
   with the same first author still doesn't dissent. Example: file and DNB say
   [Ann Author], the text prints [Bob B, Cara C, Dan D, Ann Author], Calibre
   has none: v9 filled all four from the text; v10 flags
   `text_roots_disagree` and proposes nothing.
2. **Capitals are not a spelling.** A text-alone title, author list or
   publisher with an uppercase letter and no lowercase one takes the spelling
   of another root that states the same key and is not in capitals itself; if
   none does, the field is not proposed. Digits and scripts without case are
   not capitals. Example: Calibre's title `scan_20190101_123456.pdf`, the text
   prints `THE PRINTED TITLE`, nothing else states a title: no proposal; with
   the file's `The Printed Title`, that is proposed.
3. **No date Calibre would shift.** A pubdate is not proposed when Calibre
   would store another day: the 1st of a month, or the last day of a month
   other than December (`findings.ShiftedDay`, which the write-time guard in
   `internal/fix` calls too). The book keeps its `date_mismatch` finding.
   Example: every source says 2005-06-30: no proposal.
4. **A record speaks about the title and authors only for the same book in
   the same language.** A record root supports or dissents on a title or
   authors proposal only when a language it states is the files' and every
   file author is among its authors. Each check needs the files to state one
   key for it: with no single files' language a record's language is not
   compared, and a record that states no language passes that check, but it
   still needs the files' authors among its own. Record conflicts and tiers
   (`records_*_conflict`) still count every record, so a record in another
   language can still make a book Tier C. Example: an English EPUB titled `Der Originaltitel` and an Open
   Library record in German with the same title no longer replace Calibre's
   `The Translated Title`; the file alone is one root.
5. **Subtitles.**
   - In the text path a record that keeps its subtitle in its own field states
     its title joined with it, the way the text joins its own (below), when
     that is the text's title; dissent reads it the same way. Example: file `Main: Sub`, DNB title `Main` with subtitle `Sub`, text
     `Main: Sub`, Calibre `Old Title`: proposed from `[dnb file text]`.
   - Without a text title, the record path shortens a title only by its noise
     (`shortensV8`, ADR-036 §3.4) instead of ADR-026's two shapes. Example:
     Calibre `Dune, Book Two: Messiah`, file and record `Dune: Messiah`: no
     proposal.
   - The text joins a printed title and subtitle with `": "`, but with one
     space after a title that ends in `:`, `?` or `!`: `Who?` and `Sub` give
     `Who? Sub`.
6. **Publishers in capitals.** v9 title-cased a text-alone publisher printed
   in capitals, turning `MIT PRESS` into `Mit Press`. From v10 it follows
   rule 2: another root's spelling, or nothing. v9's title-casing stays for v9
   packages.

Wikidata and Hardcover stay recorded-only (ADR-035, ADR-036 §6).

Series proposals, once planned as decision v10 (ADR-037 part 1 in the spec),
are now v11.

## Not in v10

- A check that the two extractors are of two model families: the sealed data
  has no family names to check.
- Calibre's default title "Unknown" as junk.
- An invalid Calibre ISBN read as empty.
- A record spelling in capitals in the two-root path: rule 2 covers the text
  alone.

## Consequences

- New runs are decided at v10; packages sealed at v1–v9 open and project as
  before. `bookwarden eval --run <N> --version 10 --baseline 9` counts the
  differences on a stored run before anything in the inbox is decided.
- Some books whose Calibre authors already agree with the catalogues get
  `text_roots_disagree` when the printed list puts another person first; no
  proposal changes.
- Write-time refusal of shifted days stays, for v9 proposals still in the
  inbox.

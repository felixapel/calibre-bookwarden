# ADR-030: A source record matches the files despite what records routinely leave out

- Status: Accepted
- Date: 2026-10-28
- Decision: Felix's ("proceed" on the five tolerances, 2026-10-28); amends ADR-020's Tier A rule

## Context

Tier A needs one exact-ISBN source record that agrees with the files on
title, authors and language (ADR-020). Check run 6 (2026-10-27, 3,196 books)
had 321 Tier A books and 790 Tier B books whose only problem was
"records_disagree". An analysis of those 790, run with this package's own
comparison keys, found that most records weren't about another book. They
were written differently:

- 111 Open Library records state no language at all ("Working Backwards"
  matches its record exactly on title and authors and fails only on that).
- 212 titles differ by a subtitle, which Open Library keeps in its own
  subtitle field, and 197 by a leading article the record drops ("Color
  Purple").
- 93 records list translators or editors besides the file's authors.
- Only 38 titles and 39 author lists name genuinely different books.

759 of the 790 have Open Library as their only source, so none of these
books can get a proposal anyway: a tier only qualifies a book.

## Decision

From decision version 5, a record matches the files' title, authors and
languages keys when all of these hold:

- **Title and authors are stated.** A record without a valid title or
  authors key never matches.
- **Title.** The record's title equals the files', or the record's title and
  its own subtitle field joined by ": " do, after both sides:
  - drop one trailing parenthetical, unless it is nested or may name a
    volume, part or book: a word vol(s), volume(s), volumen, book(s),
    part(s), pt(s), band, bd, t, tom, tome(s), tomo(s), teil, deel,
    livre(s), libro(s), no, nos or nr; a "#" or "№"; a roman numeral; or a
    number of up to three digits (a four-digit year is dropped). The list
    errs toward keeping a parenthetical, which only makes the match
    stricter;
  - drop one leading "the", "a" or "an";
  - read " & " as " and ".

  A subtitle the record doesn't state itself is never assumed, so "Series:
  Book Two" doesn't match a record "Series" with subtitle "Book One", nor a
  record "Series" with no subtitle.
- **Authors.** Every file author is one of the record's authors, by the
  exact folded name. The record may list more people, such as translators.
  A name written differently ("Walker, Alice" against "Alice Walker") still
  doesn't match: initials and name forms can name different people
  (ADR-020).
- **Languages.** The record's languages equal the files', or the record
  states none and no other record for the ISBN states a different language.
  A record that states another language, or one that can't be read, doesn't
  match.

Nothing else changes:
- Two records that disagree with each other still make the book Tier C.
- Proposals still need two independent roots and no veto (ADR-020, ADR-026,
  ADR-027).
- Packages sealed under versions 1 to 4 still open with their own rules.

## Consequences

- On the 790 run-6 packages, 288 books move from B to A, and no proposal
  changes. A Claude Haiku 5.5 read of all 288 file and record pairs found
  no different work, volume or language, and four borderline rows were the
  same books.
- A real co-author that the files leave out doesn't block Tier A either:
  "American Prometheus" files name Kai Bird, its record Kai Bird and Martin
  J. Sherwin. The files' authors stay what they are; only a proposal could
  add a name, and that still needs two roots.
- A Tier A book's language may now be the files' alone, when its record
  states none. A Tier A book's record may name only the main title.
- Name-form matching, packed author strings ("Steven D.; Dubner; …") and
  records in another script remain mismatches. They are file problems, not
  record ones.
- Changing these tolerances needs a new decision version.

## Amendment: Decision v7 tolerances

- Date: 2026-11-01
- Decision: Felix's priority is precision; this widens what counts as "the
  same" for tiers only (decision version 7)

### Context

Check run 10 was the first full v2 run. Google Books answered again, so many
books had a second root, and 46 books moved from B to A. But 40 moved from A
to C on `records_title_conflict` or `records_authors_conflict` between Open
Library and Google Books. A sample of 14 showed almost none were real
disagreements:

- a leading article ("Precipice" against "The Precipice", "Sport of Nature"
  against "A Sport of Nature") or one before a subtitle ("… Handbook : A
  Step-By-Step …");
- contributors that only Open Library lists as authors (Freud and Gay, Zweig
  and Bell, Dahl and Blake, Bellow and Coetzee, Schopenhauer and
  Hollingdale, Joyce and "Gabler Gabler");
- "&" against "and"; a mangled apostrophe ("Homeowner?s"); "Dr. Jason Fung"
  against "Jason Fung"; an imprint in the title ("Penguin Classics Passage To
  India" against "A Passage to India (Penguin Classics)") with "E M Forster"
  against "E. M. Forster"; an extra parenthetical subtitle.

Version 5 already read the files against a record this way (above). Two
records were still compared by exact key, so any of these made the book Tier
C.

### Decision

From decision version 7, the record-against-record check
(`records_title_conflict`, `records_authors_conflict`), the record-against-file
match (`records_disagree`) and the other-work guard use the rules below.
Languages are still compared exactly. Every rule runs only at version 7 or
later; versions 1 to 6 re-derive byte-identically (the replay goldens are
unchanged, and `testdata/replay_v7_golden.json` pins versions 1 to 6 of every
shape below, generated before this change).

1. **Leading articles.** One leading article is ignored on the main title and
   on the subtitle (the part after the first ": " or " : "), from a closed list
   per language. A language is the files' only language, else the records'
   only language; with none or several, no article is ignored.

   | Language | Articles |
   |---|---|
   | en | the, a, an |
   | de | der, die, das, ein, eine |
   | es | el, la, los, las, un, una |
   | fr | le, la, les, l', un, une |
   | it | il, lo, la, i, gli, le, l' |

   It is ignored only when words follow, and those don't start with another
   article. A title equal to a bare article is never reduced: "The" keeps its
   article and matches only itself.
2. **"&" and "and"; apostrophes.** "&" and the language's word (en and, de
   und, es y, fr et, it e) are one token. The apostrophe class `'` `’` `ʼ`, and
   a "?" between two letters (Open Library's mangled apostrophe), become one
   apostrophe. Titles only; the "?" rule is not applied to names.
3. **Names.** Dots are blanks, so "E M Forster", "E. M. Forster" and
   "E.M. Forster" agree. Leading dr, prof, sir, dame, rev, mr, mrs and ms are
   dropped while a name remains ("Dr. Jason Fung" is "Jason Fung"; a name that
   is only "Sir" stays). The repo has no doubled-final-token normaliser, so
   "Gabler Gabler" is not folded: it counts as one more name (rule 4).
   "Surname, Given" forms still differ from "Given Surname" (above).
4. **A contributor superset is consistent.** Two author lists are consistent
   when they are equal after rule 3, or one is a strict superset of the other,
   both have the same first author after rule 3, and the longer list holds
   at most two names more. This changes only the conflict and tier verdict.
   An `authors` proposal still needs roots that state the very same list
   (ADR-020), and ADR-027 and ADR-031 still hold: translators and editors are
   not authors. A superset never proposes adding or removing a name.
   Against the files, a record also matches when every file author is among
   its authors by rule 3, or the lists are consistent (the record may be the
   shorter one, within the same limits).
5. **An imprint in the title.** The closed list of series imprints in
   `internal/findings/v5.go` (ADR-035 part 3: Penguin Classics, Penguin
   Modern Classics, Oxford World's Classics, Everyman's Library, Vintage
   International, Vintage Classics, Signet Classics, Signet Books, Dover
   Thrift Editions, Modern Library Classics) is read through
   `findings.SeriesImprints()`; there is no second list. An imprint is
   ignored as a prefix when a title follows it. As a parenthetical suffix it
   was already dropped by ADR-030's trailing-parenthetical rule.
6. **Trailing parentheticals.** Between records, a trailing unnested
   parenthetical that names no volume (the list above) is ignored only when
   the other title has none: "History (Illustrated Edition)" matches
   "History", but "(1914-1918)" and "(1939-1945)", "(One)" and "(Two)",
   "(Abridged)" and "(Unabridged)", "(2nd Edition)" and "(3rd Edition)" stay
   conflicts. The side that has them drops all it has, so that the result is
   idempotent. This covers Google's extra parenthetical subtitle. ADR-026's
   `shortens` is a proposal guard (a title proposal never drops words of
   Calibre's title) and plays no part in the conflict check.
7. **The files count too.** When records state different lists or titles, the
   files' own key must also be consistent with every record's. Files [A, C]
   against Open Library [A, B] and Google [A] stay a conflict, though Google's
   list is consistent with each of the others.

The normalisers repeat their steps until nothing changes, so they are
idempotent, and they never reduce a non-empty title or name to nothing. Both
are fuzzed.

### Rulings, and what each costs if wrong

Each ruling errs toward keeping a conflict, because a missed tolerance leaves
a book in Tier C as it is today, while a false match hides two different works.

1. **"Man" against "A Man" stays a conflict.** An indefinite article (a, an,
   ein, eine, un, una, une) is ignored only before at least two words. A
   definite one is ignored before one word, since "Precipice" against "The
   Precipice" and "Secret" against "The Secret" are the run 10 cases. Cost:
   a catalogue that drops "A" before a single word ("Secret" for "A Secret")
   stays a conflict, and if it is the other way round, "A Man" (Fallaci) would
   have matched "Man".
2. **No language, no article.** With no single language, nothing is ignored in
   the conflict verdict. Cost: books whose files and records state no language
   keep their conflict. (The record-against-file match keeps version 5's
   English articles whatever the language, because it only adds to version
   5's.)
3. **Tolerances only add.** The version 7 match accepts whatever version 5's
   accepted and more, and a conflict at version 7 is a conflict at version 6.
   No book gets a worse tier from this change.
4. **The ISBN proposal keeps version 6's requirements.** Its title key must be
   identical, with no article fold, and its record must match the files by
   version 6's rules. Cost: a book whose only matching record differs by an
   article gets no ISBN proposal. The front-matter confirmation (ADR-035 part 2)
   also keeps version 6's match, so a tolerance never selects an identity.
5. **Imprint list reuse, not extension.** Imprints outside the findings list
   (for example "Penguin Books") stay conflicts. Extending the list needs an
   ADR change and, for the decision, a new version.
6. **Other-work guard.** Records that agree with each other up to these
   tolerances count as one work, and the guard fires when none of their
   spellings is Calibre's own. Cost if wrong: none for precision; without it, a
   book with a wrong file could pass as Tier A.
7. **The record's separate subtitle field plays no part in the conflict
   check.** It is read only by the match against the files (as before).
   Cost: a pair like "Title" with subtitle "Sub" against "Title: Sub" stays a
   conflict.
8. **Italian "e" is read as "and".** The brief listed und, y and et; the
   Italian row of the article table had no conjunction, so "e" was added for
   it. Cost if wrong: an Italian title with "e" against "&" matches.

### Precision argument

Every rule is applied to both sides of a comparison, so it can only equate two
strings that are equal once the same words are ignored. What is ignored is a
closed list (articles, conjunctions, honorifics, imprints), a punctuation
class, or at most two contributors beside the same first author. What stays
different: "The Road" against "Road to Serfdom", "Dune" against "Dune
Messiah", "Tom & Jerry" against "Tom and Jerry's Big Adventure", a volume in
parentheses, a different first author, a third extra name, and two lists of the
same size that differ by a name. Table tests pin these.

### Consequences

- **Tolerances move tiers; title and authors proposals still need strict
  agreement.** Proposals are built from records exactly as before: no
  normalised form feeds a value. Lifting the Tier C gate would otherwise have
  re-opened proposals for exactly the fields that agree only through a
  tolerance (a translator added as an author, "Precipice" renamed "The
  Precipice"). So from version 7 `title` and `authors` are not proposed when
  the identity's records state different values for that field, whatever the
  tolerance says. Other fields are unaffected: a book that was Tier C and is
  now A or B carries the proposals the same field rules build for them (a
  test compares them with version 6's rules without the Tier C gate), so a
  publisher two roots agree on may now be proposed, where Tier C hid it.
- **A front-matter identity is disputed by the strict comparison.** The record
  conflicts and the disagreement that drop a front-matter identity (ADR-035
  part 2) are version 6's, so a tolerance never keeps an identity that version
  6 dropped. The ISBN proposal and the identity's confirmation keep version
  6's match for the same reason.
- **`other_work` can replace `records_title_conflict`.** When records agree up
  to a tolerance and none is Calibre's spelling, the book is Tier C with
  `other_work`, which, unlike the conflict, withholds the sort proposals too
  (ADR-035 part 2). That is the safe direction: fewer proposals, never more.
- New runs derive at version 7. Packages sealed under versions 1 to 6 still
  open and re-derive with their own rules (`decide.Open` derives at the
  sealed version), so version 7 tiers appear only in runs checked after this
  change; the seal check's re-projection does not apply version 7 to old runs.
- `bookwarden eval` accepts version 7, and measures the change against a
  run before it is used.
- Wikidata and Hardcover stay recorded only at version 7 (ADR-035).

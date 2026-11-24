# ADR-036: The book's printed text is a root (decision v8)

- Status: Accepted (merged with decision v8, 2026-11-04)
- Date: 2026-11-04
- Decision: the [v1.0 production design](../superpowers/specs/2026-11-04-bookwarden-v1-production-design.md) §4 and §6.2, and the [M4 design](../superpowers/specs/2026-11-02-m4-grounded-extraction-design.md) §5, §7 and §9. Amends ADR-020 (the field rule), ADR-026 (titles never shorten), ADR-035 part 2 (an ISBN taken from a copyright page is never proposed) and the M4 design §8 (re-verification).

## Context

The style guide (ADR-031) names the book's printed front matter as the authority for:
- the title (Title 1);
- the imprint (Publisher 1);
- the ISBN printed in the file (Identifiers 1).

Up to decision v7, proposals come only from the file's metadata and exact-ISBN records. Felix's person decisions on record-based fixes rejected most of them: authors 19 of 19, publisher 40 of 46, title 23 of 29.

Since M4a, every EPUB format carries a sealed extraction block (schema 4):
- grounded claims, each a quote that code has verified in the sealed excerpt;
- the agreed facts, where the parser or two model families give the same value and no voice grounds another.

Run 14 measured these candidates, as counts only:

| Field | Candidates |
|---|---|
| Title | 1,733 books with an agreed title. Of those, 139 really differ from Calibre's and 272 have extra text in Calibre's |
| ISBN | 167 printed where Calibre has none, 277 that differ |
| Imprint | 62 where Calibre has no publisher |

Decision v8 makes those facts a root.

## Decision

### 1. The text statement
For each field below, the package gets at most one `text` statement, built from the sealed `Extraction.Agreed` facts.

- **Only `agreed` facts count.** `single` gives no statement. `disagree` gives no statement and vetoes every proposal for that field: the book's own text is unclear.
- **EPUB formats.** An EPUB format (EPUB, ORIGINAL_EPUB) with no extraction block, or whose extractors all failed (no extractor in state `ok`), is ignored: it neither agrees nor conflicts. At least one EPUB must give the statement. Two EPUBs that both give one must agree; if they differ there is no statement and, because the printed text is contested, every proposal for that field is vetoed, record-only ones included. The reason is `formats_text_conflict`, which counts as tier B.
- **Values** (keys through the same `findings` key functions the other roots use):
  - `title`: the agreed `title`, or `title + ": " + subtitle` when a `subtitle` is agreed too (ADR-031 Title 1).
  - `authors`: the agreed `contributor` list in printed order, keeping only the `author` role. If no author is left, there is no statement.
  - `publisher`: the agreed `imprint`.
  - A title, a publisher or an authors list that is itself junk under the §3.3 detector (a file-name title, a placeholder publisher, a single placeholder author) gives no statement.
  - `isbn`: the agreed `isbn` values decide it as follows:
    - a value counts only if both a parser claim and an extractor claim ground it in units that are not `back` units (the claim's unit is mapped to the excerpt's landmark). Back matter and advertisements never count for an ISBN;
    - a value labelled hardcover, paperback, print or PDF never counts, as in v6's front-matter rule (ADR-031, Q1);
    - if any value is labelled `ebook`, exactly one of those gives that one;
    - otherwise exactly one unlabelled value gives that one;
    - anything else gives no statement (ambiguous).
- **No text statement** in v8 for languages, series, and the years.

### 2. The root and its weight
- **Weight.** `text` is a root of its own. Its class lets it give authority and veto, like DNB, BNE and the file metadata.
- **Path.** A proposal's path lists `text` among its roots, sorted after the existing ones (`rootOrder`).
- **Independence.** The text is independent of the records. It isn't independent of the file metadata in the sense that both come from the same file, so `file+text` cannot show that Calibre's real value is wrong: the file and the text can both describe another volume or another spelling. Replacing a non-empty, non-junk Calibre title, authors list or ISBN therefore needs a record root among the supporters (`record+text` or `record+file+text`). `file+text` alone is accepted only for fills, for junk or placeholder replacements, and for exact title-noise removal (rule 4). The other-work guard (§4) does not cover this.

### 3. Proposals for a field with a text statement
1. **Equivalence first.** There is no proposal when the text value and Calibre's are equivalent under the style guide's "not corrections":
   - titles: `titlesEquivalentV7` (case, "&"/"and", apostrophes, articles), and also the same words once all punctuation is dropped and straight and curly quotes and the dash variants are folded (ADR-031 Title 4). It is skipped when Calibre's title minus exactly the noise `titleNoiseReasons` detects equals the text title: `titlesEquivalentV7` ignores a trailing parenthetical, so without this skip §3.4 could never be reached for series or edition noise. Such a title goes through rule 4;
   - authors: `authorsConsistentV7`;
   - publishers: equal once trailing corporate suffixes are dropped (Inc., Ltd, LLC, GmbH, S.A., S.L., Co., Corp., Company, Books, Publishing, Publishers, Publishing Group, Verlag) and case, punctuation and spacing are folded and "&" reads as "and";
   - ISBN: the same ISBN-13.

   Here a loose equivalence only removes proposals. The same equivalence also decides dissent under rule 3, though, so it can make a text-alone proposal possible: an empty Calibre publisher, "Acme Books" in the file and a record, and "Acme" printed gives the text's "Acme" with the path `text` alone, where v7 proposed "Acme Books" from `file+record`. The printed imprint is the style guide's value (ADR-031 Publisher 1), and the fix still goes to the inbox.
2. **Two roots.** Exactly as the field rule (ADR-020) requires. Another root states the same key (the file, or a record root that isn't recorded-only), and no root with authority states another key. Agreement is exact on the keys: no v7 tolerance proposes. A real Calibre value is replaced only with a record among the supporters (§2). A publisher is never replaced this way: a text-rooted publisher proposal fills an empty value or replaces a junk one, nothing else (ADR-031, Publisher 2 and 3). For an ISBN, a record root supports the text ISBN only when the record is for that ISBN and matches the book the way v6's ISBN proposal requires: the file's exact title key and `strictMatcher` together.
3. **One root** (design §6.2). The text alone proposes only when **both** of these hold:
   - Calibre's value is empty or junk:
     - title: `junkTitleReason(title)` is not empty (word_artefact, file_extension, untitled, number_only). `title_noise` is not junk: noise needs two roots (rule 4);
     - authors: empty, or a single name from the text-rule placeholder list. That list is the other-work list (`placeholderAuthorNames`, ADR-035) without "anonymous", "anónimo", "anonimo", "various" and "varios", because those can be real attributions. The other-work guard keeps the full list;
     - publisher: empty, or `junkPublisherReason(publisher)` is not empty (placeholder, website, scanner_site);
     - isbn: Calibre has no ISBN identifier. The text-alone ISBN also needs the value to be labelled `ebook`, or to be the only ISBN printed outside the back units at all, labelled or not: a series, set or audiobook ISBN can sit beside the book's own.
   - No root dissents. A root dissents only when **all** of these hold:
     - it may back a fix: it is not recorded-only (Wikidata, Hardcover) and not a record found only through Calibre's ISBN or a PDF hint;
     - its value for the field is neither empty nor junk under the detector above (the file's own metadata often carries the very junk Calibre has, such as the publisher "calibre", and that must not block the fix);
     - its value differs from the text value under the equivalence of rule 1. For authors the text list must cover the root's list: a root naming more people than the text dissents.

   A root's junk value never blocks a single-source proposal, but it still vetoes a two-root one: in a two-root proposal a root with authority that states another key vetoes, junk or not. A junk file value therefore sends a fix that a record and the text would support back to the text-alone path.

   The path is `text` alone. The fix kind stays as `FixKind` has it: `fill` when Calibre's value is empty, otherwise `replace`.
4. **Title noise** (amends ADR-026). A title may get shorter only when all of these hold:
   - `titleNoiseReasons` finds noise in Calibre's title;
   - the text title equals Calibre's title with exactly that noise removed;
   - a second root agrees with the text (rule 2).

   A subtitle is never dropped: if Calibre's title is the text title plus `: X` and `X` isn't noise, there is no proposal. More generally, a text title whose words are a strict subsequence of Calibre's title (words dropped from anywhere, as in "Dune, Book Two: Messiah" to "Dune: Messiah") is a shortening and is refused unless it is this exact noise removal. A junk Calibre title may shorten.
5. **The text wins over records in silence, never in contradiction.** A record-only proposal (v7 rules) remains only for a field with no text statement. Where a text statement exists and the roots can't agree, the result is a finding with reason `text_roots_disagree` (no tier effect) and no proposal.

### 4. Other work
From v8, the other-work guard (ADR-035) also fires when the text title and the text authors **both** differ from Calibre's, after the equivalences of §3.1. It doesn't fire when Calibre's title is junk or its authors are a placeholder, since those cases are fills. The placeholder test here is ADR-035's full list (`placeholderAuthorNames`, which includes "anonymous" and "various"), not the smaller list of §3.3: a book Calibre calls "Anonymous" is a fill candidate, never proof of another work. As in ADR-035's guard, it does not fire when Calibre's authors share a surname with the text authors. When it fires, the result is a finding, tier C and no proposals, exactly as before.

### 5. Re-verification at the point of use
Amends M4 §8: `decide.Open` gets no loader.
- **Why it doesn't need one.** The seal already binds the claims, the agreed facts, and the hashes of the excerpt and the raw answers. `verify-seals` re-runs the verifier nightly.
- **The extra check.** Approving or applying a fix whose roots include `text` re-runs that book's extraction verification, the same check `verify-seals` runs, and refuses on any mismatch. It fails closed: an extraction sealed under another verifier or prompt version can't be re-derived by this build, so it can't back an approval until the book is extracted again. `verify-seals` only reports such blocks; this check refuses them, with its own message ("the printed text was read by an older verifier; recheck the book") so that it can't be mistaken for tampering. At apply, a refusal blocks only the book's text-rooted fields, with the reason `text_unverified` or `text_old_verifier`; its other approved fields still apply.

### 6. Not in v8
- Wikidata and Hardcover stay recorded-only.
- Translators and editors are not removed from authors.
- Languages and series get no text statement.
- This edition's year (`pubdate`): a year-only write needs its own rule. Calibre stores full dates, so a printed year can't be written without inventing a day and month (ADR-031). Pubdate proposals stay as in v7.

Each of these needs its own measurement.

## Consequences
- **Replay.** Decisions v1–v7 replay byte-identically, and the goldens prove it. A package sealed at v7 keeps its v7 decision.
- **Measured before deploy.** `bookwarden eval --run 14 --version 8 --baseline 7` reports the proposals gained and lost per fix type. Felix sees those counts before v8 is deployed.
- **The inbox is the gate** (design §2 decision 3):
  - every v8 fix type starts with no labels;
  - nothing applies on its own;
  - a type whose decisions show poor precision is muted.
- **The copyright-page ISBN.** Up to v7 an ISBN taken from a copyright page is never proposed (ADR-035 part 2). From v8 the agreed printed ISBN of §1 may be proposed under rule 2 or rule 3, because the extraction, not the page-text hint, states it.
- **Accepted with this ADR:** the M4 verifier rules (§5, v3), as the text root depends on them.

## Decision v9 (2026-11-08)
Production run 15 (the first v8 run) showed two defects. v9 fixes both. Neither changes a key, a tier, a reason, an equivalence or a dissent decision, and v1 to v8 replay unchanged.

### 1. A pubdate proposal that only shifts the time zone
Calibre stores a date picked at local midnight as UTC: `2005-03-24T23:00:00+00:00` is 25 March in Berlin. `findings.DateKey` takes the date in the string's own offset (for a `+00:00` timestamp, the UTC date), so Calibre's key is 2005-03-24, and a file or record saying `2005-03-25` proposed "2005-03-24T23:00:00+00:00 to 2005-03-25", which changes nothing. Run 15 had 11 of these among 40 pubdate proposals.

From v9, the pubdate rule drops a proposal for a date D when the local midnight of D, read as UTC, lies from 12h before to 14h after Calibre's timestamp: (D at 00:00Z) - timestamp is in [-12h, +14h]. `2005-03-24T23:00Z` against 2005-03-25 is +1h, so there is no proposal. The timestamp is first normalised to UTC. Its own UTC date also counts as equal, so `2005-03-25T01:00:00+02:00` (23:00Z on the 24th) against 2005-03-24 gives no proposal either. This only removes proposals.
- A Calibre value with no time of day (`2005-03-24`), or one that isn't a valid timestamp, is exact, as before.
- `2005-03-24T00:00Z` against 2005-03-23 is -24h, and `2005-03-24T05:00Z` against 2005-03-23 is -29h: both are real differences and propose as before.
- `findings.DateKey` and the `date_mismatch` finding are unchanged: findings keep their own version.

### 2. A text-rooted publisher printed in capitals
The text root proposes the printed imprint as printed, so run 15 proposed "VINTAGE BOOKS", "HYPERION PRESS, INC." and "ALFRED A. KNOPF" over empty publishers. ADR-031 Publisher 1 says a publisher is written "as printed"; v9 changes the case of what is shown, because a house name printed in capitals for the cover is a typographic choice, not its name, and the capitals would be written into the library as they are. From v9, when the displayed value of a publisher proposal comes from the `text` root:
1. If another supporting root (the file or a record) states the same key, its spelling is shown. This takes effect when the text proposes alone because a root vetoed the two-root path (for example a record with a junk publisher) while the file states the same key. When two roots support the proposal, the supporter's spelling was already shown.
2. Otherwise, if the printed value has no lowercase letter, it is title-cased word by word. The case changes and nothing else: no period or comma is added or removed, so the displayed value has the same key as the printed one.
   - The first letter that is a letter is upper-case and the rest lower-case, also after a hyphen. After a single letter and an apostrophe the next letter is upper-case (O'Reilly, L'École), but not in "Martin's".
   - A word with no vowel (a, e, i, o, u, y or an accented vowel) stays upper-case: DK, CRC, BBC, DC, KG. A word with a bracket, such as "(USA)", stays as printed.
   - A word with periods stays as printed when every part between the periods is at most one letter (A., U.S.); otherwise it is title-cased ("St.", "Bros.").
   - Corporate forms get a fixed case: Inc, Ltd, LLC, Co, GmbH, AG, KG.
   - The small words of, and, the, for, on, in, at, to, a, an, de, la, las, los, del, el, y, e, du, des, et, le, les, der, die, das, und, von, zu, di, da, il are lower-case except as the first word.
   - So "HYPERION PRESS, INC." becomes "Hyperion Press, Inc." and "THE CATHOLIC UNIVERSITY OF AMERICA PRESS" becomes "The Catholic University of America Press".
3. A value with any lowercase letter stays as printed.

Only the displayed proposed value changes. The key, the roots and every equivalence and dissent decision are the same.

### Re-verification
Approving or applying a text fix re-verifies the extraction (section 5) and writes the value of the sealed decision. Nothing in `textCheck` or `internal/fix` compares a proposed value with a grounded quote, so the title-cased value, which is not a quote, is not refused. The extraction and its claims are unchanged. A decision sealed at v8 keeps its v8 value; a v9 seal carries the title-cased one.

### Not in v9
Wikidata and Hardcover stay recorded-only (section 6), and the pubdate still gets no text value.

## Decision v10
> Amended by [ADR-045](ADR-045-decision-v10-text-root-corrections.md): from decision version 10, author dissent in the text-alone path also needs the lists to be the same people (§3.3), a text-alone title, author list or publisher in capitals takes another root's spelling or is not proposed (replacing v9's title-casing), a pubdate Calibre would shift is not proposed, a record speaks about the title and authors only for the same book in the same language, a record's own subtitle field joins its title in the text path, and a printed title ending in ":", "?" or "!" joins its subtitle with a space.

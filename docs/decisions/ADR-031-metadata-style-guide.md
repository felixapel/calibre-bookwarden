# ADR-031: A metadata style guide defines the value every field should have

- Status: Accepted (2026-10-29, when `AGENTS.md` made it binding)
- Date: 2026-10-29
- Decision: Felix's (the rules restate his decisions on runs 4, 5 and 7; he answered Q1–Q4 on 2026-10-29)

## Context

A proposal can only be judged correct against a target, and Bookwarden has never written one down.

On check runs 4, 5 and 7 most rejected proposals were not misreadings. They were policy:
- a parent company replacing an imprint (Vintage → Knopf Doubleday);
- an editor, compiler or translator proposed as the author;
- a subtitle dropped;
- a stylistic variant ("&" vs "and", a comma before a year range) proposed as a correction;
- a print ISBN proposed over an e-book ISBN for the same work.

Grounding a value in the book's text can't catch any of these. A rule has to say which value is right.

This guide is that rule. Every proposal rule, detector and extraction prompt cites the section it implements, and every review decision is made against it (the M3 gold set was dropped on 2026-11-04).

## Decision

The rules apply to Calibre's fields. Examples are public books, never entries from a real library.

### Title
1. **What the title is.** The title is the work's title as printed on the title page, followed by its subtitle: `Main: Subtitle`. The subtitle is kept, so a proposal never drops one (ADR-026).
2. **What gets removed.** Noise added by catalogues or files is removed:
   - publisher lines and imprint series ("Signet Books", "(Penguin Classics)");
   - translator or edition credits ("Translated by …", "20th Anniversary Edition" when it isn't part of the printed title);
   - narrative-series markers ("(Dune Chronicles #1)"), which move to the series fields;
   - author-name prefixes ("Mann, Thomas - …").
3. **What gets kept:** volume and part designations ("Vol. 2", "Book One") that identify *this* volume.
4. **Not corrections:** case, punctuation, "&" vs "and", straight vs curly quotes, and a leading article present or absent. These are never proposed.
5. **Language:** the title is in the language of the text, as printed. A translation keeps its translated title.

### Authors
1. **Who goes in Authors.** Authors are the people who wrote the text, in the order printed.
2. **Who doesn't:** editors, compilers, translators, illustrators, and writers of introductions or forewords are not authors of a single-author work.
3. **Collections with no author:** for anthologies and edited volumes with no author, the editors go in Authors (Q3).
4. **Name parts that stay:** suffixes ("Jr.", "III"), particles ("von", "de") and diacritics ("Søren Kierkegaard") are part of the name.
5. **Name form.** One canonical form per person across the library, backed by an authority record (Wikidata, LoC, VIAF) and keeping diacritics, e.g. "Fyodor Dostoevsky" for every edition even where a translation prints "Dostoyevsky" (Q4).
6. **Several people, several entries.** Several people are several entries; one entry never packs several names ("A & B", "A; B").

### Author sort and title sort
Calibre's own rules apply to Calibre's own record (ADR-028). They are never hand-tuned.

### Publisher
1. **Imprint, as printed.** The publisher is the imprint printed on the title page or copyright page (Vintage, Signet, Tor).
2. **Not corrections:**
   - a parent company or group (Knopf Doubleday, Penguin Random House);
   - a corporate suffix ("Inc.", "Ltd", "Publishing Group");
   - a regional label variant ("Penguin UK" vs "Penguin Books Ltd").
   None of these is ever proposed over the imprint.
3. **When to replace.** A publisher is replaced only when it names a different edition's publisher, or junk ("Unknown", a scanner or website name).

### Published date
This edition's publication year, as printed on its copyright page (Q2). In any case no day or month is invented. A year-only source gives a year-only proposal, and the spec's day rules still hold.

### Languages
The language of the text in this file. A translation is in the language it was translated into; the original language is not added.

### Identifiers (ISBN)
1. **Which ISBN.** The ISBN of the edition the file is, as printed in it: the e-book ISBN when the copyright page names one, otherwise the ISBN printed in the file (Q1).
2. **Validity.** An ISBN must be checksum-valid and belong to this work. An ISBN of a different volume of a multi-volume work is wrong, even if the titles match.
3. **Other identifiers** (goodreads, amazon, …) are kept as they are.

### Series and series index
1. **What counts as a series.** A series is a narrative or authorial series (The Expanse, Discworld). Publisher imprint lines (Penguin Classics, Oxford World's Classics, Everyman's Library) are not series.
2. **Index.** The index is the position the author or publisher gives. Novellas between books may be fractional (2.5). Without an established position, a series has no index proposal.
3. **Omnibus volumes** get no series index unless the series itself numbers the omnibus.

### Tags
1. **Vocabulary.** Tags come only from a small taxonomy that Felix approves before M5, mapped deterministically from subject codes (Thema/BISAC).
2. **Fallback.** A book with no confident mapping keeps its tags.
3. **Never removed automatically.** Bookwarden never removes a tag.

### Comments (description)
1. **Fill empty only.** Comments are filled only when empty, from a verbatim description in an openly licensed source (Open Library, DNB, BNE).
2. **Format.** Written as escaped plain text.
3. **Never replaced.** An existing description is never replaced automatically.

### Cover
The cover is the cover of this edition. A replacement is proposed only when it matches the cover embedded in the file (perceptual hash) or is the file's own cover. Replacements are always reviewed by hand (ADR-037).

## Felix's answers (2026-10-29)

- **Q1. Which ISBN for an e-book?** The ISBN of the edition the file is, as printed in it. He did not choose "always the print ISBN" or "keep Calibre's whenever it is valid".
- **Q2. What does Published mean?** This edition's publication year, not the work's first publication.
- **Q3. Edited volumes.** Editors go in Authors only when the work has no author. He did not choose "Various" or "editors never".
- **Q4. Author name form.** One canonical, authority-backed form per person, not the name as printed on each book.

## Consequences

- Every proposal rule, detector, extraction prompt and recall-sample answer cites a section here. Where they disagree with this guide, the code is wrong.
- A change to this guide is a new decision. Labels gathered under the old rule are replayed (ADR-034) and only carry over where the answer is unchanged.
- The style variants in Title 4 and Publisher 2 never count as fixes. Today's rules already reject most of them; findings v5 adds detectors for the noise in Title 2.

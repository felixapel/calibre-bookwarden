# ADR-046: Extract v4: the verifier folds case, dashes and invisible characters

- Status: Accepted
- Date: 2026-11-24
- Amends: the [M4 design](../superpowers/specs/2026-11-02-m4-grounded-extraction-design.md) §5.2 (the transforms) and §7 (contributor lists); [ADR-036](ADR-036-text-root.md) §5 applies as written

## Context

The verifier (M4 §5) grounds a model's value only when it equals what a
closed transform makes of the quote. A model that writes "The Hobbit" for a
printed "THE HOBBIT", a hyphen for a printed en dash, or leaves out a
zero-width space was refused as `value_not_derived`, although the book
prints exactly those words. The reverify reason table (plan v4 C4, run 32,
after the fix in #161) counted these refusals as `case_only`:

- names: 1,499 values in 483 books;
- titles: 66 values in 43 books;
- imprints: 63 values in 44 books;
- subtitles: 11 values in 9 books;
- editions: 3 values in 3 books.

It also counted ISBNs whose groups are separated by an em dash or another
dash variant `findings.ScanISBNs` doesn't read: 310 values in 66 books. Names
with a dash variant counted only 2 values in 1 book, and no field had a
refusal for a format character. Q9 of plan v4 sets the bar for this version:
at least 100 books must gain an agreed title, author or ISBN.

Separately, `Agree` compared contributor lists with duplicates. An extractor
that quoted the same person on the cover and on the title page disagreed with
one that quoted the title page only (audit Q3-B).

## Decision

`extract.Version` is 4.

### 1. The comparison folds; the value is the print

Each fold applies only where a field's transform compares the model's value
with the print. What is derived, sealed and proposed is still the printed
text, cut by the offsets: never the model's spelling.

- **Title, subtitle, imprint.** The value must equal the whole trimmed quote.
  Both sides are compared with three folds:
  - letter case, by simple lower-case mapping;
  - the dash variants U+2010–U+2015 and U+2212, folded to "-";
  - U+200B, U+FEFF and U+2060, removed.

  The sealed value is the trimmed print with those three invisible characters
  removed. One right before a combining mark stays, as the soft hyphen does in
  the normaliser: dropping it would join the mark to the letter before it.
  That one, and any other format character (bidi controls, for example), still
  refuses the value. The check that a title or imprint doesn't run into a role
  line reads each line with the three characters dropped, so an invisible
  character can't hide a by-line ("THE X", then U+200B and "BY JANE DOE").
  The readers around a quote do the same:
  - the role-lead line above a by-line ("Edi", U+200B, "ted");
  - a role phrase ending the line above a name;
  - the lines above a lone name.

  An excluded heading is a heading when either reading of a zero-width space
  (a space, or nothing) makes it one. An invisible character next to a quote
  changes no verdict.
- **Contributor, series name, edition.** Letter case only. The name, series
  or edition phrase is parsed from the print, as in v3, so a list, a role noun
  or part of a name is still refused. Dashes are not folded, for two reasons:
  an em dash between two names would join two people into one, and C4
  counted 2 such values.
- **ISBN.** Every dash variant separates the groups, as a hyphen and an en
  dash already did; the checksum decides. The value is the canonical ISBN-13,
  as before. The qualifier is read from the same folded line.
- **Year, series index.** Digits are unchanged: they have no case. The year's label is read with the three invisible characters dropped, as the role readers are, and a year split by one of them is no year.

The case fold is `strings.ToLower`, not Unicode case folding. So ß against
SS, a final ς against a capital Σ and the long s stay different. Where the
mapping is broader (a dotted capital İ lowers to i), the sealed value is
still the print.

### 2. Still refused

- NFKC equivalents: ligatures, superscripts and compatibility forms.
- Confusables, such as a Cyrillic letter that looks like a Latin one.
- A value that crosses a role line.
- A value found only inside another word.
- A value whose letters differ in anything but case.

`review_test.go` keeps every v3 refusal outside the three folded classes.
`v4_test.go` adds the adversarial cases.

### 3. Agreement

For contributors and translators, a source's list keeps the first occurrence
of each (name, role) in text order. One person quoted twice is one
contributor (Q3-B). Two lists with different people still disagree.

Two limits are accepted:

- **Exact duplicates only.** A cover in capitals and a title page in mixed
  case are two prints, "JANE DOE" and "Jane Doe", so a source quoting both
  still disagrees with one quoting the title page only.
- **First place wins.** When a source quotes a name on the cover and again on
  the title page after another name, the cover's place sets the order.

Agreement compares prints exactly (M4 §7). Comparing them case-insensitively
would also need a rule for which print is sealed; that is left for later.

### 4. Capitals and decision v10

A value that verifies only through the case fold is the print. So an
all-caps print is still all caps in the seal, and decision v10's rule 2
(ADR-045) decides whether it may be proposed: a text-alone value in capitals
takes another root's spelling, or is not proposed.

### 5. Packages sealed under v3

- **No model calls.** Cached raw answers are re-verified under v4 without
  asking a model again. The extraction cache key is the excerpt, the prompt
  version, the host, the model and the effort; it does not contain
  `extract.Version`.
- **Approving and applying.** Until a book is re-verified, approving or
  applying a text-rooted fix sealed under v3 is refused, with "the printed
  text was read by an older verifier" (ADR-036 §5). Deploy v4 after the
  approved text fixes of the open inbox are applied, or they wait for the next
  full run.

## Consequences

- More free-form fields agree; the counts are measured by
  `bookwarden eval --run N --extraction --reverify` on a run sealed under v3,
  before the deploy.
- The reverify reasons (C4) treat what v4 folds as no reason: a value that
  matches with those folds was refused for something else (`other`).
  `case_only` stays in the report for older runs.
- `FuzzVerify` checks that a grounded free-form value is a part of the print,
  the invisible characters dropped, whatever the model wrote.

# Bookwarden v2: content-grounded, earned automation (design spec)

- **Status:** Proposed. Felix approved the master plan on 2026-10-29. This document records the rule changes and the roadmap. Each rule change takes effect only when the PR carrying its ADR is merged.
- **Date:** 2026-10-29
- **Amends:** [the Go rewrite spec](2026-10-12-bookwarden-go-rewrite-design.md), sections 2, 6, 7, 12 and 18, and ADR-017, 018, 020, 021, 026, 027 and 029, as listed below. Everything not listed stays as it is.

## 1. Why

Bookwarden should check every book's own contents and work out the truest metadata, then fix the library with as much automation as can be trusted. Automation that is sometimes wrong is worse than none.

Measured on check run 7 (2026-10-28, 3,196 books, decision v5):

- **Proposals:** only 19 were open.
- **Precision:** field proposals were about 43 % correct (13 of 30 judged). The misses were mostly about policy and identity: an editor credited as author, an imprint replaced by its parent company, a dropped subtitle, the wrong edition. They were not parsing errors. Sort fixes, which restate Calibre's own rules, were about 99.8 % correct.
- **Recall is low by construction:**
  - The title and copyright pages are never read as text.
  - ISBN hints come only from PDFs.
  - OCR is English-only.
  - Sources take an ISBN, never a title.
  - `decide` only uses records for the ISBN that the files' metadata states.

## 2. Goals and anti-goals

**Goals**

1. Every value Bookwarden proposes is backed by the book's own printed text or by independent catalogue records, and a reviewer can see that evidence.
2. A fix type is applied automatically only once it has *earned* it. That means at least 598 blind, individually checked outcomes with zero misses, which is a one-sided 95 % Clopper-Pearson lower bound of 99.5 % precision. After that, it keeps being audited. *Amended 2026-11-04: v1.0 is review-first, and nothing applies on its own ([v1.0 production design](2026-11-04-bookwarden-v1-production-design.md) §2).*
3. Everything that hasn't earned automation goes to a fast, blind review inbox. *(In v1.0, that is everything.)*
4. Scope grows to series and series index, covers, author names, tags and comments. Merges stay manual, because Calibre's content server can't merge.

**Anti-goals**

- An LLM is never the source of a value. A model may point at text in the book. Code then checks that the quoted text is really there and derives the value from it. Agent verdicts stay a veto (ADR-029); since 2026-11-04 the panel only judges in shadow.
- No automation without measurement. A fix type with too few checked outcomes stays manual, however good it looks.
- Nothing is written by sending book contents anywhere other than the configured model providers, and never whole books.

## 3. Rule changes

| Today | v2 | Record |
|---|---|---|
| "Truest" is undefined | A metadata style guide, approved by Felix, defines the target value of every field | ADR-031 |
| The package holds no book text (spec §6) | Bounded front-matter excerpts (at most 12k characters per format) live in a private `excerpts` table keyed by SHA-256. The package seals only their hashes, lengths and unit maps (spine position or page, landmark, rune offsets), plus each found ISBN's context (landmark and label). Excerpts never go into logs or git. Decided in ADR-032 part 2: EPUB front matter and PDF text layers, never OCR text. | ADR-032 |
| ISBN hints come from PDFs only, with English OCR (ADR-018) | Labelled ISBNs also come from EPUB front matter and from PDF pages 1–12 plus the last 3. OCR runs in the book's language. Hints still seed lookups and are never authority. | ADR-032 |
| Seats only; private and HTTP endpoints refused | A provider layer with endpoints set by env only. Private or plain-HTTP endpoints need an exact-CIDR opt-in. A quota state per provider; budgets come with the extractors in M4. Roles are three panel seats and two extractors, each from a different model family. | ADR-033 (amends ADR-029) |
| Decisions per run; bulk approval counts; verdicts shown | Blind, individual decisions become labels. Decisions carry across runs. Certification, audits and circuit breakers gate automatic application per fix type (v1.0: nothing applies on its own, design 2026-11-04). | ADR-034 (amends ADR-021, ADR-029) |
| Identity comes only from file metadata (decision v5) | Decision v6: with no metadata ISBN, one unambiguous copyright or title page ISBN (the e-book's when the page labels one) plus an ADR-030 title match also establishes identity; it only selects whose records are read. If the identity's records agree on a title and authors that both differ from Calibre's (unless Calibre's authors are empty or a placeholder such as "Unknown"), the result is an "other work" finding (tier C) with no proposals. | ADR-035 |
| Sources: DNB, BNE, Open Library and Google Books, by exact ISBN | More exact-ISBN sources for English editions: Wikidata (P212) and Hardcover. | ADR-035 |
| OCR and aggregators are never authority (spec §7) | Unchanged. Decision v8 (first planned as v7, which became ADR-030's tolerances) adds a **text root** that only EPUB XHTML front matter can supply. Each value must be a verbatim quote that code has verified, and two independent extractors must agree on it: a deterministic parser plus an LLM for patterned fields, or two LLM families for free text. PDF text layers and OCR stay hints. | ADR-036 |
| A title proposal never shortens (ADR-026) | An atomic title + series + index proposal may remove a series marker from the title. | ADR-036 |
| An authors proposal never drops a name (ADR-027) | A translator or editor may be removed when the front matter and a source both show that role. | ADR-036 |
| Not written: covers, series, tags, comments (spec §18) | Series and index are written by proposal. Covers, tags and comments are manual or fill-empty only, from verbatim CC0 descriptions or a deterministic subject mapping. Author renames stay manual. | ADR-037 |
| Every report page re-verifies every seal of the run (ADR-017) | A verified projection: seals are verified at seal time and in a nightly sweep, and always on Approve, Apply and agent paths. | ADR-038 |
| No scheduled runs (spec §18) | A daily incremental run that reuses unchanged file evidence, plus a weekly full run. | ADR-039 |
| Exact ISBN only (spec §12) | Last, and only for books with no ISBN anywhere: a work-level title and author search. It can support title, authors, series and index, never ISBN, publisher, date or cover, and it needs identifier linkage. | ADR-040 |
| The agent VM is not a client (spec §2) | An OpenAI-compatible proxy on the LAN may be configured as an LLM provider. Bookwarden stays the client. | ADR-033 |

## 4. Pipeline

```
stage 1 — the 4 check workers, as today:
  snapshot → export each format → inspect (+ front-matter excerpt, + labelled ISBNs)
  → OCR hints in the book's language → cover measures → delete the file
  → exact-ISBN lookups, now also seeded by front-matter ISBNs

stage 2 — a bounded pool sized for model latency:
  grounded extraction (v8) → work-level search (last) → findings → decide → second snapshot read → seal

after the run:
  panel veto on open proposals → certified fix types apply → everything else goes to the inbox
  (v1.0: no panel veto and no automatic apply; everything goes to the inbox)
```

- **Extractors are blind to the record.** They never see Calibre's record or the file metadata, so their agreement can't be copying.
- **Exhausted providers pause the run.** When every extractor provider is out of quota, the run waits for a bounded time instead of sealing "unavailable" packages, which would make proposals flap from one run to the next.
- **Seals stay re-derivable.** Raw model outputs and their claims, with offsets, are sealed with the package, and `decide.Open` re-checks every quote against the stored excerpt.

## 5. Certification (ADR-034)

*Amended 2026-11-04: in v1.0 nothing is certified. The labels stay, muting is added, and the gold set becomes a 200-book recall sample ([v1.0 production design](2026-11-04-bookwarden-v1-production-design.md) §6).*

- **Unit:** one fix type is (field, evidence path, fill vs replace). The evidence path is derived from the proposal's sealed roots.
- **Labels:**
  - Only individual decisions made with the panel's verdicts hidden count.
  - Approve counts as correct and reject as a miss. Skip gives no label.
  - Bulk and pre-ticked decisions never count.
- **Bar:** 598 labels with zero misses (one-sided 95 % lower bound ≥ 99.5 %). With one miss the bar rises to about 949.
- **What doesn't certify:** confirmations of current values measure recall and can kill a path, but they never certify changes.
- **Audits after certification:**
  - Blind audits of 10 % of the first 200 automatic fixes per type, then max(2 %, 2 per month).
  - A miss in an audit, or an undo of an automatic fix, trips the type. Its automatic fixes since the last clean audit go back to the inbox.
- **Rule changes:** labelled packages are replayed at the new decision version. Labels whose output is byte-identical carry over. The items that motivated the change are dropped, and at least 100 fresh labels are needed.
- **Gold set:**
  - About 600 stratified books, labelled once, with a 2 % canary rate and 50 items labelled a second time.
  - It measures recall and labelling stability. It doesn't certify, because per-type proposal counts in it are far below 598.
- **Decision point D1:** once shadow runs have measured per-type volumes, Felix decides what happens to types that can never reach 598. The default stays strict: those types remain manual.

## 6. Milestones

| | Content |
|---|---|
| M0 | This spec and the style guide (ADR-031). Spikes: Calibre write behaviour for the new fields, provider throughput, LAN reachability, prompt injection, image size with more OCR languages, label stability, front-matter coverage |
| M1 | Front-matter ISBN hints; blind labels; providers; verified projection; excerpts; style rules as code |
| M2 | Identity v6, more exact-ISBN sources, findings v5 detectors |
| M3 | Gold-set labelling, certifier, audits, `bookwarden eval` (v1.0 keeps only `eval`; the rest is dropped or deferred) |
| M4 | Grounded extraction in shadow, then the v8 text root; panel prompt v2 |
| M5 | Series and index writes; covers, comments and tags (manual or fill-empty); the authors assistant |
| M6 | Web v2: inbox, carry-forward decisions, evidence with quotes, dashboards |
| M7 | Metrics and readiness, scheduler, retention, backups and restore drills, releases with SBOM and signatures, docs |
| M8 | Work-level search for books with no ISBN |

Until ADR-034 lands, no change may make anything apply automatically that doesn't today.

## 7. Unchanged

These don't change:
- content server only;
- writes are tested on `test/calibre` only;
- seals and their re-derivation;
- two independent roots per proposal (amended 2026-11-04: the printed text alone may propose where Calibre's value is empty or junk, [v1.0 production design](2026-11-04-bookwarden-v1-production-design.md) §6.2);
- the stale check, readback and undo;
- fail closed;
- no secrets or library contents in git, logs or fixtures;
- tests never call a real model API (only `-tags live` does);
- one container of at most 100 MB.

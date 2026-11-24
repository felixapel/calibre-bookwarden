# M4: grounded extraction from the book's own front matter (design)

- Status: draft for Felix's approval
- Date: 2026-11-02
- Implements: v2 plan milestone M4 and ADR-036 (row "OCR and aggregators are never authority → a text root from EPUB XHTML front matter", approved in the v2 plan)
- Builds on:
  - ADR-032 (front-matter evidence, excerpts, scan window)
  - ADR-033 (providers)
  - ADR-034 (labels, certification)
  - ADR-031 (style guide)
  - ADR-030 and ADR-035 (identity and match tolerances)

## 1. Why

Runs 10 and 11 make the gap measurable:

- **Few proposals.** About 18 proposals per run, all from exact-ISBN records, in five field types.
- **Many books without records.** 1,778 books have no record at all (`no_record`), and 1,597 have no ISBN.
- **Unused front matter.** Their EPUBs carry a title page and a copyright page: 3,947 EPUB excerpts, about 10.5k characters on average. These state the title, the contributors and their roles, the imprint, the edition and its year, and often the series. Nothing reads them yet.

At this rate no field type comes close to the 598 blind labels that certification needs (ADR-034). M4 is the recall engine. It turns the book's own printed front matter into evidence, with precision kept by code, not by trusting a model.

## 2. Goals, non-goals, anti-goals

**Goals**

- Extract per-field claims from each EPUB excerpt: title, subtitle, contributors with roles, imprint, edition statement, this edition's year, series and index, and ISBNs with qualifiers.
- Every accepted value is a code-verified verbatim quote from the sealed excerpt, turned into a value by a closed transform.
- Run in **shadow** first. Claims are sealed and measured but grant nothing. A later decision version (v8) adds a **text root** only after measurement against gold labels (*amended 2026-11-04:* measured by inbox decisions and a 200-book recall sample instead, §9).
- Deterministic replay: raw model outputs and claims are sealed, so a package re-derives without calling a model.

**Non-goals for M4**

- PDF text layers and OCR stay hint-class. They are not used for the text root.
- No work-level search (that's M8).
- No new write types (that's M5).
- No change to the agent panel's veto role.

**Anti-goals (it fails even if it "works" when...)**

- a value that isn't printed in the book becomes evidence;
- a model sees Calibre's record or the file metadata, so the root would no longer be independent;
- text from "Also by", ads or a praise page becomes a claim about this book;
- a prompt injection in a book changes an accepted value;
- excerpts leave the machine for anything except the configured providers, or reach logs.

## 3. Shape

```
stage 2 (per book, bounded pool, after the existing stage 1):
  for each EPUB format with a complete-or-truncated excerpt:
    deterministic parsers  → parser claims         (always, no network)
    extractor luna (gpt-6-luna, family openai)     → raw JSON → claims   (cached)
    extractor deepseek (deepseek-v4.1-flash, deepseek) → raw JSON → claims (cached)
    extractor glm  (glm-5.3-flash, family zhipu)   → raw JSON → claims   (cached)
    grounding verifier     → each claim: grounded | dropped(reason)
    agreement              → per field: agreed value | disagreement | single
  seal: extraction block in the package (schema 4)
decide ≤ v7: ignores the extraction block (shadow)
eval:  measures agreement, coverage and accuracy against Calibre, records and gold
```

## 4. Extractors

- **Configuration.** Three extractors, all opencode-go models, chosen by the benchmark (§4a) among the opencode models Felix's Bifrost gateway serves (Felix, 2026-11-03: Bifrost, opencode, free models first, then the best three by cost and performance). Felix then put deepseek-v4.1-flash in place of hy3; it was measured once Bifrost's rules were scoped (§4c):

  | Name | Model | Family | Effort | Calls per run |
  |---|---|---|---|---|
  | `luna` | gpt-6-luna | openai | low | 5,000 |
  | `deepseek` | deepseek-v4.1-flash | deepseek | none | 5,000 |
  | `glm` | glm-5.3-flash | zhipu | low (none until 2026-11-04, §4e) | 15,000 |

  - **Where calls go.** `BOOKWARDEN_EXTRACTOR_BIFROST_URL` (e.g. `http://192.0.2.250:8080`, Bifrost's pinned address on the database network) sends every extractor through Bifrost, which holds the opencode key: models are named `opencode-go/<id>`, no key is needed, and a Bifrost virtual key in `BOOKWARDEN_EXTRACTOR_KEY` is sent if set. Plain http needs `BOOKWARDEN_EXTRACTOR_ALLOW_CIDR` with Bifrost's exact address. Without the Bifrost URL, `BOOKWARDEN_EXTRACTOR_KEY` calls opencode-go directly (`https://opencode.ai/zen/go/v1/chat/completions`). The gateway is checked once, under the variables set.
  - **Per extractor**, `BOOKWARDEN_EXTRACTOR_<NAME>_{URL,MODEL,FAMILY,EFFORT,ALLOW_CIDR,MAX_CALLS}` override the defaults; an overriding model id is sent as given.
  - **Session.** opencode refuses calls without an `x-opencode-session` header. Each extractor sends one random id per process, as `x-opencode-session` directly or `x-bf-eh-x-opencode-session` for Bifrost to forward.
  - **The model that answered.** Bifrost's routing rules rerouted some model ids silently (`opencode-go/deepseek-v4.1-flash` matched a `deep` rule and Groq's gpt-oss-120b answered). Since 2026-11-03 every rule skips the providers `opencode-go` and `opencode-zen`; the check stays, as any gateway change can bring it back. An answer whose reported model is not the one asked for (allowing the gateway prefix and a dated snapshot suffix) is refused as `model_mismatch` and not used: agreement and certification are per model.
  - The three families must differ; two extractors of one family is a config error. They may share families with panel seats.
  - **Quota.** opencode-go has a monthly dollar limit, 20 % of it per rolling 5-hour window and 50 % per week. Run 12 showed the window is **per account, not per model** (§4d): all three extractors were cut off in the same second though their spend differed 2.5×, so a cold run also blocks Felix's other opencode-go use (claude-go, opencode reviews) until the window rolls. The limit answers `429` with `Retry-After` in seconds (1,512 in run 12); a Retry-After longer than a retry would wait (a minute), or a wait that would outlast the call's timeout, ends the call as `quota_exhausted` at once and pauses that extractor for the Retry-After (15 minutes when none came).
  - Extraction needs at least two extractors configured.
  - `EFFORT` is the `reasoning_effort` sent.
- **Input.** Only the excerpt text, wrapped in a fresh-nonce data block as in `agent/packet.go`, and the closed instruction "extract what is printed".
  - No Calibre record.
  - No file metadata.
  - No source records.
  - No book id.
- **Output.** Schema-constrained JSON. Use `response_format: json_schema` where the provider supports it; otherwise use strict parsing, and treat a parse failure as `invalid_output`. Per claim:
  - `field`;
  - `value`;
  - `quote`, the verbatim span;
  - `role`, for contributors only.
  - The model is **not** asked for offsets; code finds them (§5).
- **Prompt.** `extract/prompt.go`, with a `PromptVersion` constant. It says:
  - quote exactly;
  - one claim per contributor;
  - roles come from the printed wording;
  - skip anything in "Also by", "Praise for", previews and ads;
  - return empty rather than guess.
- **Cache.** `extraction_cache(key, raw, created_at)`.
  - `key` = sha256(excerpt_sha, prompt_version, provider URL host, model, effort).
  - The raw response is stored and the claims are re-derived from it.
  - An unchanged excerpt costs nothing on later runs. Incremental runs (ADR-039) reuse it naturally.
- **Budgets and failure.**
  - Per-run call caps per extractor: `BOOKWARDEN_EXTRACTOR_<NAME>_MAX_CALLS`. The default comes from the benchmark (§4a), not from the book count.
  - Quota exhaustion or unavailability seals the extraction state as `unavailable` for that format. In shadow this is fine.
  - A `RunWaiting` state is deferred to the text-root step (§9).

## 4a. Benchmark: reasoning effort and call caps

Felix asked for the effort and the cap to be measured, not guessed. `bookwarden extract-bench` (phase 2) runs the real prompt, client and verifier, read-only, and prints counts and rates only, never text.

- **Inputs.**
  - A synthetic set with known truth: generated title and copyright pages covering the hard cases of §5 (translators and editors, ads and "Also by" sections, several ISBNs with qualifiers, reprint lines, series lines, injected instructions). Its accuracy is exact.
  - A seeded random sample of real EPUB excerpts from a stored run. Real text has no truth yet (gold was planned for M3, replaced on 2026-11-04 by inbox decisions and a recall sample), so it gives proxy rates: grounded share, agreement with the parsers on patterned fields, agreement between extractors.
- **Matrix.** Each extractor × each effort it accepts (`none`, `minimal`, `low`, `medium`, `high`, `max`). An effort the provider refuses is recorded as unsupported. One that it accepts but ignores shows as unchanged reasoning tokens.
- **Measured per cell.**
  - Synthetic precision and recall per field. A wrong grounded value counts against precision.
  - Grounded share of claims, and verifier drop reasons.
  - Parser agreement and pairwise extractor agreement.
  - Invalid-output rate, errors by code, latency p50/p95, and input/output/reasoning tokens.
  - Cost per call at opencode's per-token prices (Bifrost's pricing table). Bifrost drops opencode's own per-call `allowance` figure, so the price is the proxy.
- **Choosing the effort.** Per extractor, take the lowest effort whose synthetic precision equals the best cell's and whose grounded share is within 2 points of the best. Precision ranks first, then cost and latency.
- **Choosing the three.** Distinct families; then the lowest cost per grounded fact on real excerpts, with synthetic recall and the quota window as checks.
- **Choosing the cap.** About 80 % of the model's own 5-hour window at the measured cost per call, rounded down. A cold run is about 3,950 calls per extractor; one that hits its cap seals the rest as `unavailable`, and later runs (incremental, cached) finish the backlog.

### 4b. Benchmark results (2026-11-03)

Through Bifrost, prompt `extract-2`, verifier v2. A screen of 9 candidates × 5 efforts × 2 synthetic cases picked each model's efforts; then six candidates at their best effort over 60 synthetic cases and 70 real excerpts from run 11 (two seeds each).

| Model | Effort | Synthetic recall | Real facts per call | Cost per call | µ$ per grounded fact | 80 % of 5-hour window | p50 (real) |
|---|---|---|---|---|---|---|---|
| **gpt-6-luna** | low | 94.0 % | 2.36 | $0.00045 | **191** | 5,333 calls | 3.1–3.8 s |
| **hy3** | none | 92.0 % | 2.06 | $0.00055 | **267** | 17,506 | 3.3–5.8 s |
| **glm-5.3-flash** | none | 95.3 % | 1.96 | $0.00059 | **302** | 16,232 | 4.8–6 s |
| minimax-m3.1-flash | low | 81.3 % | 2.31 | $0.00081 | 351 | 3,934 | 5.6–6.6 s |
| qwen3.8-flash | none | 94.7 % | 1.61 | $0.00061 | 375 | 7,928 | 3–4.3 s |
| claude-haiku-5-5 | medium | 94.7 % | 2.06 | $0.00084 | 410 | 2,844 | 4.1–5.2 s |

- **Precision.** 100 % synthetic precision in every cell, and no trap (ads, "Also by", injected instructions, a translator as author) was ever grounded. Precision is the verifier's: the models only differ in recall and cost.
- **Not measurable.** Every free opencode-zen model answers API clients 429 "Free models work in OpenCode"; DeepSeek models are rerouted by Bifrost's rules; minimax refuses effort `none`.
- **Rejected.** mimo-v2.6-flash and longcat-2.0 ground poorly at every effort; qwen grounds the least on real text; minimax recalls the least on synthetic text; haiku costs the most per fact and its window doesn't fit a cold run.
- **What prompt `extract-2` fixed.** `extract-1` lost recall on every model the same ways: `series_index` sent as a JSON number (now accepted as its literal text), imprints quoted with "©"/"First published by" around them, titles re-cased, roles and labels on fields that have none, authors taken from a © line. glm/none went from 13 dropped or failed claims on 30 cases to 5, and invalid answers from 1–3 per 30 to 0.
- **Real text grounds far less than synthetic: 40–66 % of claims.** A text-free diagnosis (counts and landmarks only, no excerpt text) of 30 real excerpts for gpt-6-luna showed three verifier causes, the same for every model:
  1. **Back-matter copyright pages.** Many EPUBs print the copyright page after the text. `excludedAt` drops every non-ISBN claim in a `back` unit (19 of 106 claims, 13 of them years).
  2. **One-unit ambiguity.** A title or imprint printed alike on the half-title, title and copyright pages is refused as `ambiguous_quote` (16 of 106), though every occurrence derives the same value.
  3. **Contributor and ISBN-qualifier derivations** (about 20 of 106 `value_not_derived`).

  Loosening (1) or (2) is a verifier v3 change with its own precision risk (a preview of another book in the back matter carries its own © line). Verifier v3 does both in narrow forms (§5, §13.3, results in §4c); gold labels measure their precision before the text root (since 2026-11-04: Felix's blind inbox decisions and muting do, v1.0 production design §6.3).

### 4c. DeepSeek and verifier v3 (2026-11-03, after Bifrost's rules were scoped)

Same seeds as §4b (`stage2-1` synthetic, `real-2`: 40 real excerpts of run 11).

- **deepseek-v4.1-flash.** Effort screen: `none` answers in 1.4 s with no reasoning tokens. `low` to `high` spend 900–1,600 reasoning tokens a call and recall no more. On 30 synthetic cases, `none` reached 96.0 % recall and `high` 95.3 %, both at 100 % precision with no trap grounded. On the 40 real excerpts at verifier v2 it grounded 96 facts (2.4 per call), at about $0.00115 a call ($0.30 / $1.20 per M), or about 480 µ$ per grounded fact. That's dearer than luna (191) and glm (302), but it recalls the most and is the fastest. 5,000 calls is about a third of its 5-hour window.
- **Verifier v3 on the 40 real excerpts** (all three at their default efforts):

  | Extractor | Grounded, v2 | Grounded, v3 |
  |---|---|---|
  | luna/low | 66.1 % (115) | 74.6 % (138) |
  | glm/none | 58.0 % (98) | 65.7 % (111) |
  | deepseek/none | 55.8 % (96) | 68.5 % (122) |

  After its review v3 is narrower than first built (§5): any front © line blocks a back page, and only the part from the © line on counts. A first, broader cut measured 80.9 / 65.6 / 63.8 %; run-to-run model variance is a few points. `excluded_section` drops to 1 per model and `ambiguous_quote` to 4–6. What remains is mostly `value_not_derived` (38–45 per 40 calls): contributor and ISBN-qualifier derivations, which v3 does not change. On 60 synthetic cases v3 keeps 100 % precision for all three, with 0 traps grounded and 0 false positives.

### 4d. The first run (run 12, 2026-11-03, deployed 9ffe136)

The first shadow run over the whole library: 3,207 books, 3,953 formats with an excerpt, 0 broken seals.

- **Spend.** 3,729 formats answered by all three extractors in 88 minutes: 37.2M input and 2.5M output tokens, about **$8.45** at list prices (deepseek $4.39, glm $2.31, luna $1.75), $0.0023 per format. A cold run of the whole library is about $9.
- **The limit.** At 22:42 opencode-go's usage limit answered every model `429 GoUsageLimitError`, `Retry-After: 1512`. The seat capped each wait at a minute and retried three times, so every call ran into its two-minute timeout first: the remaining 199 formats sealed `timeout` for all three extractors, the quota was never recorded, and the last 219 books took 99 minutes. The fix (§4 Quota) ends such a call as `quota_exhausted` and pauses the extractor; formats that weren't answered are asked again next run, since only answers are cached.
- **Outcomes** (`eval --extraction`, coverage of the 3,754 answered formats; agreement = agreed / (agreed + single + disagree)):

  | key | agreed | coverage | agreement | agreed vs Calibre: match / differ / none |
  |---|---|---|---|---|
  | title | 2,282 | 77.6 % | 78.3 % | 1,732 / 550 / 0 |
  | imprint | 1,352 | 53.8 % | 66.9 % | 420 / 823 / 109 |
  | year/copyright | 1,156 | 53.8 % | 57.2 % | — |
  | isbn | 894 | 42.1 % | 56.5 % | 414 / 283 / 197 |
  | subtitle | 678 | 25.0 % | 72.4 % | — |
  | year/first_published | 596 | 23.0 % | 68.9 % | — |
  | contributor | 548 | 22.3 % | 65.6 % | 180 / 39 / 0 |
  | year/this_edition | 303 | 22.5 % | 35.9 % | 106 / 197 / 0 |
  | translator | 232 | 12.3 % | 50.2 % | — |
  | edition | 198 | 6.5 % | 81.1 % | — |
  | series_index | 8 | 1.5 % | 14.3 % | — |
  | series | 7 | 0.2 % | 77.8 % | — |

  Contributors are compared with Calibre's authors only, so their match / differ / none counts the agreed author-role contributors (219 of 548). "Differ" is not "wrong": imprint vs Calibre's publisher, a printing year vs Calibre's pubdate and a print ISBN vs an ebook ISBN all differ by the style guide's own rules. Which side is right is what Felix's inbox decisions and the recall sample measure (v1.0 production design §6).

### 4e. glm moves to effort low (2026-11-04)

From the morning of 2026-11-04, opencode-go's glm-5.3-flash upstream refused `reasoning_effort: none`. It answered `400` "[1210] This model always engages in thinking and cannot be disabled; please use low, high, or max", or "model does not support disabling reasoning". Run 12 saw none of these errors. In run 13 they failed 61 of 207 fresh glm calls, which sealed as `unavailable`. Text-free probes then failed 9 of 12 at `none` and 0 of 12 at `low`.

| seed | effort | calls | errors | P | R | reasoning tokens / call |
|---|---|---|---|---|---|---|
| stage2-1 | low | 30 | 0 | 100.0 % | 94.7 % | 30 |
| stage2-1 | none | 30 | 1 | 100.0 % | 89.3 % | 26 |
| bench-1 | low | 30 | 0 | 100.0 % | 93.3 % | 34 |
| bench-1 | none | 30 | 10 | 98.9 % | 62.7 % | 0 |

- `low` recalls about what `none` did on 2026-11-03 (95.3 %), with no trap grounded.
- It spends about 30 reasoning tokens a call, so the cost per call is about the same.
- The effort is part of the cache key, so the next run asks glm again for every format: about 4,000 calls, roughly $2.5.
- In bench-1, glm/none grounded one wrong title, the bench's first false positive. It is a grounded value that differs from the case's truth. It came from a single extractor, and a title needs two families to agree, so agreement would not have accepted it alone.

## 5. The grounding verifier (pure code; built and fuzzed first)

For each claim:

1. **Find the quote.**
   - Normalise both excerpt and quote with the same function: NFC, whitespace runs collapsed to one space, curly quotes and apostrophes folded, soft hyphen removed, line-break hyphenation "-\n" joined.
   - Keep an **offset map** from normalised to original runes.
   - The quote must occur in the normalised excerpt. If it occurs more than once, every occurrence must sit in the same unit (§5.3); otherwise the claim is `ambiguous_quote`. **v3:** for a context-free field (title, subtitle, imprint, edition) occurrences in several units are allowed when every one derives the same value; the claim is placed at the first.
   - The normalisation never changes letters or digits. There is no NFKC, so "²" stays "²".
2. **Derive the value from the quote by a closed, per-field transform.** The claimed `value` must equal the derived value. If it doesn't, the claim is `value_not_derived`. **v4 ([ADR-046](../../decisions/ADR-046-extract-v4-folds.md)):** the comparison folds letter case (title, subtitle, imprint, contributor, series name, edition), dash variants (title, subtitle, imprint, ISBN groups) and U+200B, U+FEFF, U+2060 (title, subtitle, imprint); the derived value is still the print.
   - `title` / `subtitle`: the quote trimmed of surrounding punctuation and whitespace; case is kept.
   - `contributor`: the name inside the quote, after stripping a closed set of role phrases ("by", "translated by", "edited by", "with an introduction by", "illustrated by", and the de/es/fr equivalents) and inverting "Surname, Given".
   - `role`: the role phrase found in the quote, earlier on the same line, or ending the previous line. "by" proves `author` only as a line-initial by-line that no role phrase precedes. A lone name on a title-page unit is `author` only when no role, connector or language line sits between it and the title. Otherwise the claim is `role_unproven`. Phase-1 review (#99) showed how easily a bare "by" grounds translators and imprints as authors; these rules are its fixes.
   - `imprint`: the quote trimmed; a closed list of corporate suffixes is kept, not stripped.
   - `year`: a 4-digit year inside the quote, after ©, "First published", "This edition published", "Published" or "Erstausgabe", and similar closed labels. Which label it follows is recorded (`copyright`, `first_published`, `this_edition`). Q2 of the style guide wants this edition's year.
   - `edition`: the quote matched against a closed edition-statement grammar.
   - `series` / `index`: "Book N of", "Volume N in", "Band N", "#N" and "Nth in the", or a series line on the title page. The index is a decimal.
   - `isbn`: digits plus checksum, with the qualifier from ADR-032's closed qualifier table.
3. **Section filter.**
   - A unit (an excerpt unit from ADR-032 part 2) or a region after a heading line from a closed list is `excluded`. The list includes "Also by", "By the same author", "Other books by", "Praise for", "Coming soon", "Read on", "Excerpt from", "Weitere Titel", "Del mismo autor" and "Du même auteur".
   - A claim whose quote falls in an excluded region is dropped as `excluded_section`.
   - Landmark `back` units are excluded for every field except ISBN. **v3:** the book's own copyright page in a back unit is not. A back unit holds it when it has a copyright line with a year (©, (c) or "Copyright") and a rights line ("All rights reserved", "Alle Rechte vorbehalten", "Todos los derechos reservados", "Reservados todos los derechos", "Tous droits réservés", "Tutti i diritti riservati"), and credits no quoted material ("by permission", "with permission", "words and music", "lyrics"). It is the book's own only when it is the only such back unit, and no other unit has a copyright line with a year, whatever its rights wording, or the copyright landmark: then the book's own page is there, and a back one is another book's. Only the part from the line of its first copyright line on is the page; text above it (an "About the Author") stays excluded, and headings still cut it. The verifier rules are accepted with the text root, in ADR-036 (§9).

All of this is deterministic and needs no network. **Fuzzed:** the normaliser, the offset map, quote search and each transform.

## 6. Deterministic parsers (the second voice for patterned fields)

Closed grammars over the same normalised excerpt:

- ISBN with qualifier: reuse `findings.ScanISBNs` and the ADR-032 context.
- The year lines ("© 2019 by…", "First published in 2003", "This edition published 2021").
- "Translated from the X by Y" and "Translated by Y".
- Series index lines.

Parser claims go through the same verifier, so they get quotes and offsets too.

## 7. Agreement (what a field's sealed outcome is)

Only grounded values count as voices.

| Field class | Agreed when | Otherwise |
|---|---|---|
| Patterned: ISBN with qualifier, year with label, translator, series index | the parser and at least one extractor produce the same value, and no voice grounds a different value | `single` (one voice) or `disagree` |
| Free-form: title, subtitle, contributors with roles, imprint, edition statement | at least two extractors (two families) produce the same value, and no extractor grounds a different value | `single` or `disagree` |

- **No dissent.** A third extractor that grounds something else makes the field `disagree`, even when two agree. An extractor that claims nothing for the field, or fails, is no dissent.
- **Contributors** are compared as ordered lists of (normalised name, role). **v4:** each (name, role) counts once per source, at its first place in the text (ADR-046 §3).
- **Equality** is exact equality of the derived values. No ADR-030 tolerances apply inside extraction: two quotes of the same printed line derive the same value by construction.

## 8. Sealing (schema 4)

`FormatEvidence.Extraction` (optional; schema ≤ 3 packages must not carry it):

- `prompt_version`, `excerpt_sha256`;
- per extractor: `{name, family, model_reported, effort, state, raw_sha256}`, where `state` is one of `ok`, `unavailable`, `quota_exhausted`, `invalid_output` or `timeout`;
- `extract_version`: the verifier's rules version (`extract.Version`). `Open` re-verifies only claims sealed at the same version; a rules change bumps it, the way decide versions do;
- `claims`: a list of `{field, value, role?, label?, source: parser|A|B, unit, start, end, status}`, where `status` is one of `grounded`, `excluded_section`, `ambiguous_quote`, `value_not_derived`, `role_unproven` or `not_found`. `label` is the year label or the ISBN qualifier;
  - No quote text and no non-grounded model value is ever sealed. Offsets into the sealed excerpt are enough to show the quote, and the raw output (private table) holds the rest;
- `agreed`: per field, `{value, voices}` or `{disagree}`.

The rules for raw outputs:

- Raw outputs live in a private table, `extraction_raw(sha256, body)`. Only their hash is sealed, as with excerpt bodies.
- `decide.Open` (from the text-root version on) gets a loader. It **re-runs the verifier** from the sealed excerpt and the stored raw outputs, and checks that the result equals the sealed claims.

## 9. Shadow, then the text root

- **Shadow (M4a).** Extraction runs, is sealed and shows up on the book page:
  - the quote is highlighted in the excerpt view;
  - agreed values appear next to Calibre's.

  Decide ≤ v7 ignores it. `bookwarden eval` gains an extraction report:
  - coverage per field;
  - agreement rates;
  - agreement against Calibre's current value;
  - agreement against exact-ISBN records, where they exist;
  - against the 200-book recall sample (v1.0 production design §6.4, replacing the M3 gold set).
- **Text root (M4b, decision v8, its own PR and ADR-036 acceptance).** An agreed extraction value becomes a root named `text`, independent of the record roots.
  - The two-root rule still holds: `text` plus one record root, or `text` plus the file metadata. *Amended 2026-11-04:* where Calibre's value is empty or junk, `text` alone may propose ([v1.0 production design](2026-11-04-bookwarden-v1-production-design.md) §6.2).
  - The fix types it unlocks go to the inbox, and v1.0 never applies anything on its own. Their precision is measured there, per type.
  - **Prerequisite** *(replaced 2026-11-04)*: no longer ≥ 99 % agreement with a gold set. Text-root fixes go to the inbox. A fix type whose person decisions show poor precision is muted back to a finding, and a 200-book sample measures recall ([v1.0 production design](2026-11-04-bookwarden-v1-production-design.md) §6.3, §6.4).

## 10. Prompt injection and privacy

- The excerpt is untrusted. It is delimited with a fresh nonce, and a marker inside the data fails closed (as in `agent/packet.go`).
- An injection can, at worst, make a model *choose a wrong printed span*. It can't make a value that isn't printed. Two families, the section filter, Felix's blind review of every fix and per-type muting bound the wrong-span risk (gold measurement was the bound until 2026-11-04).
- Spike S4 measures that risk: 50 synthetic front matters containing instructions, ads and several ISBNs. Passing means zero changed accepted values.
- Only excerpts (≤ 12k characters per format) are sent, and only to the two configured extractor endpoints.
  - Excerpts and raw outputs never appear in logs, metrics or errors.
  - A gateway keeps its own logs. Bifrost stores every request's messages and answer (run 12: all 11,187 calls, 365-day retention) unless told otherwise, so calls through it send `x-bf-disable-content-logging: true`. That header takes effect because Bifrost's `allow_per_request_content_storage_override` is on (since 2026-11-04, §13.6). The global `disable_content_logging` would do the same for every client.
  - The README threat model gets a section on this.
  - The local vLLM option keeps everything on the LAN.

## 11. Phasing (each one PR, with the usual gates: TDD, Opus review, green CI)

1. **`extract` verifier and parsers.** Pure code, no network, no schema change:
   - the normaliser with its offset map;
   - quote search;
   - the transforms;
   - the section filter;
   - the parsers.

   Table tests come from synthetic front matter, plus fuzzing. *This can start now, with no keys.*
2. **Extractor providers, the cache and the benchmark.** ADR-033 providers for the three extractors, the prompt, JSON parsing, the cache table, recorded-fixture tests, and `bookwarden extract-bench` (§4a). Live tests use `-tags live`. The benchmark's numbers set the effort and cap defaults.
3. **Stage 2 and schema 4 sealing (shadow).** The pool, budgets, extraction states, `Open` re-verification, the book-page view, and replay goldens (v1–v7 unchanged).
4. **Eval extraction report.**
5. *(Later, after measurement)* **M4b, decision v8 with the text root.**

## 12. What Felix provides

- Two variables in the Unraid template, no key: `BOOKWARDEN_EXTRACTOR_BIFROST_URL=http://192.0.2.250:8080` and `BOOKWARDEN_EXTRACTOR_ALLOW_CIDR=192.0.2.250/32` (Bifrost's address on the database network, pinned since 2026-11-04, §13.6). A Bifrost virtual key in `BOOKWARDEN_EXTRACTOR_KEY` if the gateway requires one.
- Approval of this design, which counts as approval of the M4a scope. M4b comes back with measurements.

## 13. Decisions

1. **Extractors** (Felix, 2026-11-03): through Bifrost, opencode models only, free first, then the best three by cost and performance; a subscription proxy on the agent VM is not used. The benchmark chose gpt-6-luna, hy3 and glm-5.3-flash (§4, §4b).
2. **Effort and call cap** (Felix, 2026-11-03): measured by the benchmark (§4a, §4b), not set by hand.
3. **Verifier v3** (decided 2026-11-03 under Felix's "proceed autonomously"; Felix confirmed 2026-11-04): yes to both, in the narrow forms of §5 (the book's only copyright page in the back matter; context-free values printed alike). Extraction is in shadow, and the text root needs ≥ 99 % agreement with gold on ≥ 300 items per field (§9), so precision is measured before either rule can matter (that gate was replaced on 2026-11-04 by inbox decisions, muting and a recall sample, §9). Results in §4c.
4. **DeepSeek** (Felix, 2026-11-03): deepseek-v4.1-flash replaces hy3, at effort `none` and 5,000 calls per run (§4c).
5. **glm at effort `low`** (2026-11-04, under Felix's "proceed"): its upstream refuses `none` (§4e).
6. **Bifrost** (Felix, 2026-11-04): Bookwarden's calls are not kept in Bifrost's log store. `allow_per_request_content_storage_override` is on, and the content of the 18,854 earlier Bookwarden rows was removed. Bifrost's address on the database network is pinned high in the subnet, so dynamic allocation can't take it after a rebuild; a script on the server re-applies the pin. Virtual keys and inference auth are Felix's to create in Bifrost's UI.

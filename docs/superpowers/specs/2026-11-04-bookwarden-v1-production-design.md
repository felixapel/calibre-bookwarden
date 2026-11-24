# Bookwarden v1.0: production-ready and review-first (design)

- **Status:** Approved by Felix on 2026-11-04 (master plan v3). Each rule change takes effect with the PR that implements it.
- **Date:** 2026-11-04
- **Amends:**
  - [the v2 design](2026-10-29-bookwarden-v2-design.md): §2 goal 2, §5 and §7;
  - [ADR-034](../../decisions/ADR-034-blind-individual-decisions-are-labels.md);
  - [the M4 design](2026-11-02-m4-grounded-extraction-design.md) §9.
  - ADR-029 changes in its own PR, which retires agent mode "on".
  - ADR-020 (the two-root field rule), ADR-026 (titles never shorten) and ADR-035 (a copyright-page ISBN is never proposed) are amended by ADR-036, with decision v8 in Wave 2.
  - Everything else stays as it is.

## 1. Why

The v2 design bet on *earned automation*: a fix type applies on its own once 598 blind, individually checked fixes of that type have zero misses (ADR-034). Thirteen stored runs (2–14) measured whether this library can ever pay for that. All counts are text-free:

| Measure | Value |
|---|---|
| Library | 3,207 books, growing by about 40 a month (3,194 on 2026-10-25, 3,207 on 2026-11-03) |
| Unique fixes ever proposed | 669, 521 of them sort rules. The largest field type has 62 (title, path `file+openlibrary`) |
| Felix's decisions on fixes from catalogue records | authors 0 of 19 approved, publisher 6 of 46, title 6 of 29 |
| Labels (blind, individual decisions) | 0. All 546 approvals were bulk |
| Shadow extraction (run 14), books whose extractors agree on a title | 1,733: 677 identical to Calibre's; 644 that differ only by case, punctuation, "&" or an article (not corrections, ADR-031 Title 4); 272 where Calibre's title has extra text; 1 that lacks part of the printed title; 139 really different |
| Other text candidates (run 14) | ISBN: 167 printed where Calibre has none, 277 different. This edition's year: 141 different. Imprint: 62 where Calibre has no publisher. About 90 junk publishers ("calibre", findings v5). The extraction reads no language; 42 books have none in Calibre |

**What follows:**
- **No fix type here can reach 598.** That includes title sort, which has 496 fixes in total. Reading the books adds an estimated 300–700 field fixes, spread over about 8 types.
- **Automation would save almost nothing.** The backlog is a one-time job, and new books bring about 8 fixes a month. Automatic writes would save about a minute a month, while the measuring machinery is real code with real risk.
- **The record-based fixes were mostly wrong.** The style guide (ADR-031) names the printed front matter as the authority for title, publisher, this edition's year and ISBN, and the record-based fixes failed on exactly those fields.

So v1.0 puts its effort into finding true values in the book itself, and into making Felix's review fast and honest.

## 2. Decisions (Felix, 2026-11-04)

1. **Review-first.**
   - Nothing is written to Calibre without Felix's own decision.
   - Blind individual decisions still become labels (ADR-034 part 1), and `/precision` keeps measuring every fix type.
   - Out of v1.0:
     - the certifier (ADR-034 later part 1);
     - audits and circuit breakers (later parts 2 and 3);
     - agent mode "on" (ADR-029).
   - Pooled certification may come back after v1.0, with real numbers.
2. **One source for empty or junk fields.** Where Calibre's value is empty or junk, the book's printed text alone may propose a value (§6.2). The two-root rule (v2 design §7) holds everywhere else.
3. **No 600-book gold set.**
   - Fixes from the text root go to the inbox as soon as their decision version ships.
   - A fix type whose decisions show poor precision is muted back to a finding (§6.3).
   - A 200-book sample measures what the text root misses (§6.4).
   - This replaces the M4 design's §9 prerequisite: "≥ 99 % agreement with gold on ≥ 300 items per field".
4. **Scope of v1.0.** The core (§3) plus series and series index, cover triage, and a public release.
   - **Amended 2026-11-08 (Felix):** series proposals (decision v12, first planned as v10; renamed by ADR-045, since v10 is the text-root corrections, and again by ADR-048, since v11 is restorable proposals) and the cover triage deck move to after v1.0. Run 16 (the first decision v9 / findings v6 run, 3,212 books) showed series proposals would propose almost nothing, because no root other than the recorded-only ones carries a series, and `cover_better_available` fired for 5 books. The series and cover read paths and their findings stay in v1.0 as a manual worklist.

## 3. What production-ready means (v1.0 exit criteria)

1. **Safety:**
   - 0 broken seals over 14 consecutive scheduled runs.
   - Replay goldens of every decision version are byte-identical in CI.
   - `test/calibre` covers write, readback, the stale check and undo for every writable field. Series, index and cover writes are pinned by spike S1 but have no write path in v1.0 (§2.4 amendment).
2. **Measured quality:**
   - `/precision` shows every fix type with labels, misses, the Clopper-Pearson bound and its mute state.
   - Recall per field, from the 200-book sample, is recorded in this document.
3. **Backlog worked:** fewer than 50 open fixes in unmuted types.
4. **Unattended operation:**
   - A daily incremental run at p95 ≤ 10 minutes, and a weekly full run ≤ 2 hours.
   - A heartbeat to Uptime Kuma alerts on a missed run, a failed run, a broken seal, or more than 5 % failed extractor calls. A forced failure has been shown to alert.
   - The nightly dump is fresh, and a restore drill has passed: `pg_restore` into a scratch database, then `bookwarden verify-seals`.
5. **Delivery:**
   - A `v*` tag produces the image (Gitea registry and GHCR), an SPDX SBOM, a cosign signature and a changelog.
   - `scripts/deploy-unraid.sh` deploys by digest after a pre-migration dump.
   - One rollback has been rehearsed.
6. **Docs:**
   - The README has a quick start with compose and with the Unraid template.
   - An environment reference that a test keeps complete.
   - An operator guide, a threat model with a privacy section, `SECURITY.md` and a `CHANGELOG`.
7. **Security:**
   - Trivy finds no fixable HIGH or CRITICAL issue, and govulncheck and gitleaks are clean.
   - A new install starts with writes off and has no automatic-apply mode.

## 4. Architecture

The framework stays.
- **Kept:**
  - one Go binary in one container, at most 100 MB, non-root, with pinned bases;
  - Postgres 18;
  - Calibre's content server as the only way into the library;
  - Go templates with htmx;
  - LLM calls at one boundary only, extraction, where a model may only point at printed text that code then verifies.
- **Not added:**
  - An agent framework. The model has one bounded job.
  - A vector store. Nothing is retrieved.
  - A learned truth or confidence model. 148 unique field fixes can't train one, and deterministic, versioned, replayable rules stay auditable.
  - Any second service.

```
evidence ─→ claims ─→ truth (decide v8–v12) ─→ review (inbox, blind) ─→ guarded write (stale check, readback, undo)
 Calibre    grounded    text-first,               the only path to
 file OPF   quotes      style-guide equivalence    a write
 excerpts   records     two roots, or one for      labels → /precision → mute
 records                empty/junk fields      ↘ findings: the manual worklist (merges, renames, DRM …)
 covers
ops: scheduler · retention · heartbeat → Kuma · nightly dump + restore drill · release → deploy by digest
```

The layers change in five places:

1. **Truth starts from the text** (decision v8, ADR-036).
   - An agreed extraction value is the `text` root.
   - Records corroborate and establish identity. Where they contradict the printed text, the result is a finding, not a proposal.
   - A record-only proposal for title, authors or publisher remains only where the book has no text value.
2. **Review is the only path to a write.**
   - Agent mode "on" is retired, and the panel judges in shadow only.
   - Its verdicts stay hidden on undecided fixes until Felix chooses to show them (ADR-034), so they are a second opinion, never a writer.
3. **Precision is enforced as well as shown** (§6.3).
4. **New write types** (ADR-037): series and index by proposal, covers through a manual deck.
5. **Delivery and operation are in the repository:**
   - the release workflow;
   - the deploy script;
   - the heartbeat;
   - the restore drill.

## 5. Pipeline and delivery

```
stage 1 (4 workers): snapshot → export → inspect (+excerpt, +ISBN hints, +OPF series, +cover measures)
        → OCR hints → delete the file → exact-ISBN lookups
stage 2 (bounded pool): grounded extraction (cached by excerpt hash) → findings → decide → second snapshot read
        → seal (from v8, Open re-runs the verifier over the stored excerpt and raw answers)
after:  seal check + projection → carried rejections → heartbeat → inbox (nothing applies on its own)
```

- **Schedule:** a daily incremental run, a weekly full one, after the nightly database dump.
- **Releases:** a `v*` tag runs a workflow:
  1. build;
  2. size budget;
  3. Trivy;
  4. smoke test;
  5. SBOM;
  6. push;
  7. signature;
  8. changelog;
  9. Gitea release.
- **Deploys** pin the image by digest and dump the database first. A failure restores the previous template and image. A migration that already ran needs the pre-deploy dump restored, because the binary refuses a database that a newer binary migrated.

## 6. Rules

### 6.1 Review-first
- There is no setting, mode or variable that makes Bookwarden write a fix nobody approved.
- Approvals come only from a person's decision: individual or bulk, with apply as before.
- Labels are unchanged from ADR-034: a blind, individual decision counts; bulk, carried and agent decisions don't.

### 6.2 One source for empty or junk fields
ADR-036 gives the exact rule; this is its summary.
- **When it applies.** A `text` value (M4 §7) may be proposed without a second root only when Calibre's current value for the field is:
  - title: junk by findings v5 (`junkTitleReason`: a word-processor artefact, a file name, "Untitled", a bare number). A title with *noise* (`title_noise`) isn't junk and still needs two roots.
  - publisher: empty, or junk by findings v5 (`junkPublisherReason`: a placeholder such as "calibre" or "Unknown", a website, a scanner site).
  - authors: empty, or a single placeholder name from a closed list ("Unknown" and its translations). The list leaves out "Anonymous" and "Various", which can be real attributions.
  - ISBN: Calibre has no ISBN identifier.
  - Published date isn't in v8. Calibre stores full dates, so a printed year can't be written without inventing a day and month (ADR-031).
- **No dissent.** A root dissents when all of these hold:
  - it may back a fix: it isn't recorded-only like Wikidata or Hardcover, and isn't a record found only through Calibre's ISBN or a PDF hint;
  - its own value is neither empty nor junk under the same detector, so the file metadata carrying the same junk as Calibre doesn't count;
  - its value differs from the text value under ADR-036's equivalences.

  One dissent makes it a finding.
- **Formats.** Every EPUB format that has an answered extraction must give the same text value. A format without an extraction, or whose extractors all failed, is ignored.
- **The usual guards still apply:**
  - the other-work guard;
  - the subtitle rule;
  - the ISBN checksum and qualifier rules: several ISBNs printed with no e-book label are ambiguous and give no proposal.
- **How it shows:**
  - The path is `text`.
  - The kind follows ADR-034: `fill` when Calibre's value is empty, `replace` when it is junk or a placeholder.
  - These are fix types of their own on `/precision`, and always go to the inbox.

### 6.3 Muting
- **The unit.** A fix type is (field, path, fill or replace), counted over all runs of a library.
  - Each unique fix counts once. A fix is the library, the book's uuid, the field, and the current and proposed values. Its latest person decision is the one counted.
  - Individual and bulk decisions count, whether or not the verdicts were shown. Muting only ever hides fixes.
  - Carried and agent decisions don't count. A carried row is a copy, and the panel no longer decides anything.
- **The outcome.** As in ADR-034:
  - approved, applied, stale, blocked and failed count as correct;
  - rejected counts as a miss;
  - an undo of an applied fix also counts as a miss.
- **The rule.** For a type with n ≥ 10 counted fixes and k misses, the one-sided 95 % Clopper-Pearson upper bound on precision is `1 − LowerBound(n, n − k)`. The second term is the lower bound on the miss rate, using `store.LowerBound`. The type is muted when that upper bound is below 50 %.
  - Worked example at n = 10: one approval gives an upper bound of 39.4 %, so the type is muted. Two approvals give 50.7 %, so it isn't.
  - At 0 of 19 the bound is 14.6 %, at 6 of 46 it is 24.1 %, at 6 of 29 it is 36.8 %.
- **What it does:**
  - A muted type's fixes leave the inbox's default view. They stay visible as findings and under a "muted" filter, where they can still be decided like any fix.
  - `/precision` shows the mute and offers an unmute, and both are recorded with who and when.
  - An unmute sticks until 10 more fixes of the type have been decided after it.
- **Where it lives.** Muting is part of the review layer. `decide` and the seals never depend on it.

### 6.4 The recall sample
- **The sample:** 200 books, seeded and stratified by format, language, tier, and whether the book has an open or decided fix. Each stratum's weight is its share of the library.
- **Blind answers.** Felix sees Calibre's value and the book's front-matter excerpt, never Bookwarden's proposal. For each field he answers one of:
  - Calibre's value is right;
  - Calibre's value is wrong, optionally typing the right one;
  - skip.
- **Recall.** Recall for a field is the share of sampled books whose Calibre value Felix called wrong that have a fix for the field. If he typed a value, it must match the fix's proposed value under ADR-036's equivalence. Strata are reweighted by their library share.
  - Skips count nowhere.
  - A book whose Calibre value he called right and that has a fix shows up as a false alarm. Precision itself stays with the inbox decisions.
- **What it is.** The answers are confirmations, not labels: they measure recall and never certify anything. `bookwarden eval` reports recall per field from them.

## 7. Roadmap

Each step is a PR with the usual gates: TDD, an Opus review and green CI. Steps inside a wave are independent.

| Wave | Steps |
|---|---|
| 1 | This design. Retire agent mode "on" (ADR-029 amendment). Run heartbeat. Release workflow with SBOM, signature and Renovate. `scripts/deploy-unraid.sh`. Spike S1: Calibre's series, index and cover writes on `test/calibre`. `eval --extraction --reverify` (re-run the verifier over stored answers, with no model calls), then verifier v4 for contributor and ISBN-qualifier recall |
| 2 | Decision v8, the text root (ADR-036): `Open` re-verifies, a text statement per field, style-guide equivalence, §6.2, title noise, replay-gated with `eval --version 8 --baseline 7`. Inbox v2: diff, highlighted quote, queues per type, muting (§6.3). Series and index read path (schema 5). Cover candidates (the EPUB's own cover, the Open Library cover by exact ISBN) |
| 3 | Deploy v8, then the inbox backlog. The 200-book recall sample. The schedule, heartbeat and restore drill switched on, with a forced `down` to prove the alert |
| 4 | Operator guide, threat model, env reference test, `SECURITY.md`, `CHANGELOG`. A compose example and the CA template on GHCR. A `v2.0.0-rc.1` tag deployed by digest, then a rehearsed rollback, then 14 days of burn-in against §3. `v2.0.0` is tagged only once §3 holds |

"v1.0" names this milestone. Its git tag is `v2.0.0`, because Certificate A, the retired Python version, already used tags up to v1.3.2.

The nightly dump itself is the server's job (a nightly User Script), not the repository's. The restore drill is what checks it.

## 8. Dropped or deferred

- **Out of v1.0:**
  - the per-type certifier;
  - blind audits;
  - circuit breakers;
  - agent mode "on";
  - the 600-book gold-set page;
  - the 2 % canaries.
- **After v1.0:**
  - decision v12 (first planned as v10, then v11; ADR-045, ADR-048), series and index proposals and writes (ADR-037 part 1), deferred 2026-11-08;
  - the cover triage deck (ADR-037 part 2), deferred 2026-11-08;
  - pooled certification;
  - work-level search (ADR-040);
  - tags and comments;
  - the authors assistant (renames stay manual in Calibre);
  - language detection;
  - panel prompt v2;
  - the local vLLM and subscription-proxy providers.

## 9. What Felix provides

- **Calibre:** write permission for the `bookwarden` user once v8 is deployed, then Settings → writes on.
- **Keys and tokens, which only he creates:**
  - a cosign key pair;
  - a GHCR token;
  - optionally, a Renovate token;
  - the Kuma push monitor URL;
  - optionally, a Hardcover token and a Bifrost virtual key.
- **His time:**
  - the inbox backlog, about 20–50 minutes at about 4 s a fix;
  - the recall sample, about 40 minutes.

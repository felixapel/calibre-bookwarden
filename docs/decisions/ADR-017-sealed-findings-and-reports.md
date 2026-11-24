# ADR-017: Version local findings and verify reports against sealed evidence

- Status: Accepted
- Date: 2026-10-13
- Decision: approved in the M2.3 implementation plan
- Amended by [ADR-038](ADR-038-verified-projection.md): pages read a verified projection instead of verifying the whole run on every request; the rules below are the whole-run check's

## Context

M2.2 stores file evidence but does not derive findings. Reports must preserve
historical identity, distinguish a checked clean book from missing evidence,
and detect a damaged projection that would otherwise suppress a filtered row.
Local observations do not grant fix authority; sources, tiers and proposals
remain M3 work.

## Decision

Schema 1 remains the pre-cutover package schema. Optional `findings_version`
and `findings` add versioned local observations. Version 0 means legacy evidence;
version 1 retains the original M2.3 rules; version 2 adds capture completeness.
The original seal is verified before interpreting any version. Sealed findings
are recomputed with their immutable version and compared as ordered tuples.
An omitted empty array and an empty array are equivalent; nonzero versions
explicitly identify a clean result. Legacy findings are derived in memory without
resealing historical bytes. Unknown versions and inconsistent findings fail
closed. Future rule changes require a new version and a historical derivation.

Comparisons preserve accents, punctuation, subtitles and ordered authors while
folding Unicode case and whitespace. Language comparisons use the explicit
primary language, including `eng`/`en` equivalence; `und` cannot become an inferred
language. A whole comparison field, including list separators, has a 512-rune
allowance. Larger fields are explicitly unverified and never compared after
truncation. Valid ISBN-10 identities map to ISBN-13; checksum-invalid values stay
visible and never enter identity comparisons. Multiple candidate ISBNs are not
resolved into a selected identity. Dates compare only at full calendar-date
precision; partial dates and Calibre January 1 remain review observations.

The final review reproduced a falsely clean title after M2.2's 512-rune capture
cap. Capture now records an optional fixed-shape `metadata.limits` marker,
version 1, with booleans for title, authors, languages, ISBNs, publisher and date.
It retains no discarded text and preserves the 512-rune string, 32-item list and
16-candidate ISBN bounds. A flag excludes that field from comparisons and emits
`metadata_unverified/field_too_long`. A complete selected XMP/EXTH replacement
is qualified by its own capture, not by the overridden value.

An absent marker means historical completeness is unknown. Version 2 conservatively
qualifies scalars or list members of at least 511 runes (the pending-space case
can retain only 511), 32-item author/language lists and 16-candidate ISBN lists
as `capture_limit_unverified`. Oversized fields remain `field_too_long`. New
marked complete boundary values can compare normally. Unknown capture versions
or marked values exceeding their storage bounds fail closed. Publishers receive
capture qualifications without introducing a publisher comparison policy.

The author-sort algorithm and named authority tables are pinned to legacy commit
`ac7a90cafbc2d250bb6057dd348dae2d617c94ca`. They produce observations, not
corrections. Title sorting is an explicit M2.4 carry-forward: that plan must prove
the content-server field and define the policy before M2 is complete. Calibre
9.16's `list` allowed fields exclude `sort`; it must not be requested by guess.

M2.3 implements the following persistence and reporting contract:

- Complete a book, store unchanged evidence bytes and its ordered finding
  projection atomically. Bind package identity to the supplied ref, frozen run
  UUID and run library. Projection versions 1 and 2 mark even zero-row results
  and fix the derivation for materialized legacy packages. New completions use
  version 2; the additive migration permits markers 0, 1 and 2 without changing
  historical packages or rows.
  Existing JSONB storage changes textual whitespace; unchanged bytes refer to
  the canonical sealed representation and original hash, verified by round-trip
  equality. Completion does not reseal the augmented legacy package.
- Read reports through read-only repeatable-read snapshots. Check the seal,
  package/row/frozen identity, schema and complete projection before filtering.
  Legacy projection 0 is derived without GET-time database repair.
- Validate a historical version-1 seal/projection with version 1 before deriving
  a separate current version-2 report view. Book, Findings and CSV use that view;
  the historical package retains its original findings. A version-2 sealed
  package cannot use an older projection marker.
- Every Findings page and CSV export verifies the selected run, including books
  before a cursor and beyond the returned page. An explicit frozen book-ID
  filter may restrict the scan to that book. A missing or edited projected row
  yields an integrity error, rather than silently hiding a finding.
- Retain one package at a time and at most 50 displayed findings or frozen-book
  summaries. Pages have a 15-second budget. CSV has a 20-second budget, 50,000
  finding rows, 32 MiB including its header and one export per process. Stage
  and verify the complete file before attachment headers; keep its scratch
  separate from the runner. Network interruption after download starts remains
  a transport failure.
- Escape HTML through `html/template`; encode CSV through `encoding/csv` with
  the plan's bounded text/control/formula encoding. No claim covers arbitrary
  spreadsheet resaves. Recheck resolves historical library/UUID server-side,
  compares current Calibre identity before freezing the new one-book run, and
  retains login, CSRF, Origin and one-active-run protections.
- Bind runner list/book reads, exports and recovery probes to an explicit
  captured library ID. The final review demonstrated that a successful
  concurrent default-library probe could otherwise redirect a guarded read.
  Periodic default-library health checks remain separate from frozen-run reads.

## Consequences

Selective and paginated Findings requests scan the selected run. This costs
more queries than trusting a filter index, but a damaged projection cannot
silently suppress matching books. Deadlines and export limits fail explicitly.
No unbounded startup backfill is required. Older binaries reject the additive
fields, so rollback requires a compatible reader. Conservative comparisons can
produce review findings for punctuation/order differences and defer unusually
large metadata fields. Peak process RSS is not a tested guarantee of this ADR.
Historical reports may require two derivations per book; current qualifications
can differ from their sealed historical observations. Some complete old boundary
values will conservatively remain unverified. Production switching latency is
unmeasured; a deterministic real-client HTTP interleaving proves read binding.

The single review's Minor CSV ordering issue remains deferred: the report title
display cap runs before CSV normalization, so 512 leading spaces followed by
`=1` lose the title text. Numeric IDs remain and formula text is not exposed.

## Evidence

- The M2.3 implementation plan (kept in the maintainer's internal docs)
- [Calibre 9.16 list fields](https://github.com/kovidgoyal/calibre/blob/v9.16.0/src/calibre/db/cli/cmd_list.py)
- [Language Tag.Raw](https://pkg.go.dev/golang.org/x/text/language#Tag.Raw)
- [OWASP CSV injection guidance](https://community.owasp.org/attacks/CSV_Injection)

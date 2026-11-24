# ADR-016: Bound inspector allocations and preserve incomplete evidence

- Status: Accepted
- Date: 2026-10-13
- Decision owners: maintainer; M2.2 final review

## Context

The rewrite checks four books concurrently inside a 2 GiB container whose
downloaded files also consume memory. M2.2's count limits allowed sixteen
64 MiB decoded PDF object streams per document, plus parsed payloads. A pixel
count alone also missed a PNG decoder's two row buffers: a 24-million-pixel,
single-row RGBA16 image can allocate about 576 MB.

The final review also found quadratic trailer deduplication between context
checks and text evidence that varied with Go map iteration after the shared
content budget ran out. Limits must produce explicit incomplete findings.

## Decision

Keep the inspectors in process and apply these conservative bounds:

| Resource | Bound |
|---|---|
| PDF cumulative parsed allocation accounting | 32 MiB per document |
| PDF cumulative constructed object nodes | 100,000 per document |
| PDF decoded object-stream cache | 32 MiB of backing capacity; at most 16 streams |
| PDF unique xref entries | 131,072 on normal, scan, merge and member paths |
| PDF repair trailer candidates | 4,096 |
| PDF encoded input and decoded object/xref stream | 64 MiB each |
| Image estimated decoding memory | 256 MiB, plus the existing 24-million-pixel cap |

One lifetime budget is shared by every document lexer. It charges buffer
growth, container growth, dictionary entries, string conversions and object
construction before allocation. It is never refunded by eviction, overwrites
or failed parsing. Resource failures stay sticky through optional loads and
repair and become `not_inspected/decompressed_too_large`.

Decoded-stream retention charges backing capacity, including member indexes.
FIFO eviction satisfies both count and byte limits. A stream larger than the
cache budget can be used for the current lookup without being retained.
Filters are resolved before allocating encoded buffers; decoded buffer growth
is capped and PNG predictors compact rows in place.

Repair deduplicates ordered absolute trailer offsets against the last offset,
checks context within each scan window and bounds candidate storage. Text
resources are visited in name order. Content skipped after budget exhaustion
is unverified, never evidence of absent text.

Images estimate eight bytes per pixel plus two eight-byte-per-pixel PNG rows
and their filter bytes. Exceeding that estimate uses `ok/header_only` for
standalone images and sampled CBZ pages, without entering full decoding.

## Alternatives

Count-only caches leave large retained payloads unbounded. Recursive sizing
of cached objects does not cover objects retained by page traversal or repeated
failed parsing; lifetime allocation accounting covers both with fewer moving
parts. Process isolation remains a future option, outside this milestone.

## Consequences

These bounds supersede the Task 6 ruling allowing two million xref entries.
Unusually complex PDFs and repeated uncached loads can exhaust the lifetime
budget even when their live objects would fit. Wide images may receive a
header-only result below 24 megapixels. Existing mainstream and synthetic
fixtures must continue to pass.

Regression tests prove aggregate payload/node rejection, capacity-based cache
eviction and uncached lookup, all xref insertion paths, repair cancellation and
overlap handling, explicit late limit findings, deterministic text evidence
and the PNG header guard. They avoid intentional OOM experiments.

These are allocation and retention bounds, not a measured guarantee of peak
container RSS. Temporary growth copies, Go GC and tmpfs usage across four
workers still require deployment measurement.

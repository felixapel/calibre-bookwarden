# ADR-023: Check covers through the content server and seal integer measures

- Status: Accepted (M5.1)
- Date: 2026-10-19
- Decision: approved in the M5.1 implementation plan

## Context

Spec section 6 asks for cover findings: missing, low resolution (shortest
side under 600 px by default), a known generic placeholder, or corrupt. M5.2's
duplicate checks also need a perceptual hash of each cover. Bookwarden reads
the library only through Calibre's content server, keeps no copies of book
files, and derives findings from sealed evidence that must verify the same
way later.

## Decision

**Export.** The cover comes from `export` with `["cover", book_id]`, in the
same msgpack envelope as a format; `{"result": nil}` means no cover. Covers
have no listed size, so the export takes a cap of 20 MiB, or the per-file size
limit when that is lower: a bigger answer is not read past the cap and the
cover is `not_inspected` (`too_large`). The bytes
stream to scratch under the in-flight budget, are hashed and inspected, and
are deleted. The cover is exported only when Calibre's record says the book
has one.

**Measures.** Decoding uses the image inspector's bounds, budgeted per format:
8 bytes per pixel, and 8 per component for a progressive JPEG, whose decoder
keeps a coefficient for every sample. Above them only the header's dimensions
are kept and the cover is `not_inspected`. A decoded cover
gets its dimensions, a 64-bit difference hash of a 9×8 grey thumbnail and a
flatness: the standard deviation of a 32×32 grey thumbnail's luminance, in
tenths. Thumbnails are integer area averages, so the sealed values don't
depend on floating point.

**Findings version 4** adds `cover_missing`, `cover_corrupt`,
`cover_uninspected`, `cover_low_resolution` (shortest side under 600 px) and
`cover_placeholder` (flatness under 3.0, or a shortest side under 50 px).
The thresholds are constants of the version; another value needs a new
version. Packages sealed before M5.1 have no `cover` field, keep their bytes
and seals, and keep their version's rules.

**Placeholders are a rule, not a list.** No hashes of named placeholder images
are invented. A list can follow from real samples; a cover shared by several
books is a duplicate finding (M5.2).

## Consequences

- The Book page shows the cover's facts, never the image.
- The 600 px threshold isn't a setting.
- A `covers` projection (SHA-256, difference hash, dimensions) serves M5.2.
- A replaced cover moves Calibre's `last_modified` (pinned by the integration test), so a cover swapped during a check makes the book `source_changed` instead of sealing the wrong cover.

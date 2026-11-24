# ADR-043: Cover candidates: the EPUB's own cover, measures only

- Status: Accepted
- Date: 2026-11-08

## Context

ADR-023 measures Calibre's cover and finds it missing, small or a placeholder.
It can't say whether a better picture exists. The cover image inside the
book's own EPUB is a free candidate. Choosing and writing a cover is the
triage deck's job (ADR-037 part 2); this change only measures and observes.

## Decision

**Measures only.** A candidate seals its state (`ok`, `missing`, `corrupt`,
`too_large`, `not_inspected`), media type, SHA-256 of the image bytes,
dimensions, 64-bit difference hash and flatness, measured by the same code
(`inspect.Cover`) and decode limits as Calibre's cover. Image bytes are never
sealed or stored. Schema 5 carries them as `file_cover` on an EPUB's format
evidence; schema 4 and earlier refuse the field. They join schema 5,
`inspect.Version` 2 and findings version 6, which PR #119 introduced and
nothing has shipped, so one deploy re-measures every file once. Reused file
evidence (ADR-039) carries the candidate with it.

**The EPUB's cover** is the manifest item with `properties="cover-image"`,
else the EPUB 2 `<meta name="cover" content="id">` item, else a guide
`reference type="cover"` that points at an image. An XHTML page, an SVG or any
non-image item is skipped. A named item that is not in the manifest or the
archive is remembered and the next method is tried; if none gives an image the
candidate is `missing`. An entry past the 20 MiB cap of ADR-023 is `too_large`.

**Finding `cover_better_available`** (info, findings version 6; versions 1 to 5
derive as before; reason `epub`, on the candidate's format). It fires when
Calibre's cover is missing, under 600 px on its shortest side or a placeholder,
and the candidate is `ok`, at least 600 px on its shortest side and not a
placeholder by ADR-023's rule. When Calibre has a usable cover, the
candidate's shortest side must also be at least 1.5 times Calibre's and the
Hamming distance between the two difference hashes at most 12 bits
(`CoverMaxHamming`: the same artwork; two unrelated covers differ by about
32). These are constants of the version.

**A flat placeholder counts as a missing cover.** A Calibre cover whose
flatness is under 3.0 has no artwork for a candidate to be the same as, so
neither the 1.5 times nor the hash condition applies to it. A tiny cover
(shortest side under 50 px) that is not flat is a thumbnail of real artwork
and keeps both conditions.

## Rejected: Open Library covers

An earlier draft also fetched `covers.openlibrary.org/b/isbn/{isbn}-L.jpg`.
Measured against the live service it can't work:

- 7 of 9 `-L` requests answered 302 to archive.org. The sources client refuses
  redirects off the host, so those lookups fail and the run's breaker trips
  after about 11 requests. Following them would mean allow-listing archive.org
  hosts and relaxing the redirect rule for a gain that doesn't exist:
- every `-L` image seen was between 265×400 and 375×500 px. None reached the
  600 px rule, so the finding could never fire from it.

It was removed with its adapter, cache table and allow-list entry. A source
with larger covers would need its own ADR.

## Consequences

- No decision version reads covers; decisions are unchanged at every version.
- No new network request: the candidate is read from the zip during inspection.
- The Book page lists the candidate's measures beside Calibre's, never an image.
- Moving the finding's thresholds needs findings version 7.

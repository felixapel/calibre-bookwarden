# ISBN range table

`RangeMessage.xml` is the International ISBN Agency's range table, used by `Hyphenate` to write an ISBN the way Wikidata stores it (ADR-035).

- Source: https://www.isbn-international.org/export_rangemessage.xml
- MessageDate in the file: Thu, 29 Oct 2026 18:35:43 UTC
- Downloaded: 2026-10-29
- sha256: `68c6547a9e6aeb7b8e1e353675983b30394ebacd634fb33020fc7678c159cc0b`

The file is vendored unchanged. To update it, download it again, replace it, update the date and hash here, and run `go test ./internal/findings/isbnranges/`. A new table only turns a `false` from `Hyphenate` into a hyphenated ISBN or moves a registrant boundary; if hyphenated strings of already-looked-up ISBNs change, bump `sources.MappingVersion`.

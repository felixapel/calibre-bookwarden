# Manga and Comics Mode (Future Design)

While `calibre-ai-auditor` is currently optimized for standard text-based books (EPUB, PDF), the architecture is prepared to support Manga, Comics, Light Novels, and Web Novels. This will be primarily driven by integrations with Komga, Kavita, and Komf.

## Normal Books vs Manga/Comics

1.  **Format Differences**: Comics typically use CBZ/CBR formats containing images, whereas normal books use EPUB/MOBI containing structured text.
2.  **Metadata Location**: Comics often rely on `ComicInfo.xml` inside the archive, rather than OPF files.
3.  **Hierarchy**: Manga and comics usually follow a strict Volume/Chapter hierarchy, whereas normal books are often standalone or part of a simple series.
4.  **Providers**: Standard book providers (Google Books, OpenLibrary) are poor sources for Manga. We need specialized providers like AniList, MyAnimeList (MAL), MangaUpdates, and ComicVine.

## Proposed Provider Differences

The metadata provider hierarchy will dynamically switch if `manga_mode` is enabled or if the file extension is `.cbz`/`.cbr`:
*   **Primary Extraction**: Read `ComicInfo.xml` inside the archive.
*   **Specialized Providers**: Query AniList/MAL/MangaUpdates via Komf (or native integrations).
*   **Image Analysis**: OCR fallback will specifically target the cover or first few pages to extract Japanese/English titles and author names.

## Volume vs Chapter Logic

Currently, the evidence resolver evaluates `series` and `series_index` as simple fields. In Manga Mode, the resolver must understand:
*   Whether the file is a single chapter or a compiled volume.
*   How to consolidate chapter metadata into a volume record.
*   This will likely require a specialized `resolve_manga_hierarchy` rule in `field_rules.py`.

## Komga and Kavita API Integration

In the future, `calibre-ai-auditor` can act as a sidecar for Komga and Kavita, similar to its relationship with Calibre:
*   Instead of `calibredb`, the auditor will communicate via Komga/Kavita REST APIs to fetch current metadata and push updates.
*   The `ApplyEngine` will map the `MetadataResolution` back to Komga/Kavita API models.

## Komf as Resolver Inspiration

Komf (Komga Metadata Fetcher) provides excellent inspiration for matching logic. We will look to adapt its multi-provider fallback strategies specifically for the nuanced metadata of translated works (e.g., handling Romaji vs Kanji titles).

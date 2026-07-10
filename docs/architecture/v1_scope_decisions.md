# v1.0 Scope Decisions: Comics, Audiobooks, MCP

These three open questions from the v1.0 plan were deferred pending design
review.  This document captures the recommended decisions based on the actual
state of the codebase and the homelab environment.

---

## 1. Comics / Manga — IN scope for v1.0

**Recommendation**: ship comics support in v1.0 GA.

**Rationale**:
- The existing `extractors/comics.py` already handles CBZ/CBR
- Manga/comics metadata APIs (Anilist, MAL, MangaUpdates) are first-class
  already wired via `MangaSettings` in `config/settings.py`
- Manga/comic libraries are the WORST for bad metadata — typically zero
  series/volume/chapter info, incorrect authors, mis-attributed covers.
  Auditing these is high-value.
- Homelab already has vision-capable models (3090) that excel at cover art
  comparison (cover identification is often the only reliable signal for manga)

**Implementation status** (partial, tracked in v1.1):
- ✅ Fields: `volume`, `chapter` (decimal), `series_position` added to Metadata model.
- ✅ Vision: schema/prompt updated for comics; `verify_comic_cover()` helper added in vision.py (extracts series/volume/chapter from cover).
- ✅ Extractor: comics.py parses Chapter and handles volume/chapter.
- ⏳ Full `verify_comic_cover()` rule in engine + workflow, Komf adapter, OCR profile still pending (see ROADMAP v1.1).

**Out of scope for v1.0 GA**: auto-apply on comics.  Manual review only
because cover art can be ambiguous between editions.

---

## 2. Audiobooks — OUT of scope for v1.0

**Recommendation**: defer audiobooks to v1.1+.

**Rationale**:
- Audiobook metadata lives in ID3 / Vorbis / MP4 chapters, not in the
  Calibre metadata DB the way ebook metadata does
- Whisper transcription is expensive ($0.006/min for whisper-1) and slow
  for large collections (10k books × 8 hours avg = 80k hours = ~$480)
- Homelab hardware doesn't have great Whisper acceleration
- Audiobook metadata is generally MORE accurate than ebook metadata
  (Audible, Libro.fm imports are clean), so the marginal value is lower
- The v0.9 architecture already has `manga_mode`; we'd want a parallel
  `audiobook_mode` that touches different systems (chapter markers, ID3 tags)

**When to revisit**: if Felix's collection grows to 5k+ audiobooks with
noticeable metadata drift, prioritize in v1.1.

**Lightweight option**: if audiobook .opf metadata is suspect, the existing
ebook verification engine works on the embedded ebook file (often present
alongside the audiobook in Calibre).  No new code needed for that case.

---

## 3. MCP Server — IN scope for v1.0 (small effort, big leverage)

**Recommendation**: ship MCP server in v1.0 GA — small effort, enables
Hermes integration and personal AI workflows.

**Rationale**:
- Felix already has a Hermes VM running multiple models (per CLAUDE.md)
- book-memex (queelius) shipped an MCP server as a thin wrapper around their
  query API — exact same pattern fits calibre-ai-auditor
- MCP exposure enables "ask Hermes: which of my books have the worst metadata"
  and similar workflows
- Implementation is ~150 LOC on top of existing `web/api/books.py`

**Implementation** (deferred to v1.0-rc):
1. New module `verification/mcp_server.py` exposing tools:
   - `query_book_audit(book_id)` — returns the latest BookVerdict as JSON
   - `list_problematic_books(min_risk_flags)` — list books needing review
   - `get_run_metrics(run_id)` — Prometheus-style counters for a run
2. STDIO transport (simpler than HTTP for personal use)
3. Smoke test in `tests/test_mcp.py` using `mcp-client` if available

**Out of scope**: HTTP transport, OAuth, multi-user.  Personal use only.

---

## Summary table

| Feature | v1.0 GA? | Effort | Why |
|---|---|---|---|
| Comics/manga OCR + vision verification | **YES (partial)** | Medium | High value, infrastructure already there; core fields + vision helper done in v1.1 |
| Audiobook verification | NO (v1.1+) | High | Marginal value, expensive transcription |
| MCP server | **YES** | Small | Big leverage for personal AI workflows |
| Whisper/audio OCR | NO (v1.1+) | High | Same reasons as audiobooks |
| Komf integration | NO (v1.1+) | Medium | Useful but not blocking |

## Implementation priority order

1. Comics verification (with vision LLM cover identification) — most value
2. MCP server — easy win, enables new workflows
3. Audiobook support — only when collection grows
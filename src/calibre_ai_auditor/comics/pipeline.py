"""Central comic enrichment pipeline (v1.1 integration closure).

Single shipped function that wires vision + Komf and returns enriched declared/observed.
Replaces scattered duplicate merge logic across audit/ingest/web.
"""

from __future__ import annotations

import logging
from pathlib import Path
from typing import Any

from calibre_ai_auditor.config.settings import Settings
from calibre_ai_auditor.extractors.comics import extract_comic_info_xml

logger = logging.getLogger(__name__)


async def enrich_comic_observations(
    settings: Settings,
    book_path: Path | None,
    cover_path: Path | None,
    declared: dict[str, Any],
    observed: dict[str, Any],
) -> tuple[dict[str, Any], dict[str, Any]]:
    """
    Enrich declared/observed with comic fields (volume int, chapter float/decimal per Weebarr,
    series_position, cover_vision) by calling vision (when manga_mode + cbz) and Komf (when enabled).

    Returns (enriched_declared, enriched_observed).
    """
    manga = getattr(settings, "manga_mode", None)
    enabled = bool(manga and getattr(manga, "enabled", False))
    use_komf = bool(manga and getattr(getattr(manga, "providers", None), "komf", False)) if manga else False

    comic_meta: dict[str, Any] = {}
    cover_vision: dict[str, Any] | None = None

    # 1. ComicInfo if cbz/cbr (prefer)
    if book_path and book_path.suffix.lower() in (".cbz", ".cbr"):
        try:
            comic_meta = extract_comic_info_xml(book_path) or {}
        except Exception as e:
            logger.warning(f"extract_comic_info_xml failed for {book_path}: {e}")

    # 2. Vision cover for comics (when manga + cbz; auto-extract cover if not provided, reuses verify_comic_cover)
    if enabled and cover_path and getattr(cover_path, "exists", lambda: False)():
        try:
            from calibre_ai_auditor.ocr.vision import VisionVerifier, verify_comic_cover

            verifier = VisionVerifier(settings)
            vision_res = await verify_comic_cover(verifier, cover_path)
            if vision_res:
                cover_vision = vision_res
                for k in ("volume", "chapter", "series_position", "series", "title"):
                    if (k not in comic_meta or comic_meta.get(k) is None) and vision_res.get(k) is not None:
                        comic_meta[k] = vision_res[k]
        except Exception as e:
            logger.warning(f"comic vision failed for {cover_path}: {e}")
    elif enabled and book_path and book_path.suffix.lower() in (".cbz", ".cbr"):
        # auto-extract for cbz vision path (so callers can pass cover=None)
        try:
            import tempfile

            from calibre_ai_auditor.extractors.cover import extract_zip_cover

            with tempfile.TemporaryDirectory() as tmpd:
                tmp_cover = Path(tmpd) / "cbz_cover.jpg"
                if extract_zip_cover(book_path, tmp_cover) and tmp_cover.exists():
                    from calibre_ai_auditor.ocr.vision import VisionVerifier, verify_comic_cover

                    verifier = VisionVerifier(settings)
                    vision_res = await verify_comic_cover(verifier, tmp_cover)
                    if vision_res:
                        cover_vision = vision_res
                        for k in ("volume", "chapter", "series_position", "series", "title"):
                            if (k not in comic_meta or comic_meta.get(k) is None) and vision_res.get(k) is not None:
                                comic_meta[k] = vision_res[k]
        except Exception as e:
            logger.warning(f"comic vision (auto) failed for {book_path}: {e}")

    # 3. Komf lookup if enabled (on title/series)
    if use_komf:
        try:
            from calibre_ai_auditor.calibre.cli import CalibreCLI
            from calibre_ai_auditor.providers.registry import ProviderRegistry

            lib_path = getattr(getattr(settings, "library", None), "path", None)
            cli = CalibreCLI(lib_path)
            reg = ProviderRegistry(cli, enable_komf=use_komf)
            komf = reg.providers.get("komf")
            if komf:
                title = (
                    comic_meta.get("series")
                    or comic_meta.get("title")
                    or declared.get("series")
                    or declared.get("title")
                )
                if title:
                    candidates = await komf.fetch_candidates(title=title, authors=declared.get("authors"))
                    if candidates:
                        best = candidates[0]
                        # Merge komf (decimal chapter)
                        for k in ("volume", "chapter", "series_position"):
                            val = getattr(best.metadata, k, None)
                            if val is not None and (k not in comic_meta or comic_meta.get(k) is None):
                                comic_meta[k] = val
                        if best.metadata.series and not comic_meta.get("series"):
                            comic_meta["series"] = best.metadata.series
        except Exception as e:
            logger.warning(f"Komf lookup failed: {e}")

    # 4. Merge into declared/observed (prefer comic_meta non-None; ensure decimal chapter)
    def _merge(target: dict[str, Any], src: dict[str, Any]) -> None:
        for k, v in src.items():
            if v is not None and (k not in target or target.get(k) is None):
                if k in ("chapter", "series_position"):
                    try:
                        target[k] = float(v)
                    except (TypeError, ValueError):
                        target[k] = v
                elif k == "volume":
                    try:
                        target[k] = int(v)
                    except (TypeError, ValueError):
                        target[k] = v
                else:
                    target[k] = v

    enriched_declared = dict(declared)
    enriched_observed = dict(observed)

    _merge(enriched_declared, comic_meta)
    _merge(enriched_observed, comic_meta)

    if cover_vision:
        enriched_observed["cover_vision"] = cover_vision
        # also ensure declared gets key fields if missing
        for k in ("volume", "chapter", "series_position"):
            if k in cover_vision and enriched_declared.get(k) is None:
                try:
                    enriched_declared[k] = float(cover_vision[k]) if k != "volume" else int(cover_vision[k])
                except Exception:
                    enriched_declared[k] = cover_vision[k]

    return enriched_declared, enriched_observed

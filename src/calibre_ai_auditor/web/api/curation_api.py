import logging
from dataclasses import asdict
from typing import Any

from fastapi import APIRouter, Depends, HTTPException

from calibre_ai_auditor.calibre.direct_engine import DirectCalibreEngine
from calibre_ai_auditor.config.settings import Settings, load_settings
from calibre_ai_auditor.curation.duplicates import DuplicateConsolidator
from calibre_ai_auditor.curation.series import SeriesGapHunter
from calibre_ai_auditor.web.schemas import APIResponse

logger = logging.getLogger(__name__)

router = APIRouter(prefix="/curation", tags=["Library Curation"])


def get_settings() -> Settings:
    return load_settings()


def get_engine(settings: Settings = Depends(get_settings)) -> DirectCalibreEngine:
    lib_path = settings.library.path
    if not lib_path:
        raise HTTPException(
            status_code=400,
            detail="Calibre library path is not configured.",
        )
    try:
        return DirectCalibreEngine(lib_path)
    except FileNotFoundError as e:
        raise HTTPException(status_code=404, detail=str(e)) from e


@router.get("/series-gaps", response_model=APIResponse)
async def get_series_gaps(
    engine: DirectCalibreEngine = Depends(get_engine),
) -> Any:
    """Scans all series and multi-volume sagas in the library to identify missing volumes."""
    conn = engine.get_connection(read_only=True)
    try:
        hunter = SeriesGapHunter(conn)
        gaps = hunter.find_all_gaps()
        return {
            "status": "success",
            "data": [asdict(g) for g in gaps],
            "meta": {"total_gaps": len(gaps)},
        }
    except Exception as exc:
        logger.exception("Series gap scan failed: %s", exc)
        raise HTTPException(status_code=500, detail=f"Series gap scan failed: {exc}") from exc
    finally:
        conn.close()


@router.get("/duplicates", response_model=APIResponse)
async def get_duplicate_clusters(
    engine: DirectCalibreEngine = Depends(get_engine),
) -> Any:
    """Identifies multi-format intra-library duplicates and shared ISBN collisions."""
    conn = engine.get_connection(read_only=True)
    try:
        consolidator = DuplicateConsolidator(conn)
        clusters = consolidator.find_multi_format_duplicates()
        return {
            "status": "success",
            "data": [asdict(c) for c in clusters],
            "meta": {"total_clusters": len(clusters)},
        }
    except Exception as exc:
        logger.exception("Duplicate consolidation failed: %s", exc)
        raise HTTPException(status_code=500, detail=f"Duplicate scan failed: {exc}") from exc
    finally:
        conn.close()

import logging
from typing import Any

from fastapi import APIRouter, Depends, HTTPException

from calibre_ai_auditor.calibre.direct_engine import DirectCalibreEngine
from calibre_ai_auditor.config.settings import Settings, load_settings
from calibre_ai_auditor.web.schemas import APIResponse

logger = logging.getLogger(__name__)

router = APIRouter(prefix="/audit", tags=["360 Library Audit"])


def get_settings() -> Settings:
    return load_settings()


def get_engine(settings: Settings = Depends(get_settings)) -> DirectCalibreEngine:
    lib_path = settings.library.path
    if not lib_path:
        raise HTTPException(
            status_code=400,
            detail="Calibre library path is not configured. Set BOOKAUDIT_LIBRARY_PATH.",
        )
    try:
        return DirectCalibreEngine(lib_path)
    except FileNotFoundError as e:
        raise HTTPException(status_code=404, detail=str(e)) from e


@router.get("/360", response_model=APIResponse)
async def get_library_360_audit(
    engine: DirectCalibreEngine = Depends(get_engine),
) -> Any:
    """Executes a 360-degree forensic audit of the Calibre SQLite database and filesystem."""
    try:
        report = engine.audit_library()
        return {"status": "success", "data": report}
    except Exception as exc:
        logger.exception("Audit 360 failed: %s", exc)
        raise HTTPException(status_code=500, detail=f"360 Audit failed: {exc}") from exc


@router.post("/sync-author-sorts", response_model=APIResponse)
async def sync_author_sorts(
    engine: DirectCalibreEngine = Depends(get_engine),
    settings: Settings = Depends(get_settings),
) -> Any:
    """Creates a hot snapshot and synchronizes canonical author sorts across metadata.db."""
    if settings.library.read_only:
        raise HTTPException(
            status_code=403,
            detail="Operation rejected: library is in read-only mode.",
        )
    try:
        snapshot = engine.create_snapshot()
        updated_count = engine.sync_all_author_sorts()
        return {
            "status": "success",
            "data": {
                "updated_count": updated_count,
                "snapshot_path": str(snapshot),
                "message": f"Successfully synchronized author_sort for {updated_count} books.",
            },
        }
    except Exception as exc:
        logger.exception("Failed to sync author sorts: %s", exc)
        raise HTTPException(status_code=500, detail=f"Author sort sync failed: {exc}") from exc


@router.post("/purge-orphan-fks", response_model=APIResponse)
async def purge_orphan_foreign_keys(
    engine: DirectCalibreEngine = Depends(get_engine),
    settings: Settings = Depends(get_settings),
) -> Any:
    """Purges orphaned junction rows and unused tags/authors after taking an atomic snapshot."""
    if settings.library.read_only:
        raise HTTPException(
            status_code=403,
            detail="Operation rejected: library is in read-only mode.",
        )
    try:
        snapshot = engine.create_snapshot()
        purged = engine.purge_orphan_foreign_keys()
        return {
            "status": "success",
            "data": {
                "purged_records": purged,
                "snapshot_path": str(snapshot),
                "message": "Orphan foreign keys successfully purged.",
            },
        }
    except Exception as exc:
        logger.exception("Failed to purge orphan foreign keys: %s", exc)
        raise HTTPException(status_code=500, detail=f"FK purge failed: {exc}") from exc

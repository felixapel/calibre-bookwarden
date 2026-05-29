from fastapi import HTTPException

from calibre_ai_auditor.config.settings import Settings


def require_write_confirmation(settings: Settings, *, force: bool) -> None:
    """Enforce read-only mode and explicit confirmation for library writes."""
    if settings.library.read_only:
        raise HTTPException(status_code=400, detail="Library is in read-only mode")
    if not force:
        raise HTTPException(
            status_code=400,
            detail="Write operation requires confirmation: set force=true after reviewing changes",
        )

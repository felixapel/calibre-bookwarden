from typing import Annotated, Any

from fastapi import APIRouter, Depends, HTTPException

from calibre_ai_auditor.config.settings import Settings, load_settings
from calibre_ai_auditor.providers.testing import probe_provider_connectivity
from calibre_ai_auditor.web.schemas import APIResponse

router = APIRouter(prefix="/providers", tags=["Providers"])


def get_settings() -> Settings:
    return load_settings()


@router.get("", response_model=APIResponse)
async def list_providers() -> Any:
    return {"status": "success", "data": ["calibre_fetch", "openlibrary", "google_books"]}


@router.post("/test", response_model=APIResponse)
async def test_provider(
    name: str,
    settings: Annotated[Settings, Depends(get_settings)],
) -> Any:
    try:
        result = await probe_provider_connectivity(settings, name)
    except ValueError as exc:
        raise HTTPException(status_code=404, detail=str(exc)) from exc

    status = "success" if result.get("ok") else "error"
    return {"status": status, "data": result}

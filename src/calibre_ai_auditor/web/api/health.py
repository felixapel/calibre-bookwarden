import shutil
from typing import Any

from fastapi import APIRouter, Depends

from calibre_ai_auditor.config.settings import Settings, load_settings
from calibre_ai_auditor.extractors.tika_client import TikaClient
from calibre_ai_auditor.llm.router import LLMRouter
from calibre_ai_auditor.vectors.client import VectorClient

router = APIRouter()


def get_settings() -> Settings:
    return load_settings()


@router.get("/health")
async def health_check() -> dict[str, str]:
    return {"status": "ok"}


@router.get("/doctor")
async def doctor_check(settings: Settings = Depends(get_settings)) -> dict[str, Any]:
    results: dict[str, Any] = {
        "dependencies": {},
        "connectivity": {},
    }
    
    # 1. System Tools
    tools = ["calibredb", "ebook-meta", "fetch-ebook-metadata", "ocrmypdf"]
    for tool in tools:
        path = shutil.which(tool)
        results["dependencies"][tool] = {"found": path is not None, "path": path}

    # 2. Sidecars
    
    # Tika
    tika = TikaClient(
        enabled=settings.extractors.tika.enabled,
        base_url=settings.extractors.tika.base_url,
    )
    results["connectivity"]["tika"] = {
        "enabled": settings.extractors.tika.enabled,
        "ok": await tika.test_connection(),
        "url": settings.extractors.tika.base_url,
    }

    # Qdrant
    qdrant = VectorClient(
        url=settings.vectors.qdrant_url,
        collection=settings.vectors.collection,
        enabled=settings.vectors.enabled,
    )
    results["connectivity"]["qdrant"] = {
        "enabled": settings.vectors.enabled,
        "ok": await qdrant.test_connection() if settings.vectors.enabled else False,
        "url": settings.vectors.qdrant_url,
    }

    # LLMs (via Router)
    router_llm = LLMRouter(settings)
    results["connectivity"]["llms"] = {}
    for name, provider in router_llm.providers.items():
        results["connectivity"]["llms"][name] = {
            "ok": await provider.test_connection(),
            "name": provider.name,
        }

    return results

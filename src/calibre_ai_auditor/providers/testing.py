import logging
from typing import Any

import httpx

from calibre_ai_auditor.calibre.cli import CalibreCLI
from calibre_ai_auditor.config.settings import Settings
from calibre_ai_auditor.providers.registry import ProviderRegistry

logger = logging.getLogger(__name__)

PROBE_TITLE = "The Great Gatsby"
PROBE_AUTHORS = ["Fitzgerald"]


async def probe_provider_connectivity(settings: Settings, provider_name: str) -> dict[str, Any]:
    """Run a lightweight connectivity probe against a metadata provider."""
    library_path = settings.library.path
    cli = CalibreCLI(library_path) if library_path else CalibreCLI(None)
    registry = ProviderRegistry(cli, enable_komf=provider_name == "komf")
    provider = registry.providers.get(provider_name)
    if provider is None:
        raise ValueError(f"Unknown provider: {provider_name}")

    if provider_name == "calibre_fetch":
        if not library_path or not library_path.exists():
            return {
                "provider": provider_name,
                "ok": False,
                "message": "Calibre library path not configured or missing",
            }
        try:
            books = cli.list_books()
            return {
                "provider": provider_name,
                "ok": True,
                "message": f"calibredb reachable ({len(books)} books visible)",
            }
        except Exception as exc:
            return {"provider": provider_name, "ok": False, "message": str(exc)}

    if provider_name == "openlibrary":
        try:
            async with httpx.AsyncClient(timeout=10.0) as client:
                response = await client.get(
                    "https://openlibrary.org/search.json",
                    params={"title": PROBE_TITLE, "limit": 1},
                )
                response.raise_for_status()
                count = response.json().get("numFound", 0)
            return {
                "provider": provider_name,
                "ok": True,
                "message": f"Open Library API reachable (probe matches: {count})",
            }
        except Exception as exc:
            return {"provider": provider_name, "ok": False, "message": str(exc)}

    if provider_name == "google_books":
        try:
            async with httpx.AsyncClient(timeout=10.0) as client:
                response = await client.get(
                    "https://www.googleapis.com/books/v1/volumes",
                    params={"q": f"intitle:{PROBE_TITLE}", "maxResults": 1},
                )
                response.raise_for_status()
                total = response.json().get("totalItems", 0)
            return {
                "provider": provider_name,
                "ok": True,
                "message": f"Google Books API reachable (probe items: {total})",
            }
        except Exception as exc:
            return {"provider": provider_name, "ok": False, "message": str(exc)}

    try:
        candidates = await provider.fetch_candidates(title=PROBE_TITLE, authors=PROBE_AUTHORS)
        if not candidates:
            return {
                "provider": provider_name,
                "ok": False,
                "message": "Provider returned no candidates for probe query",
            }
        return {
            "provider": provider_name,
            "ok": True,
            "message": f"Returned {len(candidates)} candidate(s) for probe query",
        }
    except Exception as exc:
        logger.exception("Provider test failed for %s", provider_name)
        return {"provider": provider_name, "ok": False, "message": str(exc)}

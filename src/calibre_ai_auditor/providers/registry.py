import logging
from typing import Any

from calibre_ai_auditor.calibre.cli import CalibreCLI
from calibre_ai_auditor.providers.base import BaseProvider
from calibre_ai_auditor.providers.calibre_fetch import CalibreFetchProvider
from calibre_ai_auditor.providers.google_books import GoogleBooksProvider
from calibre_ai_auditor.providers.openlibrary import OpenLibraryProvider
from calibre_ai_auditor.storage.models import Candidate, Metadata

logger = logging.getLogger(__name__)


class KomfProvider(BaseProvider):
    """Basic Komf provider stub for manga/comics metadata (v1.1).
    Attempts real call to Komf if reachable (e.g. http://komf:8085), else returns minimal stub metadata.
    Komf typically proxies AniList/MAL/ComicVine etc for Komga/Kavita.
    """

    def __init__(self, base_url: str = "http://localhost:8085"):
        self.base_url = base_url

    @property
    def name(self) -> str:
        return "komf"

    async def fetch_candidates(
        self, title: str | None = None, authors: list[str] | None = None, isbn: str | None = None
    ) -> list[Candidate]:
        if not title:
            return []
        # Try real call (stub endpoint; actual Komf API is /api/v1/series or provider match)
        try:
            import httpx  # lazy to avoid hard dep if not installed

            async with httpx.AsyncClient(timeout=6.0) as client:
                payload: dict[str, Any] = {"title": title}
                if authors:
                    payload["authors"] = authors
                if isbn:
                    payload["isbn"] = isbn
                # Attempt common komf match path (may vary); catch all
                for endpoint in ("/api/match", "/api/v1/providers/match", "/series/match"):
                    try:
                        r = await client.post(f"{self.base_url}{endpoint}", json=payload)
                        if r.status_code == 200:
                            data = r.json()
                            # naive: if list or wrapped
                            items = data if isinstance(data, list) else data.get("candidates", [data]) if isinstance(data, dict) else []
                            cands = []
                            for item in items[:3]:
                                md = self.normalize_metadata(item)
                                cands.append(Candidate(
                                    candidate_id=f"komf-{item.get('id', title)}",
                                    provider="komf",
                                    provider_url=item.get("url"),
                                    metadata=md,
                                    raw_score=0.8,
                                ))
                            if cands:
                                return cands
                    except Exception:
                        continue
        except Exception as e:
            logger.debug("Komf real call skipped/failed (stub): %s", e)

        # Minimal stub metadata with decimal chapter (per requirement)
        meta = Metadata(
            title=title,
            authors=authors or [],
            series=title,
            volume=1,
            chapter=1.0,  # decimal
            series_position=1.0,
            identifiers={"komf": "stub"},
        )
        return [Candidate(
            candidate_id=f"komf-stub-{title}",
            provider="komf",
            metadata=meta,
            raw_score=0.4,
        )]

    def normalize_metadata(self, raw_data: Any) -> Metadata:
        if isinstance(raw_data, Metadata):
            return raw_data
        if not isinstance(raw_data, dict):
            raw_data = {}
        ch = raw_data.get("chapter") or raw_data.get("number")
        try:
            ch = float(ch) if ch is not None else None  # decimal chapter
        except Exception:
            ch = None
        return Metadata(
            title=raw_data.get("title") or raw_data.get("name"),
            authors=raw_data.get("authors") or [],
            series=raw_data.get("series") or raw_data.get("title"),
            volume=raw_data.get("volume"),
            chapter=ch,
            series_position=raw_data.get("series_position") or ch,
            publisher=raw_data.get("publisher"),
            identifiers=raw_data.get("identifiers") or {},
            tags=raw_data.get("tags") or [],
        )


class ProviderRegistry:
    def __init__(self, cli: CalibreCLI, order: list[str] | None = None, enable_komf: bool = True):
        self.cli = cli
        self.order = order or ["calibre_fetch", "openlibrary", "google_books", "komf"]
        self.providers: dict[str, BaseProvider] = {
            "google_books": GoogleBooksProvider(),
            "openlibrary": OpenLibraryProvider(),
            "calibre_fetch": CalibreFetchProvider(cli),
            "komf": KomfProvider(),
        }
        if not enable_komf and "komf" in self.providers:
            # still registered for basic impl, but callers can filter
            pass

    def get_providers(self) -> list[BaseProvider]:
        """Returns the list of providers in the configured order."""
        return [self.providers[name] for name in self.order if name in self.providers]

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
    """Best-effort Komf adapter that fails closed on unavailable or invalid data."""

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
        try:
            import httpx  # lazy to avoid hard dep if not installed

            limits = httpx.Limits(max_connections=10, max_keepalive_connections=5)
            async with httpx.AsyncClient(timeout=6.0, limits=limits) as client:
                payload: dict[str, Any] = {"title": title}
                if authors:
                    payload["authors"] = authors
                if isbn:
                    payload["isbn"] = isbn
                for endpoint in ("/api/match", "/api/v1/providers/match", "/series/match"):
                    try:
                        r = await client.post(f"{self.base_url}{endpoint}", json=payload)
                        if r.status_code != 200:
                            continue
                        data = r.json()
                        if isinstance(data, list):
                            items = data
                        elif isinstance(data, dict):
                            if "candidates" in data:
                                wrapped = data["candidates"]
                                items = wrapped if isinstance(wrapped, list) else []
                            else:
                                items = [data]
                        else:
                            items = []
                        candidates: list[Candidate] = []
                        for index, item in enumerate(items[:3]):
                            if not isinstance(item, dict):
                                continue
                            try:
                                md = self.normalize_metadata(item)
                                if not (md.title or md.series):
                                    continue
                                candidates.append(
                                    Candidate(
                                        candidate_id=f"komf-{item.get('id') or f'{title}-{index}'}",
                                        provider="komf",
                                        provider_url=item.get("url"),
                                        metadata=md,
                                        raw_score=0.8,
                                    )
                                )
                            except (TypeError, ValueError):
                                continue
                        if candidates:
                            return candidates
                    except Exception as exc:
                        logger.debug("Komf endpoint %s failed closed: %s", endpoint, exc)
                        continue
        except Exception as exc:
            logger.debug("Komf request failed closed: %s", exc)
        return []

    def normalize_metadata(self, raw_data: Any) -> Metadata:
        if isinstance(raw_data, Metadata):
            return raw_data
        if not isinstance(raw_data, dict):
            raw_data = {}
        chapter = raw_data.get("chapter") or raw_data.get("number")
        try:
            chapter = float(chapter) if chapter is not None else None
        except (TypeError, ValueError):
            chapter = None
        volume = raw_data.get("volume")
        try:
            volume = int(volume) if volume is not None else None
        except (TypeError, ValueError):
            volume = None
        series_position = raw_data.get("series_position")
        try:
            series_position = float(series_position) if series_position is not None else chapter
        except (TypeError, ValueError):
            series_position = chapter
        authors = raw_data.get("authors") or []
        if isinstance(authors, str):
            authors = [authors]
        elif not isinstance(authors, list):
            authors = []
        identifiers = raw_data.get("identifiers") or {}
        if not isinstance(identifiers, dict):
            identifiers = {}
        tags = raw_data.get("tags") or []
        if isinstance(tags, str):
            tags = [tags]
        elif not isinstance(tags, list):
            tags = []
        return Metadata(
            title=raw_data.get("title") or raw_data.get("name"),
            authors=authors,
            series=raw_data.get("series") or raw_data.get("title"),
            volume=volume,
            chapter=chapter,
            series_position=series_position,
            publisher=raw_data.get("publisher"),
            identifiers=identifiers,
            tags=tags,
        )


class ProviderRegistry:
    def __init__(self, cli: CalibreCLI, order: list[str] | None = None, enable_komf: bool = False):
        self.cli = cli
        self.order = order if order is not None else ["calibre_fetch", "openlibrary", "google_books", "komf"]
        self.providers: dict[str, BaseProvider] = {
            "google_books": GoogleBooksProvider(),
            "openlibrary": OpenLibraryProvider(),
            "calibre_fetch": CalibreFetchProvider(cli),
        }
        if enable_komf:
            self.providers["komf"] = KomfProvider()

    def get_providers(self) -> list[BaseProvider]:
        """Returns the list of providers in the configured order."""
        return [self.providers[name] for name in self.order if name in self.providers]

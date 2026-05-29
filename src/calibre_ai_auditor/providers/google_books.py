import logging
from typing import Any

import httpx

from calibre_ai_auditor.providers.base import BaseProvider
from calibre_ai_auditor.storage.models import Candidate, Metadata

logger = logging.getLogger(__name__)


class GoogleBooksProvider(BaseProvider):
    @property
    def name(self) -> str:
        return "google_books"

    async def fetch_candidates(
        self,
        title: str | None = None,
        authors: list[str] | None = None,
        isbn: str | None = None,
    ) -> list[Candidate]:
        q = ""
        if isbn:
            q = f"isbn:{isbn}"
        elif title:
            q = f"intitle:{title}"
            if authors:
                q += f" inauthor:{authors[0]}"
        else:
            return []

        url = "https://www.googleapis.com/books/v1/volumes"
        params: dict[str, str | int] = {"q": q, "maxResults": 5}

        logger.info(f"Fetching candidates from Google Books (q={q})...")
        async with httpx.AsyncClient(timeout=15.0) as client:
            try:
                response = await client.get(url, params=params)
                response.raise_for_status()
                data = response.json()
                return self._parse_volumes(data)
            except Exception as e:
                logger.error(f"Google Books fetch failed: {e}")
                return []

    def _parse_volumes(self, data: dict[str, Any]) -> list[Candidate]:
        candidates = []
        for item in data.get("items", []):
            volume_info = item.get("volumeInfo", {})

            identifiers = {}
            for ident in volume_info.get("industryIdentifiers", []):
                type_ = ident.get("type", "").lower()
                if "isbn" in type_:
                    identifiers["isbn"] = ident.get("identifier")
                else:
                    identifiers[type_] = ident.get("identifier")

            metadata = Metadata(
                title=volume_info.get("title"),
                authors=volume_info.get("authors", []),
                publisher=volume_info.get("publisher"),
                published_date=volume_info.get("publishedDate"),
                language=volume_info.get("language"),
                identifiers=identifiers,
            )

            candidates.append(
                Candidate(
                    candidate_id=f"google_books:{item.get('id')}",
                    provider=self.name,
                    provider_url=item.get("selfLink"),
                    metadata=metadata,
                    cover_url=volume_info.get("imageLinks", {}).get("thumbnail"),
                )
            )
        return candidates

    def normalize_metadata(self, _raw_data: Any) -> Metadata:
        return Metadata()

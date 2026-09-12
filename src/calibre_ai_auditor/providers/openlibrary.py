import logging
from typing import Any

from calibre_ai_auditor.providers.base import BaseProvider
from calibre_ai_auditor.providers.cache import cached_get_json
from calibre_ai_auditor.storage.models import Candidate, Metadata

logger = logging.getLogger(__name__)


class OpenLibraryProvider(BaseProvider):
    @property
    def name(self) -> str:
        return "openlibrary"

    async def fetch_candidates(
        self,
        title: str | None = None,
        authors: list[str] | None = None,
        isbn: str | None = None,
    ) -> list[Candidate]:
        params: dict[str, Any] = {"format": "json"}
        if isbn:
            params["isbn"] = isbn
        elif title:
            params["title"] = title
            if authors:
                params["author"] = authors[0]  # OL search accepts one author usually
        else:
            return []

        url = "https://openlibrary.org/search.json"
        logger.info(f"Fetching candidates from OpenLibrary (title={title}, isbn={isbn})...")
        try:
            data = await cached_get_json(url, params=params, timeout=15.0)
            return self._parse_search_results(data)
        except Exception as e:
            logger.error(f"OpenLibrary fetch failed: {e}")
            return []

    def _parse_search_results(self, data: dict[str, Any]) -> list[Candidate]:
        candidates = []
        for doc in data.get("docs", [])[:5]:  # Limit to top 5
            ol_id = str(doc.get("key", "")).split("/")[-1]
            first_publish_year = doc.get("first_publish_year")
            pub_date = str(first_publish_year) if first_publish_year else None

            isbn_list = doc.get("isbn", [])
            first_isbn = isbn_list[0] if isbn_list else None

            metadata = Metadata(
                title=doc.get("title"),
                authors=doc.get("author_name", []),
                publisher=(doc.get("publisher", [None])[0] if doc.get("publisher") else None),
                published_date=pub_date,
                language=doc.get("language", [None])[0] if doc.get("language") else None,
                identifiers={
                    "openlibrary": ol_id,
                },
            )
            if first_isbn:
                metadata.identifiers["isbn"] = str(first_isbn)

            # Filter out None values from identifiers
            metadata.identifiers = {k: v for k, v in metadata.identifiers.items() if v}

            cover_id = doc.get("cover_i")
            cover_url = f"https://covers.openlibrary.org/b/id/{cover_id}-L.jpg" if cover_id else None

            candidates.append(
                Candidate(
                    candidate_id=f"openlibrary:{ol_id}",
                    provider=self.name,
                    provider_url=f"https://openlibrary.org{doc.get('key')}",
                    metadata=metadata,
                    cover_url=cover_url,
                )
            )
        return candidates

    def normalize_metadata(self, _raw_data: Any) -> Metadata:
        # We already have _parse_search_results doing this, but to satisfy the ABC:
        return Metadata()

import logging
import re
from typing import Any

import httpx

from calibre_ai_auditor.providers.base import BaseProvider
from calibre_ai_auditor.storage.models import Candidate, Metadata

logger = logging.getLogger(__name__)


def resolve_hd_cover_url(thumbnail_url: str | None, volume_id: str | None = None) -> str | None:
    """Transforms a Google Books thumbnail URL into a crisp HD frontcover URL.

    Bypasses standard low-res thumbnails (zoom=1 or zoom=5) by:
    1. Upgrading http:// to https://
    2. Replacing zoom=1 / zoom=5 with zoom=0 (raw full resolution)
    3. Stripping curled page edge artifacts (&edge=curl)
    4. Supporting the publisher content CDN URL:
       https://books.google.com/books/publisher/content/images/frontcover/{volume_id}?fife=w1000-h1500
    """
    if not thumbnail_url and not volume_id:
        return None

    if thumbnail_url:
        # Upgrade scheme
        url = thumbnail_url.replace("http://", "https://")
        # Remove curled page edge noise
        url = url.replace("&edge=curl", "").replace("edge=curl&", "").replace("edge=curl", "")
        # Zoom bypass: zoom=0 requests the unscaled original scan
        if "zoom=1" in url:
            url = url.replace("zoom=1", "zoom=0")
        elif "zoom=5" in url:
            url = url.replace("zoom=5", "zoom=0")
        elif "zoom=" not in url and "?" in url:
            url += "&zoom=0"

        # If fife parameter is already present, boost to w1000-h1500
        if "fife=" in url:
            url = re.sub(r"fife=w\d+-h\d+", "fife=w1000-h1500", url)
        return url

    if volume_id:
        return f"https://books.google.com/books/publisher/content/images/frontcover/{volume_id}?fife=w1000-h1500"

    return None


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

            volume_id = item.get("id")
            images = volume_info.get("imageLinks", {})
            raw_cover = images.get("thumbnail") or images.get("smallThumbnail")
            hd_cover = resolve_hd_cover_url(raw_cover, volume_id=volume_id)

            candidates.append(
                Candidate(
                    candidate_id=f"google_books:{volume_id}",
                    provider=self.name,
                    provider_url=item.get("selfLink"),
                    metadata=metadata,
                    cover_url=hd_cover,
                )
            )
        return candidates

    def normalize_metadata(self, _raw_data: Any) -> Metadata:
        return Metadata()

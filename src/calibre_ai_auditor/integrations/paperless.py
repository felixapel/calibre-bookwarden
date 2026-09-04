import logging
import os
from pathlib import Path
from typing import Any, cast

import httpx

from calibre_ai_auditor.config.settings import Settings

logger = logging.getLogger(__name__)


class PaperlessBridge:
    """
    Disabled-by-default bridge for integrating with Paperless-ngx.
    Allows fetching document metadata or importing scanned books.
    """

    def __init__(self, settings: Settings):
        self.settings = settings.paperless
        self.enabled = self.settings.enabled
        self.base_url = self.settings.base_url.rstrip("/")
        self.token = os.environ.get(self.settings.token_env)

        if self.enabled and not self.token:
            logger.warning(
                f"Paperless-ngx is enabled, but '{self.settings.token_env}' environment "
                "variable is missing. The bridge will not be able to authenticate."
            )

    def _get_headers(self) -> dict[str, str]:
        if not self.token:
            return {}
        return {"Authorization": f"Token {self.token}"}

    async def get_document(self, document_id: int) -> dict[str, Any] | None:
        if not self.enabled:
            return None

        url = f"{self.base_url}/api/documents/{document_id}/"
        try:
            async with httpx.AsyncClient(timeout=10.0) as client:
                response = await client.get(url, headers=self._get_headers())
                response.raise_for_status()
                return cast(dict[str, Any], response.json())
        except Exception as e:
            logger.error(f"Failed to fetch document {document_id} from Paperless: {e}")
            return None

    async def get_document_types(self) -> dict[int, str]:
        if not self.enabled:
            return {}
        url = f"{self.base_url}/api/document_types/"
        try:
            async with httpx.AsyncClient(timeout=10.0) as client:
                response = await client.get(url, headers=self._get_headers())
                response.raise_for_status()
                results = response.json().get("results", [])
                return {item["id"]: item["name"] for item in results}
        except Exception as e:
            logger.error(f"Failed to fetch document types from Paperless: {e}")
            return {}

    async def fetch_candidate_documents(self) -> list[dict[str, Any]]:
        if not self.enabled:
            return []
        types_map = await self.get_document_types()
        name_to_id = {name.lower(): id for id, name in types_map.items()}

        target_ids = []
        for name in self.settings.import_document_types:
            if name.lower() in name_to_id:
                target_ids.append(name_to_id[name.lower()])

        if not target_ids:
            logger.info("No matching Paperless document types found or configured.")
            return []

        all_documents = []
        url = f"{self.base_url}/api/documents/"
        type_query = ",".join(map(str, target_ids))
        params = {"document_type__id__in": type_query}

        try:
            async with httpx.AsyncClient(timeout=10.0) as client:
                while url:
                    response = await client.get(url, headers=self._get_headers(), params=params)
                    response.raise_for_status()
                    data = response.json()
                    all_documents.extend(data.get("results", []))
                    url = data.get("next")
                    params = {}  # Clear params since query parameters are already part of the next url
            return all_documents
        except Exception as e:
            logger.error(f"Failed to fetch documents from Paperless: {e}")
            return all_documents

    async def download_document_file(self, document_id: int, output_dir: Path) -> Path | None:
        if not self.enabled:
            return None

        url = f"{self.base_url}/api/documents/{document_id}/download/"
        try:
            output_dir.mkdir(parents=True, exist_ok=True)
            async with (
                httpx.AsyncClient(timeout=30.0) as client,
                client.stream("GET", url, headers=self._get_headers()) as response,
            ):
                response.raise_for_status()
                filename = f"paperless_{document_id}.pdf"
                disp = response.headers.get("content-disposition", "")
                if "filename=" in disp:
                    parts = disp.split("filename=")
                    if len(parts) > 1:
                        raw_filename = parts[1].split(";")[0].strip("\"' ")
                        safe_base = Path(raw_filename).name
                        if safe_base and safe_base not in (".", ".."):
                            filename = safe_base

                file_path = output_dir / filename
                with open(file_path, "wb") as f:
                    async for chunk in response.aiter_bytes():
                        f.write(chunk)

                logger.info(f"Downloaded paperless document {document_id} to {file_path}")
                return file_path
        except Exception as e:
            logger.error(f"Failed to download document {document_id} from Paperless: {e}")
            return None

    async def test_connection(self) -> bool:
        if not self.enabled:
            return False

        url = f"{self.base_url}/api/documents/"
        try:
            async with httpx.AsyncClient(timeout=5.0) as client:
                # A simple request with limit=1 to verify authentication and connectivity
                response = await client.get(url, headers=self._get_headers(), params={"page_size": 1})
                response.raise_for_status()
                return True
        except Exception as e:
            logger.error(f"Failed to connect to Paperless-ngx: {e}")
            return False

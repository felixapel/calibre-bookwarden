import logging
from pathlib import Path
from typing import Any, cast

import httpx

logger = logging.getLogger(__name__)


class TikaClient:
    def __init__(
        self,
        enabled: bool = False,
        base_url: str = "http://tika:9998",
        timeout_seconds: int = 30,
        write_limit: int = 200000,
        max_embedded_resources: int = 5,
    ):
        self.enabled = enabled
        self.base_url = base_url.rstrip("/")
        self.timeout = timeout_seconds
        self.write_limit = write_limit
        self.max_embedded_resources = max_embedded_resources

    async def extract_text(self, file_path: Path) -> str | None:
        if not self.enabled:
            return None

        if not file_path.exists():
            logger.error(f"Tika extraction failed: File not found: {file_path}")
            return None

        url = f"{self.base_url}/tika"
        headers = {
            "Accept": "text/plain",
            "X-Tika-Skip-Embedded": str(self.max_embedded_resources == 0).lower(),
        }

        try:
            logger.info(f"Extracting text via Tika from {file_path}...")
            # We open the file and stream it to avoid loading huge files entirely in memory
            async with httpx.AsyncClient(timeout=self.timeout) as client:
                with open(file_path, "rb") as f:
                    response = await client.put(url, headers=headers, content=f)
                    response.raise_for_status()

                    text = response.text
                    # Truncate if it exceeds write limit
                    if len(text) > self.write_limit:
                        text = text[: self.write_limit]
                    return text
        except Exception as e:
            logger.error(f"Tika text extraction failed for {file_path}: {e}")
            return None

    async def extract_metadata(self, file_path: Path) -> dict[str, Any] | None:
        if not self.enabled:
            return None

        if not file_path.exists():
            return None

        url = f"{self.base_url}/rmeta"
        headers = {"Accept": "application/json"}

        try:
            logger.info(f"Extracting metadata via Tika from {file_path}...")
            async with httpx.AsyncClient(timeout=self.timeout) as client:
                with open(file_path, "rb") as f:
                    response = await client.put(url, headers=headers, content=f)
                    response.raise_for_status()

                    data = response.json()
                    if isinstance(data, list) and data:
                        return cast(dict[str, Any], data[0])
                    return None
        except Exception as e:
            logger.error(f"Tika metadata extraction failed for {file_path}: {e}")
            return None

    async def test_connection(self) -> bool:
        if not self.enabled:
            return False

        url = f"{self.base_url}/tika"
        try:
            async with httpx.AsyncClient(timeout=5.0) as client:
                # Just ping the endpoint
                response = await client.get(url)
                # Tika usually returns 200 OK with a welcome message on GET /tika
                return response.status_code == 200
        except Exception:
            return False

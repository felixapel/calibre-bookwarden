"""Deprecated mutation bridge and lock facades; only read-only OCR payload parsing remains supported."""

from __future__ import annotations

import logging
from dataclasses import dataclass
from pathlib import Path
from typing import Any

logger = logging.getLogger(__name__)


@dataclass
class LockInfo:
    pid: int
    hostname: str
    acquired_at: str
    operation: str


class CooperativeAdvisoryLock:
    """Deprecated facade that cannot claim to protect the supervised writer."""

    def __init__(self, lock_dir: Path | str, lock_filename: str = "calibre_bookwarden.lock", timeout_s: float = 10.0):
        self.lock_dir = Path(lock_dir)
        self.lock_file = self.lock_dir / lock_filename
        self.timeout_s = timeout_s
        self._acquired = False

    def acquire(self, operation: str = "curation") -> bool:
        """Reject the unreliable lock before it can create a lock file."""
        raise RuntimeError(
            "CooperativeAdvisoryLock is retired and does not protect the supervised writer; "
            "use the writer's serialization boundary instead."
        )

    def release(self) -> None:
        """Release local bookkeeping only; no filesystem lock exists."""
        self._acquired = False

    def __enter__(self) -> CooperativeAdvisoryLock:
        if not self.acquire():
            raise TimeoutError("Could not acquire Calibre Bookwarden cooperative lock")
        return self

    def __exit__(self, exc_type: Any, exc_val: Any, exc_tb: Any) -> None:
        self.release()


class HomelabBridge:
    """Coordinates hot-reloads and cache invalidation with Calibre-Web and Paperless."""

    def __init__(
        self,
        calibre_web_url: str = "http://localhost:8083",
        thumbnails_dir: Path | str | None = None,
    ):
        self.calibre_web_url = calibre_web_url.rstrip("/")
        self.thumbnails_dir = Path(thumbnails_dir) if thumbnails_dir else None

    def invalidate_book_thumbnails(self, book_id: int) -> int:
        """Reject thumbnail deletion before inspecting or mutating the filesystem."""
        raise PermissionError(
            "HomelabBridge mutation methods are retired; use an explicitly authorized, supervised integration instead."
        )

    def trigger_calibre_web_reconnect(self, timeout_s: float = 3.0) -> bool:
        """Reject reconnect requests before initiating network traffic."""
        raise PermissionError(
            "HomelabBridge mutation methods are retired; use an explicitly authorized, supervised integration instead."
        )

    def reuse_paperless_ocr(self, paperless_payload: dict[str, Any]) -> str | None:
        """Extracts existing OCR text from Paperless webhook payload, avoiding re-OCR."""
        doc = paperless_payload.get("document", {})
        content = doc.get("content") or paperless_payload.get("content")
        if content and isinstance(content, str) and content.strip():
            logger.info("Reusing existing Paperless-ngx OCR text (Zero-Waste processing)")
            return content.strip()
        return None

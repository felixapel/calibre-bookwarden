"""Read-only cover size diagnostics. Legacy in-place optimization is disabled."""

from __future__ import annotations

import logging
from pathlib import Path
from typing import Any

from PIL import Image

logger = logging.getLogger(__name__)

# Standard target dimensions for Calibre HD covers
TARGET_MAX_WIDTH = 1200
TARGET_MAX_HEIGHT = 1800
JPEG_QUALITY = 92
MAX_SAFE_PIXELS = 30_000_000
MAX_SAFE_FILE_SIZE = 10_000_000  # 10 MB


class CoverOptimizer:
    def __init__(self, library_path: Path | str):
        self.library_path = Path(library_path)

    def is_decompression_bomb(self, image_path: Path) -> tuple[bool, dict[str, Any]]:
        """Checks if an image is oversized or could cause a decompression bomb."""
        Image.MAX_IMAGE_PIXELS = 60_000_000
        if not image_path.exists():
            return False, {}
        size_bytes = image_path.stat().st_size
        try:
            with Image.open(image_path) as img:
                w, h = img.size
                pixels = w * h
                is_bomb = (pixels > MAX_SAFE_PIXELS) or (size_bytes > MAX_SAFE_FILE_SIZE)
                return is_bomb, {
                    "width": w,
                    "height": h,
                    "pixels": pixels,
                    "size_bytes": size_bytes,
                    "format": img.format,
                }
        except Image.DecompressionBombError as exc:
            return True, {"error": "decompression_bomb_detected", "detail": str(exc), "size_bytes": size_bytes}
        except Exception as exc:
            return False, {"error": str(exc)}

    def optimize_cover(
        self,
        cover_path: Path,
        backup: bool = True,
    ) -> tuple[bool, int, int]:
        """Reject in-place cover rewrites from the retired optimizer."""
        raise PermissionError(
            "CoverOptimizer mutation methods are retired; use the supervised apply writer for authorized changes."
        )

    def scan_and_optimize_all(self) -> dict[str, Any]:
        """Reject library-wide cover mutation before walking the library."""
        raise PermissionError(
            "CoverOptimizer mutation methods are retired; use the supervised apply writer for authorized changes."
        )

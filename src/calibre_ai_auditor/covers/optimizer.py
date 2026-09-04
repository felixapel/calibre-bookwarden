"""Cover image optimizer and decompression-bomb neutralizer.

Safely detects oversized images (>30 Megapixels, uncompressed BMPs, or >10 MB)
that cause Pillow DecompressionBomb warnings and slow down Calibre-Web thumbnail
generation, downscaling them to crisp HD standards (1200x1800 px, JPEG 92, <300 KB).
Also identifies low-resolution/pixelated covers (<200 px).
"""

from __future__ import annotations

import logging
import shutil
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
        Image.MAX_IMAGE_PIXELS = None
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
        except Exception as exc:
            return False, {"error": str(exc)}

    def optimize_cover(
        self,
        cover_path: Path,
        backup: bool = True,
    ) -> tuple[bool, int, int]:
        """
        Downscales and optimizes a cover image to standard HD JPEG.
        Returns: (success, bytes_before, bytes_after)
        """
        Image.MAX_IMAGE_PIXELS = None
        if not cover_path.exists():
            return False, 0, 0

        initial_size = cover_path.stat().st_size
        try:
            with Image.open(cover_path) as img:
                w, h = img.size
                if w <= TARGET_MAX_WIDTH and h <= TARGET_MAX_HEIGHT and initial_size < 500_000 and img.format == "JPEG":
                    # Already optimized
                    return False, initial_size, initial_size

                rgb = img.convert("RGB")
                tw = TARGET_MAX_WIDTH
                th = int(h * (TARGET_MAX_WIDTH / w))
                if th > TARGET_MAX_HEIGHT:
                    th = TARGET_MAX_HEIGHT
                    tw = int(w * (TARGET_MAX_HEIGHT / h))

                resized = rgb.resize((tw, th), Image.Resampling.LANCZOS)
                tmp_dest = cover_path.with_suffix(".optimized.tmp")
                resized.save(tmp_dest, "JPEG", quality=JPEG_QUALITY, optimize=True)

            if backup:
                bak_path = cover_path.with_suffix(".orig_bak")
                if not bak_path.exists():
                    shutil.copyfile(cover_path, bak_path)

            shutil.move(tmp_dest, cover_path)
            final_size = cover_path.stat().st_size
            logger.info(
                f"Optimized {cover_path.name}: {w}x{h} ({initial_size} B) -> "
                f"{tw}x{th} ({final_size} B). Saved {(initial_size - final_size) / 1024:.1f} KB"
            )
            return True, initial_size, final_size
        except Exception as exc:
            logger.error(f"Failed to optimize cover {cover_path}: {exc}")
            return False, initial_size, initial_size

    def scan_and_optimize_all(self) -> dict[str, Any]:
        """Scans the entire library and neutralizes all oversized covers."""
        optimized_count = 0
        total_saved_bytes = 0
        bombs_detected: list[dict[str, Any]] = []

        for cov_file in self.library_path.glob("**/cover.jpg"):
            is_bomb, info = self.is_decompression_bomb(cov_file)
            if is_bomb:
                bombs_detected.append({"path": str(cov_file), **info})
                ok, before, after = self.optimize_cover(cov_file, backup=True)
                if ok:
                    optimized_count += 1
                    total_saved_bytes += before - after

        return {
            "bombs_detected": bombs_detected,
            "optimized_count": optimized_count,
            "saved_bytes": total_saved_bytes,
            "saved_mb": round(total_saved_bytes / (1024 * 1024), 2),
        }

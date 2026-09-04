"""Spurious and Fake Cover Detector.

Identifies:
1. Calibre Default Generated Covers (generic pastel/brown SVG templates).
2. Interior Scanned Pages (white background with dense black body text mistaken for a cover).
3. Publisher boilerplate / title-page scans without art.
"""

from __future__ import annotations

import logging
import math
from dataclasses import dataclass
from pathlib import Path
from typing import Any

import numpy as np
from PIL import Image

logger = logging.getLogger(__name__)


@dataclass
class SpuriousCoverResult:
    is_spurious: bool
    defect_type: str | None  # "calibre_default_template", "interior_page_scan", "blank_canvas", None
    confidence: float  # 0.0 to 1.0
    details: dict[str, Any]
    entropy: float = 0.0


class SpuriousCoverDetector:
    def __init__(self, white_threshold: int = 240, text_density_threshold: float = 0.65):
        self.white_threshold = white_threshold
        self.text_density_threshold = text_density_threshold

    def inspect(self, image_path: Path | str, ocr_text: str | None = None) -> SpuriousCoverResult:
        path = Path(image_path)
        if not path.exists():
            return SpuriousCoverResult(
                is_spurious=False,
                defect_type=None,
                confidence=0.0,
                details={"error": "file_missing"},
            )

        try:
            with Image.open(path) as img:
                w, h = img.size
                rgb = img.convert("RGB")
                gray = img.convert("L")

                # Calculate Shannon information entropy (0.0 to 8.0)
                hist = gray.histogram()
                total_h = sum(hist)
                entropy = 0.0
                if total_h > 0:
                    for count in hist:
                        if count > 0:
                            p = count / total_h
                            entropy -= p * math.log2(p)
                entropy = round(entropy, 2)

                # 1. Check for Calibre Default Generated Template
                is_calibre, cal_conf, cal_details = self._check_calibre_default_template(rgb, ocr_text)
                if is_calibre:
                    return SpuriousCoverResult(
                        is_spurious=True,
                        defect_type="calibre_default_template",
                        confidence=cal_conf,
                        details=cal_details,
                        entropy=entropy,
                    )

                # 2. Blank canvas / solid color check (or entropy < 0.5)
                is_blank, blank_conf, blank_color = self._check_solid_or_blank(rgb)
                if is_blank:
                    return SpuriousCoverResult(
                        is_spurious=True,
                        defect_type="blank_canvas",
                        confidence=blank_conf,
                        details={"color": blank_color, "dimensions": (w, h)},
                        entropy=entropy,
                    )

                # 3. Check for Interior Scanned Page (white background + black text paragraph)
                is_interior, int_conf, int_details = self._check_interior_page_scan(gray, ocr_text, entropy)
                if is_interior:
                    return SpuriousCoverResult(
                        is_spurious=True,
                        defect_type="interior_page_scan",
                        confidence=int_conf,
                        details=int_details,
                        entropy=entropy,
                    )

                return SpuriousCoverResult(
                    is_spurious=False,
                    defect_type=None,
                    confidence=0.0,
                    details={"dimensions": (w, h)},
                    entropy=entropy,
                )
        except Exception as exc:
            logger.warning(f"Failed to inspect cover for spurious defects {path}: {exc}")
            return SpuriousCoverResult(
                is_spurious=False,
                defect_type=None,
                confidence=0.0,
                details={"error": str(exc)},
                entropy=0.0,
            )

    def _check_solid_or_blank(self, rgb: Image.Image) -> tuple[bool, float, tuple[int, int, int] | None]:
        # Downscale to 50x50
        small = rgb.resize((50, 50), Image.Resampling.BILINEAR)
        colors = small.getcolors(maxcolors=2500)
        if not colors:
            return False, 0.0, None

        # Most dominant color
        most_freq_count, most_freq_rgb = max(colors, key=lambda item: item[0])
        ratio = most_freq_count / 2500.0
        if ratio > 0.98:
            dom_rgb: tuple[int, int, int] | None = None
            if isinstance(most_freq_rgb, (tuple, list)) and len(most_freq_rgb) >= 3:
                dom_rgb = (int(most_freq_rgb[0]), int(most_freq_rgb[1]), int(most_freq_rgb[2]))
            return True, 0.99, dom_rgb
        return False, 0.0, None

    def _check_calibre_default_template(
        self,
        rgb: Image.Image,
        ocr_text: str | None,
    ) -> tuple[bool, float, dict[str, Any]]:
        """Calibre default generated covers typically have a solid border, gradient or plain rectangle."""
        if ocr_text:
            ocr_lower = ocr_text.lower()
            if "generated by calibre" in ocr_lower or "calibre quick cover" in ocr_lower:
                return True, 0.98, {"detected_marker": "calibre_text"}

        return False, 0.0, {}

    def _check_interior_page_scan(
        self,
        gray: Image.Image,
        ocr_text: str | None,
        entropy: float = 0.0,
    ) -> tuple[bool, float, dict[str, Any]]:
        """Detects if an image is predominantly a white page with lines of body text."""
        # Downsample to 100x150
        small = gray.resize((100, 150), Image.Resampling.BILINEAR)
        arr = np.asarray(small, dtype=np.uint8)
        total = arr.size

        white_pixels = int(np.count_nonzero(arr >= self.white_threshold))
        white_ratio = white_pixels / total

        # If >80% of the image is stark white/paper background
        if white_ratio > 0.80:
            dark_pixels = int(np.count_nonzero(arr < 80))
            dark_ratio = dark_pixels / total
            # Typical page scan has 1.5% to 22% text ink and low-to-moderate entropy (1.2 to 3.5)
            if 0.015 < dark_ratio < 0.22:
                confidence = 0.85
                if 1.2 <= entropy <= 3.5:
                    confidence = 0.92
                if ocr_text:
                    interior_markers = ["contents", "chapter", "preface", "index", "all rights reserved", "printed in"]
                    if any(m in ocr_text.lower() for m in interior_markers):
                        confidence = 0.98
                return (
                    True,
                    confidence,
                    {
                        "white_ratio": round(white_ratio, 3),
                        "dark_ratio": round(dark_ratio, 3),
                        "entropy": entropy,
                    },
                )

        return False, 0.0, {"entropy": entropy}

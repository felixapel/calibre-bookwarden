"""Cover Quality Scorer (CQS 0-100).

Evaluates cover images on resolution, aspect ratio, sharpness, contrast,
and aesthetic integrity. Flags low-resolution thumbnails, blurry scans,
and fatal anomalies with deterministic mathematical metrics.
"""

from __future__ import annotations

import logging
import math
from dataclasses import dataclass
from pathlib import Path

import numpy as np
from PIL import Image

logger = logging.getLogger(__name__)

# Ideal dimensions for Calibre e-reader displays
IDEAL_WIDTH = 1200
IDEAL_HEIGHT = 1800
IDEAL_ASPECT_RATIO = 1.5  # height / width (2:3 standard book proportion)


@dataclass
class CoverScoreResult:
    cqs: int  # 0 to 100
    tier: str  # "Tier A", "Tier B", "Tier C", "Tier D"
    width: int
    height: int
    aspect_ratio: float
    dimension_score: float  # 0 to 25
    sharpness_score: float  # 0 to 25
    contrast_score: float  # 0 to 25
    cleanliness_score: float  # 0 to 25
    penalties: list[str]
    fatal_defects: list[str]
    is_actionable: bool  # True if cover should be replaced
    entropy: float = 0.0  # Shannon information entropy (0.0 - 8.0)


class CoverQualityScorer:
    """Deterministic Cover Quality Scorer without requiring GPU or heavy dependencies."""

    def __init__(self, min_acceptable_width: int = 400, min_acceptable_height: int = 600):
        self.min_width = min_acceptable_width
        self.min_height = min_acceptable_height

    def evaluate(self, image_path: Path | str, ocr_text: str | None = None) -> CoverScoreResult:
        path = Path(image_path)
        if not path.exists():
            return CoverScoreResult(
                cqs=0,
                tier="Tier D",
                width=0,
                height=0,
                aspect_ratio=0.0,
                dimension_score=0.0,
                sharpness_score=0.0,
                contrast_score=0.0,
                cleanliness_score=0.0,
                penalties=["file_missing"],
                fatal_defects=["file_missing"],
                is_actionable=True,
            )

        penalties: list[str] = []
        fatal_defects: list[str] = []

        try:
            with Image.open(path) as img:
                w, h = img.size
                gray = img.convert("L")

                # 1. Dimension & Aspect Ratio Score (0 - 25)
                dim_score = self._compute_dimension_score(w, h, penalties, fatal_defects)

                # 2. Sharpness / Blur Score (0 - 25)
                sharpness_score = self._compute_sharpness_score(gray, penalties)

                # 3. Contrast & Dynamic Range Score (0 - 25)
                contrast_score = self._compute_contrast_score(gray, penalties)

                # 4. Cleanliness & Watermarks (0 - 25)
                cleanliness_score = self._compute_cleanliness_score(ocr_text, penalties)

                # 5. Shannon Entropy calculation (0.0 to 8.0 bits)
                entropy = round(self.compute_shannon_entropy(gray), 2)
                if entropy < 1.5 and "monochrome_or_blank_wash" not in penalties:
                    penalties.append("monochrome_or_blank_wash")

                # Total raw score
                total_raw = dim_score + sharpness_score + contrast_score + cleanliness_score

                # Deductions
                penalty_points = len(penalties) * 5
                final_score = max(0, min(100, int(total_raw - penalty_points)))

                # Apply fatal floor
                if fatal_defects:
                    final_score = min(final_score, 35)

                # Classify Tier
                if final_score >= 80:
                    tier = "Tier A"
                elif final_score >= 65:
                    tier = "Tier B"
                elif final_score >= 45:
                    tier = "Tier C"
                else:
                    tier = "Tier D"

                aspect_ratio = round(h / w, 2) if w > 0 else 0.0

                return CoverScoreResult(
                    cqs=final_score,
                    tier=tier,
                    width=w,
                    height=h,
                    aspect_ratio=aspect_ratio,
                    dimension_score=round(dim_score, 1),
                    sharpness_score=round(sharpness_score, 1),
                    contrast_score=round(contrast_score, 1),
                    cleanliness_score=round(cleanliness_score, 1),
                    penalties=penalties,
                    fatal_defects=fatal_defects,
                    is_actionable=final_score < 60 or bool(fatal_defects),
                    entropy=entropy,
                )
        except Exception as exc:
            logger.error(f"Error scoring cover {path}: {exc}")
            return CoverScoreResult(
                cqs=0,
                tier="Tier D",
                width=0,
                height=0,
                aspect_ratio=0.0,
                dimension_score=0.0,
                sharpness_score=0.0,
                contrast_score=0.0,
                cleanliness_score=0.0,
                penalties=[f"read_error: {exc}"],
                fatal_defects=["corrupt_image"],
                is_actionable=True,
                entropy=0.0,
            )

    def _compute_dimension_score(
        self,
        w: int,
        h: int,
        penalties: list[str],
        fatal_defects: list[str],
    ) -> float:
        if w < 100 or h < 150:
            fatal_defects.append("thumbnail_micro_cover")
            return 2.0

        if w < self.min_width or h < self.min_height:
            penalties.append("low_resolution")
            base = 10.0 * min(w / self.min_width, h / self.min_height)
        else:
            res_ratio = min(1.0, (w * h) / (IDEAL_WIDTH * IDEAL_HEIGHT))
            base = 15.0 + (10.0 * res_ratio)

        # Aspect ratio penalty (books are portrait 1:1.2 to 1:1.8)
        ratio = h / w if w > 0 else 1.0
        if ratio < 1.1:
            penalties.append("square_or_landscape_ratio")
            base = max(5.0, base - 8.0)
        elif ratio > 2.0:
            penalties.append("overly_tall_ratio")
            base = max(5.0, base - 5.0)

        return min(25.0, base)

    @staticmethod
    def compute_shannon_entropy(gray: Image.Image) -> float:
        """Calculates Shannon information entropy (0.0 to 8.0 bits) from 256-bin histogram."""
        histogram = gray.histogram()
        total_pixels = sum(histogram)
        if total_pixels == 0:
            return 0.0
        entropy = 0.0
        for count in histogram:
            if count > 0:
                p = count / total_pixels
                entropy -= p * math.log2(p)
        return float(entropy)

    def _compute_sharpness_score(self, gray: Image.Image, penalties: list[str]) -> float:
        """Vectorized high-frequency sharpness variance using NumPy array slicing."""
        small = gray.resize((200, 300), Image.Resampling.BILINEAR)
        arr = np.asarray(small, dtype=np.int16)
        # Vectorized absolute difference of adjacent horizontal pixels
        diffs = np.abs(arr[:, 1:] - arr[:, :-1])
        avg_grad = float(np.mean(diffs))

        if avg_grad < 4.0:
            penalties.append("severe_blur_or_flat_wash")
            return 5.0
        elif avg_grad < 8.0:
            penalties.append("mild_blur")
            return 14.0

        score = min(25.0, 15.0 + (avg_grad - 8.0) * 1.0)
        return score

    def _compute_contrast_score(self, gray: Image.Image, penalties: list[str]) -> float:
        """Evaluates histogram standard deviation to ensure adequate dynamic range."""
        histogram = gray.histogram()
        total_pixels = sum(histogram)
        if total_pixels == 0:
            return 0.0

        mean = sum(i * count for i, count in enumerate(histogram)) / total_pixels
        variance = sum(((i - mean) ** 2) * count for i, count in enumerate(histogram)) / total_pixels
        std_dev = math.sqrt(variance)

        if std_dev < 20.0:
            penalties.append("washed_out_low_contrast")
            return 6.0

        return min(25.0, (std_dev / 75.0) * 25.0)

    def _compute_cleanliness_score(self, ocr_text: str | None, penalties: list[str]) -> float:
        if not ocr_text:
            return 25.0

        text_lower = ocr_text.lower()
        bad_keywords = [
            ("z-library", "piracy_watermark"),
            ("libgen", "piracy_watermark"),
            ("welib.org", "scraper_watermark"),
            ("oceanofpdf", "scraper_watermark"),
            ("bestseller", "promotional_sticker"),
            ("now a netflix", "promotional_sticker"),
            ("new york times bestseller", "promotional_sticker"),
        ]

        score = 25.0
        for kw, pen in bad_keywords:
            if kw in text_lower:
                penalties.append(f"{pen}:{kw}")
                score = max(0.0, score - 8.0)

        return score

    score_image = evaluate

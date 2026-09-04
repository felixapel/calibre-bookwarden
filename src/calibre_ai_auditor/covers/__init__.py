"""Cover optimization, forensic extraction, and multimodal vision validation."""

from calibre_ai_auditor.covers.extractor import extract_cover_from_epub
from calibre_ai_auditor.covers.optimizer import CoverOptimizer
from calibre_ai_auditor.covers.scorer import CoverQualityScorer, CoverScoreResult
from calibre_ai_auditor.covers.spurious_detector import SpuriousCoverDetector, SpuriousCoverResult

__all__ = [
    "CoverOptimizer",
    "CoverQualityScorer",
    "CoverScoreResult",
    "SpuriousCoverDetector",
    "SpuriousCoverResult",
    "extract_cover_from_epub",
]

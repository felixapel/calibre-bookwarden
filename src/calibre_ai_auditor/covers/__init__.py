"""Cover optimization, forensic extraction, and multimodal vision validation."""

from calibre_ai_auditor.covers.cache import LocalCoverAuditCache
from calibre_ai_auditor.covers.extractor import (
    UnifiedCoverExtractor,
    extract_cover_from_epub,
    extract_native_cover,
)
from calibre_ai_auditor.covers.optimizer import CoverOptimizer
from calibre_ai_auditor.covers.scorer import CoverQualityScorer, CoverScoreResult
from calibre_ai_auditor.covers.spurious_detector import SpuriousCoverDetector, SpuriousCoverResult

__all__ = [
    "CoverOptimizer",
    "CoverQualityScorer",
    "CoverScoreResult",
    "LocalCoverAuditCache",
    "SpuriousCoverDetector",
    "SpuriousCoverResult",
    "UnifiedCoverExtractor",
    "extract_cover_from_epub",
    "extract_native_cover",
]

"""Cover optimization, forensic extraction, and multimodal vision validation."""

from calibre_ai_auditor.covers.extractor import extract_cover_from_epub
from calibre_ai_auditor.covers.optimizer import CoverOptimizer

__all__ = ["CoverOptimizer", "extract_cover_from_epub"]

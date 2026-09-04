"""Intelligent library curation modules for Calibre libraries.

Includes:
- SeriesGapHunter: Identifies missing volumes in series and sagas.
- DuplicateConsolidator: Identifies multi-format duplicate records and cross-language editions.
"""

from calibre_ai_auditor.curation.duplicates import DuplicateCluster, DuplicateConsolidator
from calibre_ai_auditor.curation.series import SeriesGap, SeriesGapHunter

__all__ = ["SeriesGap", "SeriesGapHunter", "DuplicateCluster", "DuplicateConsolidator"]

"""Synthetic fixture library for v1.0 content-verification engine tests."""

from .gold_truth import VERIFICATION_CONTRACT
from .synthetic_library.gold_truth import (
    SYNTHETIC_FIXTURES,
    all_fixture_ids,
    fixture_count,
    get_fixture,
)

__all__ = [
    "SYNTHETIC_FIXTURES",
    "get_fixture",
    "fixture_count",
    "all_fixture_ids",
    "VERIFICATION_CONTRACT",
]

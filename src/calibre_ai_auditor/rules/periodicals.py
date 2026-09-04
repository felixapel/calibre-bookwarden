"""Rules and automated curator for periodicals, newspapers, and hemeroteca."""

from __future__ import annotations

import re
from typing import NamedTuple


class PeriodicalRule(NamedTuple):
    title_pattern: re.Pattern[str]
    canonical_author: str
    canonical_sort: str
    default_rating: int  # e.g. 7 for 3.5 stars
    tags: list[str]


PERIODICAL_RULES: list[PeriodicalRule] = [
    PeriodicalRule(
        title_pattern=re.compile(r"^the economist", re.IGNORECASE),
        canonical_author="The Economist",
        canonical_sort="Economist, The",
        default_rating=7,
        tags=["Hemeroteca", "Periodicals", "News"],
    ),
    PeriodicalRule(
        title_pattern=re.compile(r"^financial times", re.IGNORECASE),
        canonical_author="Financial Times",
        canonical_sort="Financial Times",
        default_rating=7,
        tags=["Hemeroteca", "Periodicals", "News"],
    ),
    PeriodicalRule(
        title_pattern=re.compile(r"^(spiegel online|der spiegel)", re.IGNORECASE),
        canonical_author="Der Spiegel",
        canonical_sort="Spiegel, Der",
        default_rating=7,
        tags=["Hemeroteca", "Periodicals", "News"],
    ),
    PeriodicalRule(
        title_pattern=re.compile(r"^the new yorker", re.IGNORECASE),
        canonical_author="The New Yorker",
        canonical_sort="New Yorker, The",
        default_rating=7,
        tags=["Hemeroteca", "Periodicals", "Culture"],
    ),
    PeriodicalRule(
        title_pattern=re.compile(r"^nature\b", re.IGNORECASE),
        canonical_author="Nature",
        canonical_sort="Nature",
        default_rating=7,
        tags=["Hemeroteca", "Periodicals", "Science"],
    ),
    PeriodicalRule(
        title_pattern=re.compile(r"^mit tr|^mit technology review", re.IGNORECASE),
        canonical_author="MIT Technology Review",
        canonical_sort="MIT Technology Review",
        default_rating=7,
        tags=["Hemeroteca", "Periodicals", "Technology"],
    ),
]


def match_periodical(title: str | None, current_author: str | None = None) -> PeriodicalRule | None:
    """Matches a book title/author against known periodical patterns."""
    if not title:
        return None
    for rule in PERIODICAL_RULES:
        if rule.title_pattern.search(title):
            return rule
    if current_author and current_author.lower() in ("calibre", "unknown"):
        for rule in PERIODICAL_RULES:
            if rule.canonical_author.lower() in title.lower():
                return rule
    return None

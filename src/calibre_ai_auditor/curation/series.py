"""Series Gap Hunter for Calibre libraries.

Identifies missing volumes in series, sagas, and multi-volume sets.
Detects missing integer volumes, missing leading volumes, and generates
actionable acquisition wishlists.
"""

from __future__ import annotations

import collections
import logging
import sqlite3
from dataclasses import dataclass
from typing import Any

logger = logging.getLogger(__name__)

# Maximum plausible gap span to prevent runaway memory allocation on corrupted index metadata
MAX_GAP_SPAN = 200


@dataclass
class SeriesGap:
    series_id: int
    series_name: str
    authors: str
    total_owned: int
    owned_indices: list[float]
    missing_indices: list[int]
    books: list[dict[str, Any]]


class SeriesGapHunter:
    """Detects missing volumes in book series."""

    def __init__(self, conn: sqlite3.Connection):
        self.conn = conn
        if self.conn.row_factory is None:
            self.conn.row_factory = sqlite3.Row

    def find_all_gaps(self, min_owned_threshold: int = 1) -> list[SeriesGap]:
        """Scans the database and returns all series with missing intermediate or leading volumes."""
        c = self.conn.cursor()
        query = """
            SELECT s.id as series_id, s.name as series_name,
                   b.id as book_id, b.title, b.series_index,
                   (
                       SELECT GROUP_CONCAT(auth_name, ' & ')
                       FROM (
                           SELECT a.name AS auth_name
                           FROM books_authors_link bal
                           JOIN authors a ON a.id = bal.author
                           WHERE bal.book = b.id
                           ORDER BY bal.id ASC
                       )
                   ) as authors
            FROM series s
            JOIN books_series_link bsl ON bsl.series = s.id
            JOIN books b ON b.id = bsl.book
            ORDER BY s.id, b.series_index ASC
        """
        c.execute(query)
        rows = c.fetchall()

        series_map: dict[int, dict[str, Any]] = collections.defaultdict(
            lambda: {"name": "", "authors": set(), "books": []}
        )

        for r in rows:
            sid = r["series_id"]
            series_map[sid]["name"] = r["series_name"]
            if r["authors"]:
                for a_name in r["authors"].split(" & "):
                    clean_a = a_name.strip()
                    if clean_a:
                        series_map[sid]["authors"].add(clean_a)
            series_map[sid]["books"].append(
                {
                    "book_id": r["book_id"],
                    "title": r["title"],
                    "series_index": float(r["series_index"] or 1.0),
                }
            )

        gaps: list[SeriesGap] = []

        for sid, sdata in series_map.items():
            books = sdata["books"]
            if len(books) < min_owned_threshold:
                continue

            indices = sorted({b["series_index"] for b in books})
            if not indices:
                continue

            # Identify integer gaps
            int_indices = {int(idx) for idx in indices if idx.is_integer() and idx >= 1}
            if not int_indices:
                continue

            min_idx = min(int_indices)
            max_idx = max(int_indices)

            # If user has only 1 book and it's volume 1, no gap known yet
            if len(int_indices) == 1 and min_idx == 1:
                continue

            # Avoid memory explosion on corrupted/unreasonable index numbers (e.g. index 99999)
            if (max_idx - min_idx) > MAX_GAP_SPAN:
                logger.warning(
                    f"Series '{sdata['name']}' has abnormal volume range [{min_idx}, {max_idx}], skipping gap search."
                )
                continue

            # Expected full sequence from 1 to max_idx
            expected_range = set(range(1, max_idx + 1))
            missing = sorted(expected_range - int_indices)

            if missing:
                authors_str = " & ".join(sorted(sdata["authors"])) if sdata["authors"] else "Unknown"
                gaps.append(
                    SeriesGap(
                        series_id=sid,
                        series_name=sdata["name"],
                        authors=authors_str,
                        total_owned=len(books),
                        owned_indices=indices,
                        missing_indices=missing,
                        books=books,
                    )
                )

        gaps.sort(key=lambda g: len(g.missing_indices), reverse=True)
        return gaps

    def export_wishlist(self, gaps: list[SeriesGap]) -> list[dict[str, Any]]:
        """Transforms series gaps into a structured book acquisition wishlist."""
        wishlist: list[dict[str, Any]] = []
        for gap in gaps:
            for vol in gap.missing_indices:
                wishlist.append(
                    {
                        "series": gap.series_name,
                        "series_id": gap.series_id,
                        "volume": vol,
                        "authors": gap.authors,
                        "query": f"{gap.series_name} #{vol} {gap.authors}",
                    }
                )
        return wishlist

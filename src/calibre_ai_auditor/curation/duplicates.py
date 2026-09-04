"""FRBR Multi-Format Duplicate Consolidator and Cross-Language Edition Linker.

Identifies:
1. Multi-format intra-library duplicates (e.g., Book ID 100 has EPUB, Book ID 101 has PDF of the same title & author).
2. Shared ISBN collisions across records.
3. Cross-language translations by the same author sharing FRBR work-level identifiers or title alignments.
"""

from __future__ import annotations

import collections
import logging
import re
import sqlite3
from dataclasses import dataclass
from typing import Any

logger = logging.getLogger(__name__)


_ARTICLE_RE = re.compile(r"^(?:(?:the|a|an|el|la|los|las|un|una|der|die|das)\s+)+", re.IGNORECASE)
DUMMY_ISBNS = {
    "1234567890", "0123456789", "9876543210", "0987654321",
    "1234567890123", "9781234567890", "9780000000000",
}


def _normalize_str(s: str | None) -> str:
    if not s:
        return ""
    # Strip non-alphanumeric except spaces, +, # (preserving C++, C#, etc.), explicitly remove underscores
    cleaned = re.sub(r"[^a-zA-Z0-9\s+#]", " ", s).lower().strip()
    # Strip leading articles repeatedly
    cleaned = _ARTICLE_RE.sub("", cleaned).strip()
    return re.sub(r"\s+", " ", cleaned)


def is_valid_isbn10(isbn: str) -> bool:
    """Validates ISBN-10 with standard modulo-11 check digit."""
    if len(isbn) != 10:
        return False
    if not isbn[:9].isdigit():
        return False
    if not (isbn[9].isdigit() or isbn[9] == "X"):
        return False
    total = sum((10 - i) * (10 if c == "X" else int(c)) for i, c in enumerate(isbn))
    return total % 11 == 0


def is_valid_isbn13(isbn: str) -> bool:
    """Validates ISBN-13 with standard modulo-10 check digit."""
    if len(isbn) != 13 or not isbn.isdigit():
        return False
    total = sum(int(c) * (1 if i % 2 == 0 else 3) for i, c in enumerate(isbn))
    return total % 10 == 0


def is_valid_isbn(isbn: str, strict_checksum: bool = False) -> bool:
    """Validates ISBN structure and optionally check digit.

    Rejects malformed strings, misplaced 'X', dummy test sequences, and repeating characters.
    """
    clean = re.sub(r"[^\dX]", "", isbn.upper())
    if len(clean) not in (10, 13):
        return False
    # Reject dummy repeating sequences like 0000000000 or 1111111111111
    if len(set(clean)) <= 1:
        return False
    # Reject common sequential test ladders
    if clean in DUMMY_ISBNS:
        return False
    # 'X' is only valid as the 10th character in ISBN-10
    if "X" in clean:
        if len(clean) != 10 or clean[9] != "X":
            return False
    if strict_checksum:
        if len(clean) == 10:
            return is_valid_isbn10(clean)
        elif len(clean) == 13:
            return is_valid_isbn13(clean)
    return True


@dataclass
class DuplicateCluster:
    cluster_type: str  # "multi_format_duplicate", "isbn_collision", "cross_language_work"
    confidence: float  # 0.0 to 1.0
    primary_book_id: int
    duplicate_book_ids: list[int]
    title: str
    author: str
    formats_by_book: dict[int, list[str]]
    recommendation: str  # "MERGE_FORMATS", "LINK_EDITIONS", "INSPECT_MANUALLY"
    details: dict[str, Any]


class DuplicateConsolidator:
    """Detects multi-format duplicates and cross-language work clusters."""

    def __init__(self, conn: sqlite3.Connection):
        self.conn = conn
        if self.conn.row_factory is None:
            self.conn.row_factory = sqlite3.Row

    def find_multi_format_duplicates(self) -> list[DuplicateCluster]:
        """Detects books that share identical title + author or identical ISBN but have different formats."""
        c = self.conn.cursor()

        # Query all books with author, isbn, and formats with deterministic author ordering
        query = """
            SELECT b.id as book_id, b.title,
                   (
                       SELECT GROUP_CONCAT(auth_name, ' & ')
                       FROM (
                           SELECT a.name AS auth_name
                           FROM books_authors_link bal
                           JOIN authors a ON a.id = bal.author
                           WHERE bal.book = b.id
                           ORDER BY bal.id ASC
                       )
                   ) as authors,
                   (
                       SELECT GROUP_CONCAT(d.format, ',')
                       FROM data d
                       WHERE d.book = b.id
                   ) as formats,
                   (
                       SELECT val FROM identifiers i
                       WHERE i.book = b.id AND LOWER(i.type) = 'isbn'
                       LIMIT 1
                   ) as isbn
            FROM books b
            ORDER BY b.id ASC
        """
        c.execute(query)
        rows = c.fetchall()

        # 1. Group by clean (title, author)
        title_author_groups: dict[tuple[str, str], list[dict[str, Any]]] = collections.defaultdict(list)
        # 2. Group by ISBN
        isbn_groups: dict[str, list[dict[str, Any]]] = collections.defaultdict(list)

        for r in rows:
            bid = r["book_id"]
            title = r["title"] or ""
            authors = r["authors"] or ""
            formats_str = r["formats"] or ""
            formats = [fmt.strip().upper() for fmt in formats_str.split(",") if fmt.strip()]
            isbn = r["isbn"]

            entry = {
                "book_id": bid,
                "title": title,
                "authors": authors,
                "formats": formats,
                "isbn": isbn,
            }

            norm_t = _normalize_str(title)
            norm_a = _normalize_str(authors)

            if norm_t and norm_a:
                title_author_groups[(norm_t, norm_a)].append(entry)

            if isbn:
                clean_isbn = re.sub(r"[^\dX]", "", isbn.upper())
                if is_valid_isbn(clean_isbn):
                    isbn_groups[clean_isbn].append(entry)

        clusters: list[DuplicateCluster] = []
        handled_pairs: set[tuple[int, ...]] = set()

        # Process title + author duplicates
        for (norm_t, norm_a), entries in title_author_groups.items():
            if len(entries) > 1:
                bids = tuple(sorted(e["book_id"] for e in entries))
                if bids in handled_pairs:
                    continue
                handled_pairs.add(bids)

                primary = entries[0]
                duplicates = entries[1:]
                formats_map = {e["book_id"]: e["formats"] for e in entries}

                # Check if formats are complementary (e.g. EPUB in one, PDF in another)
                all_formats = [fmt for fmts in formats_map.values() for fmt in fmts]
                unique_formats = set(all_formats)
                is_complementary = bool(all_formats) and (len(all_formats) == len(unique_formats))

                rec = "MERGE_FORMATS" if is_complementary else "INSPECT_MANUALLY"
                confidence = 0.98 if is_complementary else 0.85

                clusters.append(
                    DuplicateCluster(
                        cluster_type="multi_format_duplicate",
                        confidence=confidence,
                        primary_book_id=primary["book_id"],
                        duplicate_book_ids=[d["book_id"] for d in duplicates],
                        title=primary["title"],
                        author=primary["authors"],
                        formats_by_book=formats_map,
                        recommendation=rec,
                        details={"norm_title": norm_t, "norm_author": norm_a},
                    )
                )

        # Process ISBN collisions
        for isbn, entries in isbn_groups.items():
            if len(entries) > 1:
                bids = tuple(sorted(e["book_id"] for e in entries))
                if bids in handled_pairs:
                    continue
                handled_pairs.add(bids)

                primary = entries[0]
                duplicates = entries[1:]
                formats_map = {e["book_id"]: e["formats"] for e in entries}

                # Check if formats are complementary
                all_formats = [fmt for fmts in formats_map.values() for fmt in fmts]
                unique_formats = set(all_formats)
                is_complementary = bool(all_formats) and (len(all_formats) == len(unique_formats))

                rec = "MERGE_FORMATS" if is_complementary else "INSPECT_MANUALLY"
                confidence = 0.99 if is_complementary else 0.88

                clusters.append(
                    DuplicateCluster(
                        cluster_type="isbn_collision",
                        confidence=confidence,
                        primary_book_id=primary["book_id"],
                        duplicate_book_ids=[d["book_id"] for d in duplicates],
                        title=primary["title"],
                        author=primary["authors"],
                        formats_by_book=formats_map,
                        recommendation=rec,
                        details={"isbn": isbn},
                    )
                )

        return clusters

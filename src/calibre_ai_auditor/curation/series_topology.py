"""FRBR Deduplication and Advanced Series Topology Engine.

Provides:
1. FRBR 4-tier categorization:
   - Clones: Exact byte-for-byte duplicates (same file hash)
   - Multiformat: Same intellectual manifestation across formats (EPUB + PDF of identical book)
   - Editions: Different editions / translations of the same work
   - Omnibus: Compilations containing individual books already present in the library
2. Graph-based clustering using Disjoint Set Union (DSU / Connected Components)
3. Fractional series analysis supporting novellas and prequels (e.g. Vol 0.5, 1.5, 2.5)
"""

from __future__ import annotations

import logging
import math
import re
import sqlite3
from collections import defaultdict
from dataclasses import dataclass, field
from enum import StrEnum
from typing import Any

from calibre_ai_auditor.curation.entity_cleanser import EntityCleanser

logger = logging.getLogger(__name__)


class DuplicateType(StrEnum):
    CLONE = "CLONE"  # Exact same file hash / identical bytes
    MULTIFORMAT = "MULTIFORMAT"  # Same work & edition in different formats (EPUB + PDF)
    EDITION = "EDITION"  # Different edition / translation of same work
    OMNIBUS = "OMNIBUS"  # Compilation / anthology containing single work


@dataclass
class BookNode:
    book_id: int
    title: str
    cleaned_title: str
    author: str
    author_sort: str
    isbn: str | None
    formats: list[str]
    paths: list[str]
    file_hashes: list[str] = field(default_factory=list)


@dataclass
class DuplicateCluster:
    cluster_id: str
    cluster_type: DuplicateType
    canonical_book_id: int
    member_book_ids: list[int]
    titles: list[str]
    authors: list[str]
    formats: list[str]
    confidence: float
    recommendation: str


@dataclass
class AdvancedSeriesGap:
    series_id: int
    series_name: str
    authors: str
    owned_indices: list[float]
    missing_integer_indices: list[int]
    detected_fractional_indices: list[float]
    gap_summary: str


class DisjointSetUnion:
    """Disjoint Set Union (Union-Find) for connected components without external dependencies."""

    def __init__(self) -> None:
        self.parent: dict[int, int] = {}
        self.rank: dict[int, int] = {}

    def find(self, item: int) -> int:
        if item not in self.parent:
            self.parent[item] = item
            self.rank[item] = 0
            return item
        if self.parent[item] != item:
            self.parent[item] = self.find(self.parent[item])
        return self.parent[item]

    def union(self, x: int, y: int) -> None:
        root_x = self.find(x)
        root_y = self.find(y)
        if root_x == root_y:
            return
        if self.rank[root_x] < self.rank[root_y]:
            self.parent[root_x] = root_y
        elif self.rank[root_x] > self.rank[root_y]:
            self.parent[root_y] = root_x
        else:
            self.parent[root_y] = root_x
            self.rank[root_x] += 1


class SeriesTopologyEngine:
    """Advanced series gap hunter supporting fractional novellas and prequels."""

    @classmethod
    def analyze_series_gaps(cls, conn: sqlite3.Connection) -> list[AdvancedSeriesGap]:
        cursor = conn.cursor()
        query = """
            SELECT s.id as series_id, s.name as series_name,
                   b.id as book_id, b.title, b.series_index,
                   (SELECT GROUP_CONCAT(a.name, ' & ')
                    FROM books_authors_link bal
                    JOIN authors a ON a.id = bal.author
                    WHERE bal.book = b.id) as author_name
            FROM series s
            JOIN books_series_link bsl ON bsl.series = s.id
            JOIN books b ON b.id = bsl.book
            ORDER BY s.id, b.series_index ASC
        """
        rows = cursor.execute(query).fetchall()

        series_map: dict[int, dict[str, Any]] = defaultdict(lambda: {"name": "", "authors": "", "indices": []})

        for row in rows:
            sid = row[0]
            sname = row[1]
            try:
                idx = float(row[4] if row[4] is not None else 1.0)
            except (TypeError, ValueError):
                logger.warning("Skipping non-numeric series index for series %s", sid)
                continue
            if not math.isfinite(idx):
                logger.warning("Skipping non-finite series index for series %s", sid)
                continue
            auth = row[5] or ""
            series_map[sid]["name"] = sname
            if not series_map[sid]["authors"]:
                series_map[sid]["authors"] = auth
            series_map[sid]["indices"].append(idx)

        gaps: list[AdvancedSeriesGap] = []
        for sid, data in series_map.items():
            indices = sorted(set(data["indices"]))
            if not indices:
                continue

            integer_indices = [int(i) for i in indices if i.is_integer()]
            fractional_indices = [i for i in indices if not i.is_integer()]

            missing_integers: list[int] = []
            if integer_indices:
                min_vol = min(integer_indices)
                max_vol = max(integer_indices)
                # Check missing leading volumes if min > 1
                if 1 < min_vol <= 5:
                    missing_integers.extend(range(1, min_vol))
                # A corrupt index must not create an unbounded report or loop.
                if max_vol - min_vol > 1_000:
                    logger.warning("Skipping implausibly broad series span for series %s", sid)
                    continue
                # Check internal missing volumes
                for expected in range(min_vol, max_vol + 1):
                    if expected not in integer_indices:
                        missing_integers.append(expected)

            if missing_integers:
                summary = f"Missing volumes: {', '.join(str(m) for m in missing_integers)}"
                if fractional_indices:
                    summary += f" (Contains side novellas: {', '.join(str(f) for f in fractional_indices)})"

                gaps.append(
                    AdvancedSeriesGap(
                        series_id=sid,
                        series_name=data["name"],
                        authors=data["authors"],
                        owned_indices=indices,
                        missing_integer_indices=missing_integers,
                        detected_fractional_indices=fractional_indices,
                        gap_summary=summary,
                    )
                )

        return gaps


class FRBRDeduplicationEngine:
    """Classifies duplicate clusters into Clones vs Multiformat vs Editions."""

    @classmethod
    def cluster_duplicates(cls, books: list[BookNode]) -> list[DuplicateCluster]:
        dsu = DisjointSetUnion()
        by_id: dict[int, BookNode] = {b.book_id: b for b in books}

        # Indexing for candidate pairing
        by_isbn: dict[str, list[int]] = defaultdict(list)
        by_clean_title: dict[str, list[int]] = defaultdict(list)

        for b in books:
            dsu.find(b.book_id)
            if b.isbn:
                can_isbn = EntityCleanser.validate_and_convert_isbn(b.isbn)
                if can_isbn:
                    by_isbn[can_isbn].append(b.book_id)
            if b.cleaned_title:
                norm_key = (
                    EntityCleanser.clean_author_name(b.author) or "",
                    b.cleaned_title.lower(),
                )
                by_clean_title[str(norm_key)].append(b.book_id)

        # Union by ISBN match
        for group in by_isbn.values():
            if len(group) > 1:
                first = group[0]
                for other in group[1:]:
                    dsu.union(first, other)

        # Union by exact Title + Author match
        for group in by_clean_title.values():
            if len(group) > 1:
                first = group[0]
                for other in group[1:]:
                    dsu.union(first, other)

        # Build clusters
        clusters_map: dict[int, list[int]] = defaultdict(list)
        for b in books:
            root = dsu.find(b.book_id)
            clusters_map[root].append(b.book_id)

        clusters: list[DuplicateCluster] = []
        for root, member_ids in clusters_map.items():
            if len(member_ids) <= 1:
                continue

            member_nodes = [by_id[mid] for mid in member_ids]
            titles = [m.title for m in member_nodes]
            authors = list({m.author for m in member_nodes if m.author})
            formats = list({f for m in member_nodes for f in m.formats})

            # A clone claim needs a complete cryptographic format-to-hash map
            # for every member. Candidate links are transitive, so a matching
            # pair cannot promote a mixed-edition component into a clone.
            hashes = [cls._complete_format_hashes(member) for member in member_nodes]
            is_clone = bool(hashes) and all(mapping is not None for mapping in hashes)
            if is_clone:
                first_mapping = hashes[0]
                is_clone = all(mapping == first_mapping for mapping in hashes[1:])

            all_fmts = [f for m in member_nodes for f in m.formats]
            has_format_overlap = len(all_fmts) != len(set(all_fmts))

            if is_clone:
                cluster_type = DuplicateType.CLONE
                rec = "Review complete cryptographic hash evidence before any manual cleanup."
                confidence = 1.0
            elif not has_format_overlap and len(member_nodes) == 2 and "EPUB" in formats and "PDF" in formats:
                cluster_type = DuplicateType.MULTIFORMAT
                rec = "Review complementary formats; title or ISBN evidence alone must not merge records."
                confidence = 0.75
            else:
                cluster_type = DuplicateType.EDITION
                rec = "Review edition variants; consider keeping if distinct translations or publishers."
                confidence = 0.75

            # Best canonical book: prefers EPUB over PDF, or lowest book_id
            canonical_id = min(member_ids)
            for m in member_nodes:
                if "EPUB" in m.formats:
                    canonical_id = m.book_id
                    break

            clusters.append(
                DuplicateCluster(
                    cluster_id=f"cluster_{root}",
                    cluster_type=cluster_type,
                    canonical_book_id=canonical_id,
                    member_book_ids=member_ids,
                    titles=titles,
                    authors=authors,
                    formats=formats,
                    confidence=confidence,
                    recommendation=rec,
                )
            )

        return clusters

    @staticmethod
    def _complete_format_hashes(book: BookNode) -> dict[str, str] | None:
        if not book.formats or len(book.formats) != len(book.file_hashes):
            return None
        mapping: dict[str, str] = {}
        for fmt, digest in zip(book.formats, book.file_hashes, strict=True):
            normalized_format = fmt.strip().upper()
            normalized_digest = digest.strip().lower()
            if (
                not normalized_format
                or not re.fullmatch(r"[0-9a-f]{64}", normalized_digest)
                or normalized_format in mapping
            ):
                return None
            mapping[normalized_format] = normalized_digest
        return mapping

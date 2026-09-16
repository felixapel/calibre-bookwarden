"""Multi-Provider Consensus, Semantic Tripwire and HD Cover Engine.

Provides:
1. FRBR Work vs. Manifestation distinction preventing edition drift
2. Semantic Tripwire: prevents poisoned ISBN overwrites (e.g. Atomic Habits incident)
3. Confidence Scoring Matrix (L0 Ground Truth -> L1 ISBN -> L2 Title+Author -> L3 External -> L4 LLM)
4. HD Cover Resolvers: Google Books FIFE w1600-h2400 & OpenLibrary '?default=false'
5. Two-Tier Cache (L1 Memory LRU + L2 SQLite WAL with negative caching)
"""

from __future__ import annotations

import logging
import re
import unicodedata
from dataclasses import dataclass, field
from enum import StrEnum

from calibre_ai_auditor.curation.entity_cleanser import EntityCleanser

logger = logging.getLogger(__name__)


class IdentityTier(StrEnum):
    TIER_A = "A"  # >= 0.90 (Auto-Apply)
    TIER_B = "B"  # 0.75 - 0.89 (Safe Enrichment)
    TIER_C = "C"  # < 0.75 (Quarantine / Review Required)


class ActionDecision(StrEnum):
    AUTO_APPLY = "AUTO_APPLY"
    ENRICH_MISSING = "ENRICH_MISSING"
    REVIEW_RECOMMENDED = "REVIEW_RECOMMENDED"
    KEEP_LOCAL = "KEEP_LOCAL"
    QUARANTINE_POISONED_ISBN = "QUARANTINE_POISONED_ISBN"


@dataclass
class ExternalCandidate:
    provider: str
    provider_id: str
    title: str
    authors: list[str] = field(default_factory=list)
    publisher: str | None = None
    published_year: int | None = None
    isbn13: str | None = None
    cover_url: str | None = None
    is_hd_cover: bool = False
    description: str | None = None
    tags: list[str] = field(default_factory=list)


@dataclass
class ScoreBreakdown:
    title_score: float = 0.0
    author_score: float = 0.0
    isbn_score: float = 0.0
    publisher_score: float = 0.0
    year_score: float = 0.0
    cover_score: float = 0.0
    penalties: float = 0.0
    final_score: float = 0.0
    tripwire_triggered: bool = False
    reasons: list[str] = field(default_factory=list)


@dataclass
class ConsensusResult:
    book_id: str
    declared_title: str
    declared_authors: list[str]
    declared_isbn: str | None
    best_candidate: ExternalCandidate | None
    scores: ScoreBreakdown
    tier: IdentityTier
    action: ActionDecision
    quarantined_identifiers: dict[str, str] = field(default_factory=dict)


class FuzzyMatcher:
    @staticmethod
    def normalize_str(s: str | None) -> str:
        if not s:
            return ""
        s = unicodedata.normalize("NFKD", str(s))
        s = "".join(c for c in s if not unicodedata.combining(c))
        s = re.sub(r"[^\w\s]", " ", s)
        s = re.sub(r"\s+", " ", s)
        return s.strip().lower()

    @classmethod
    def ratio(cls, s1: str, s2: str) -> float:
        c1, c2 = cls.normalize_str(s1), cls.normalize_str(s2)
        if not c1 and not c2:
            return 1.0
        if not c1 or not c2:
            return 0.0
        if c1 == c2:
            return 1.0
        # Fast Levenshtein distance
        if len(c1) < len(c2):
            c1, c2 = c2, c1
        prev = list(range(len(c2) + 1))
        for i, ch1 in enumerate(c1):
            curr = [i + 1]
            for j, ch2 in enumerate(c2):
                ins = prev[j + 1] + 1
                dels = curr[j] + 1
                subs = prev[j] + (ch1 != ch2)
                curr.append(min(ins, dels, subs))
            prev = curr
        return max(0.0, 1.0 - (prev[-1] / max(len(c1), len(c2))))

    @classmethod
    def token_set_ratio(cls, s1: str, s2: str) -> float:
        c1, c2 = cls.normalize_str(s1), cls.normalize_str(s2)
        t1, t2 = set(c1.split()), set(c2.split())
        if not t1 or not t2:
            return 0.0
        inter = t1.intersection(t2)
        d1 = t1 - inter
        d2 = t2 - inter
        s_inter = " ".join(sorted(inter))
        s_d1 = f"{s_inter} {' '.join(sorted(d1))}".strip()
        s_d2 = f"{s_inter} {' '.join(sorted(d2))}".strip()
        r1 = cls.ratio(s_inter, s_d1) if s_inter else 0.0
        r2 = cls.ratio(s_inter, s_d2) if s_inter else 0.0
        r3 = cls.ratio(s_d1, s_d2)
        return max(r1, r2, r3, cls.ratio(c1, c2))


class HDCoverResolver:
    @staticmethod
    def transform_google_books_cover(thumbnail_url: str | None, volume_id: str | None = None) -> str | None:
        """Transforms Google Books thumbnail into uncompressed HD cover (>1000px)."""
        if not thumbnail_url and not volume_id:
            return None

        if thumbnail_url:
            url = thumbnail_url.replace("http://", "https://")
            url = re.sub(r"&edge=curl", "", url)
            url = re.sub(r"edge=curl&?", "", url)

            if "zoom=1" in url:
                url = url.replace("zoom=1", "zoom=0")
            elif "zoom=5" in url:
                url = url.replace("zoom=5", "zoom=0")
            elif "zoom=" not in url and "?" in url:
                url += "&zoom=0"

            if "fife=" in url:
                url = re.sub(r"fife=w\d+-h\d+", "fife=w1600-h2400", url)
            else:
                sep = "&" if "?" in url else "?"
                url += f"{sep}fife=w1600-h2400"
            return url

        if volume_id:
            return f"https://books.google.com/books/publisher/content/images/frontcover/{volume_id}?fife=w1600-h2400"

        return None

    @staticmethod
    def get_openlibrary_hd_cover(isbn13: str | None, cover_id: int | None = None) -> str | None:
        """Requests OpenLibrary Cover with '?default=false' to avoid silent 1x1 transparent GIFs."""
        if cover_id:
            return f"https://covers.openlibrary.org/b/id/{cover_id}-L.jpg?default=false"
        if isbn13:
            can13 = EntityCleanser.validate_and_convert_isbn(isbn13)
            if can13:
                return f"https://covers.openlibrary.org/b/isbn/{can13}-L.jpg?default=false"
        return None

    @staticmethod
    def get_amazon_hd_cover(asin_or_isbn: str) -> str:
        clean = re.sub(r"[^0-9A-Za-z]", "", asin_or_isbn).upper()
        return f"https://images-na.ssl-images-amazon.com/images/P/{clean}.01._SL1600_.jpg"


class SemanticTripwire:
    """Semantic circuit breaker: aborts external queries if ISBN points to completely different work."""

    @classmethod
    def evaluate(
        cls,
        local_title: str,
        local_author: str,
        candidate_title: str,
        candidate_authors: list[str],
    ) -> tuple[bool, str | None]:
        title_sim = max(
            FuzzyMatcher.ratio(local_title, candidate_title),
            FuzzyMatcher.token_set_ratio(local_title, candidate_title),
        )

        author_sim = 0.0
        if candidate_authors and local_author and local_author.lower() != "unknown":
            author_sim = max(
                max(FuzzyMatcher.ratio(local_author, ca), FuzzyMatcher.token_set_ratio(local_author, ca))
                for ca in candidate_authors
            )
        elif not local_author or local_author.lower() == "unknown":
            author_sim = 0.50

        # TRIPWIRE FIRED: Poisoned ISBN detected (e.g. Atomic Habits ISBN on a different book)
        if title_sim < 0.40 and author_sim < 0.35:
            reason = (
                f"Poisoned ISBN detected: Local '{local_title}' by '{local_author}' "
                f"conflicts with candidate '{candidate_title}' by '{', '.join(candidate_authors)}' "
                f"(title_sim={title_sim:.2f}, author_sim={author_sim:.2f})"
            )
            return False, reason

        return True, None


class ConsensusEngine:
    """Multi-provider consensus scorer with confidence ranking and decision tiering."""

    @classmethod
    def score_candidate(
        cls,
        declared_title: str,
        declared_author: str,
        declared_isbn: str | None,
        candidate: ExternalCandidate,
    ) -> ScoreBreakdown:
        # 1. Semantic Tripwire Check
        passed_tripwire, tripwire_reason = SemanticTripwire.evaluate(
            declared_title, declared_author, candidate.title, candidate.authors
        )

        if not passed_tripwire:
            return ScoreBreakdown(
                tripwire_triggered=True,
                final_score=0.0,
                reasons=[tripwire_reason or "tripwire_failed"],
            )

        # 2. Field scores
        title_score = max(
            FuzzyMatcher.ratio(declared_title, candidate.title),
            FuzzyMatcher.token_set_ratio(declared_title, candidate.title),
        )

        author_score = 0.0
        if candidate.authors and declared_author:
            author_score = max(
                max(FuzzyMatcher.ratio(declared_author, a), FuzzyMatcher.token_set_ratio(declared_author, a))
                for a in candidate.authors
            )
        elif not declared_author:
            author_score = 0.50

        isbn_score = 0.0
        if declared_isbn and candidate.isbn13:
            can_local = EntityCleanser.validate_and_convert_isbn(declared_isbn)
            can_cand = EntityCleanser.validate_and_convert_isbn(candidate.isbn13)
            if can_local and can_cand and can_local == can_cand:
                isbn_score = 1.0

        cover_score = 1.0 if candidate.is_hd_cover else (0.50 if candidate.cover_url else 0.0)

        penalties = 0.0
        if author_score < 0.25 and declared_author:
            penalties += 0.35

        # Weighted composite score
        if declared_isbn:
            final = (
                (0.35 * title_score) + (0.25 * author_score) + (0.25 * isbn_score) + (0.15 * cover_score) - penalties
            )
        else:
            final = (0.50 * title_score) + (0.35 * author_score) + (0.15 * cover_score) - penalties

        final_score = max(0.0, min(1.0, round(final, 3)))
        return ScoreBreakdown(
            title_score=round(title_score, 3),
            author_score=round(author_score, 3),
            isbn_score=round(isbn_score, 3),
            cover_score=round(cover_score, 3),
            penalties=round(penalties, 3),
            final_score=final_score,
            tripwire_triggered=False,
        )

    @classmethod
    def resolve_consensus(
        cls,
        book_id: str,
        declared_title: str,
        declared_author: str,
        declared_isbn: str | None,
        candidates: list[ExternalCandidate],
    ) -> ConsensusResult:
        if not candidates:
            return ConsensusResult(
                book_id=book_id,
                declared_title=declared_title,
                declared_authors=[declared_author] if declared_author else [],
                declared_isbn=declared_isbn,
                best_candidate=None,
                scores=ScoreBreakdown(),
                tier=IdentityTier.TIER_C,
                action=ActionDecision.KEEP_LOCAL,
            )

        # A valid local ISBN and a different valid candidate ISBN are explicit
        # manifestation evidence, not a weak scoring signal. Reject that
        # candidate before title/author similarity can accidentally select it.
        local_isbn = EntityCleanser.validate_and_convert_isbn(declared_isbn)
        eligible: list[ExternalCandidate] = []
        rejected_isbns: list[str] = []
        for candidate in candidates:
            candidate_isbn = EntityCleanser.validate_and_convert_isbn(candidate.isbn13)
            if local_isbn and candidate_isbn and candidate_isbn != local_isbn:
                rejected_isbns.append(candidate_isbn)
                continue
            eligible.append(candidate)

        if not eligible:
            return ConsensusResult(
                book_id=book_id,
                declared_title=declared_title,
                declared_authors=[declared_author] if declared_author else [],
                declared_isbn=declared_isbn,
                best_candidate=None,
                scores=ScoreBreakdown(reasons=["candidate_rejected_valid_isbn_conflict"]),
                tier=IdentityTier.TIER_C,
                action=ActionDecision.KEEP_LOCAL,
                quarantined_identifiers={"candidate_isbn": ",".join(rejected_isbns)},
            )

        scored = [
            (cand, cls.score_candidate(declared_title, declared_author, declared_isbn, cand)) for cand in eligible
        ]
        # Sort by final score descending
        scored.sort(key=lambda item: item[1].final_score, reverse=True)

        best_cand, best_score = scored[0]

        if best_score.tripwire_triggered:
            return ConsensusResult(
                book_id=book_id,
                declared_title=declared_title,
                declared_authors=[declared_author] if declared_author else [],
                declared_isbn=declared_isbn,
                best_candidate=None,
                scores=best_score,
                tier=IdentityTier.TIER_C,
                action=ActionDecision.KEEP_LOCAL,
                quarantined_identifiers={"candidate_isbn": best_cand.isbn13} if best_cand.isbn13 else {},
            )

        # Provider consensus remains review evidence. identity_v2 is the sole
        # authority that can decide whether a correction is eligible to apply.
        tier = IdentityTier.TIER_C
        action = ActionDecision.REVIEW_RECOMMENDED

        return ConsensusResult(
            book_id=book_id,
            declared_title=declared_title,
            declared_authors=[declared_author] if declared_author else [],
            declared_isbn=declared_isbn,
            best_candidate=best_cand,
            scores=best_score,
            tier=tier,
            action=action,
        )

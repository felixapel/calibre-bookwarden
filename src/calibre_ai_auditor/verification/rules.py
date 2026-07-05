"""Deterministic per-field verification rules.

Each rule takes (declared_value, observed_value, context) and returns a FieldVerdict.
Rules MUST be side-effect free, MUST NOT call any network or LLM, and MUST be
testable in isolation.  Anything that needs an LLM witness is the job of the
ContentVerificationEngine, not here.

Rule priority order when computing confidence:
  - Both values are None → ambiguous (we have no signal)
  - Declared is None but observed exists → missing (proposed fix)
  - Declared exists but observed is None → ambiguous (could be extraction failure)
  - Both exist and deterministically match → confirmed
  - Both exist and deterministically disagree → mismatch
"""

from __future__ import annotations

import re
import unicodedata
from difflib import SequenceMatcher
from typing import Any

from calibre_ai_auditor.verification.verdict import (
    EvidenceSpan,
    FieldVerdict,
    VerdictKind,
)

# ---------------------------------------------------------------------------
# Normalization helpers
# ---------------------------------------------------------------------------

_PUNCT_RE = re.compile(r"[^\w\s]", re.UNICODE)
_WS_RE = re.compile(r"\s+")
_EDITION_TAGS = re.compile(
    r"\s*[\(\[](?:"
    r"special(?:\s+edition)?|"
    r"anniversary(?:\s+edition)?|"
    r"collector'?s?(?:\s+edition)?|"
    r"deluxe(?:\s+edition)?|"
    r"hardcover|paperback|"
    r"first(?:\s+edition)?|"
    r"\d+(?:st|nd|rd|th)(?:\s+edition)?|"
    r"1st(?:\s+edition)?|2nd(?:\s+edition)?|3rd(?:\s+edition)?|"
    r"revised(?:\s+edition)?|updated(?:\s+edition)?|"
    r"expanded(?:\s+edition)?|complete(?:\s+edition)?|"
    r"definitive(?:\s+edition)?|illustrated(?:\s+edition)?|"
    r"gift(?:\s+edition)?|boxed(?:\s+set)?|"
    r"set|omnibus(?:\s+edition)?|"
    r"annotated(?:\s+edition)?|unabridged(?:\s+edition)?|"
    r"abridged(?:\s+edition)?|preview|"
    r"novel|edition|"
    r"vol\.?|volume|book\s+\d+|part\s+\d+|chapter\s+\d+|\d+"
    r")\s*[\)\]]",
    re.IGNORECASE,
)
_IS_DATE_RE = re.compile(r"^(\d{4})(?:-(\d{2})(?:-(\d{2}))?)?")


def _normalize_text(value: Any) -> str:
    """Lowercase, strip accents, collapse whitespace, strip edition tags, strip punctuation."""
    if value is None:
        return ""
    if isinstance(value, list):
        value = " ".join(str(v) for v in value)
    s = str(value)
    s = unicodedata.normalize("NFKD", s)
    s = "".join(c for c in s if not unicodedata.combining(c))
    s = _EDITION_TAGS.sub("", s)
    s = _PUNCT_RE.sub(" ", s)
    s = _WS_RE.sub(" ", s)
    return s.strip().lower()


# Minimal Cyrillic → Latin transliteration for transliteration-tolerant matching.
# Covers the 33 Russian Cyrillic letters.  Not a full library — just enough for
# names like "Михаил Булгаков" ≈ "Mikhail Bulgakov".
_CYRILLIC_TO_LATIN = {
    "а": "a", "б": "b", "в": "v", "г": "g", "д": "d", "е": "e", "ё": "e",
    "ж": "zh", "з": "z", "и": "i", "й": "y", "к": "k", "л": "l", "м": "m",
    "н": "n", "о": "o", "п": "p", "р": "r", "с": "s", "т": "t", "у": "u",
    "ф": "f", "х": "kh", "ц": "ts", "ч": "ch", "ш": "sh", "щ": "shch",
    "ъ": "", "ы": "y", "ь": "", "э": "e", "ю": "yu", "я": "ya",
}


def _transliterate_cyrillic(s: str) -> str:
    out = []
    for ch in s.lower():
        out.append(_CYRILLIC_TO_LATIN.get(ch, ch))
    return "".join(out)


def _cross_script_similarity(a: str, b: str) -> float:
    """Compare across scripts (e.g. Cyrillic vs Latin) by transliterating and re-matching."""
    a_t = _transliterate_cyrillic(a)
    b_t = _transliterate_cyrillic(b)
    return _fuzzy_ratio(a_t, b_t)


def _isbn13_checksum_valid(s: str) -> bool:
    """Standard ISBN-13 mod-11 checksum (after clearing the check digit and weighting 1,3)."""
    if len(s) != 13 or not s.isdigit():
        return False
    total = sum(int(c) * (1 if i % 2 == 0 else 3) for i, c in enumerate(s[:-1]))
    check = (10 - (total % 10)) % 10
    return check == int(s[-1])


def _isbn10_checksum_valid(s: str) -> bool:
    """Standard ISBN-10 checksum."""
    if len(s) != 10:
        return False
    if not (s[:9].isdigit() and (s[9].isdigit() or s[9].upper() == "X")):
        return False
    total = sum(int(c) * (10 - i) for i, c in enumerate(s[:9]))
    if s[9].upper() == "X":
        total += 10
    return total % 11 == 0


def _isbn_is_valid(s: str) -> bool:
    s = re.sub(r"[\s-]", "", s)
    if len(s) == 13:
        return _isbn13_checksum_valid(s)
    if len(s) == 10:
        return _isbn10_checksum_valid(s)
    return False


def _isbn13_from_isbn10(s: str) -> str:
    """Convert ISBN-10 to ISBN-13 by prepending '978' and recomputing check digit."""
    body = "978" + s[:9]
    total = sum(int(c) * (1 if i % 2 == 0 else 3) for i, c in enumerate(body))
    check = (10 - (total % 10)) % 10
    return body + str(check)


def _is_isbn10(s: str) -> bool:
    return len(re.sub(r"[\s-]", "", s)) == 10


def _fuzzy_ratio(a: str, b: str) -> float:
    return SequenceMatcher(None, a, b).ratio()


# ---------------------------------------------------------------------------
# Per-field rule functions
# ---------------------------------------------------------------------------


def verify_isbn(declared: Any, observed: Any, *, page_range: str | None = None) -> FieldVerdict:
    """ISBN rule: checksum-validated exact match only."""
    declared_clean = re.sub(r"[\s-]", "", str(declared)) if declared else None
    observed_clean = re.sub(r"[\s-]", "", str(observed)) if observed else None

    # Both empty
    if not declared_clean and not observed_clean:
        return FieldVerdict(
            field="isbn",
            declared_value=None,
            observed_value=None,
            verdict=VerdictKind.ambiguous,
            confidence=0,
            reason="No ISBN declared and none observed.",
        )

    # Missing in Calibre but found in book
    if not declared_clean and observed_clean:
        if _isbn_is_valid(observed_clean):
            return FieldVerdict(
                field="isbn",
                declared_value=None,
                observed_value=observed_clean,
                verdict=VerdictKind.missing,
                confidence=95,
                evidence=[
                    EvidenceSpan(
                        source="isbn_block",
                        text=observed_clean,
                        page_range=page_range,
                        confidence=95,
                    )
                ],
                reason=f"Found valid ISBN {observed_clean} on copyright page; not declared in Calibre.",
            )
        return FieldVerdict(
            field="isbn",
            declared_value=None,
            observed_value=observed_clean,
            verdict=VerdictKind.ambiguous,
            confidence=40,
            reason=f"Found ISBN-shaped string {observed_clean!r} but checksum invalid.",
        )

    # Declared exists, observed missing
    if declared_clean and not observed_clean:
        return FieldVerdict(
            field="isbn",
            declared_value=declared_clean,
            observed_value=None,
            verdict=VerdictKind.ambiguous,
            confidence=50,
            reason="ISBN declared but no ISBN found on extracted copyright page.",
        )

    # Both exist — validate and compare
    assert declared_clean and observed_clean
    decl_valid = _isbn_is_valid(declared_clean)
    obs_valid = _isbn_is_valid(observed_clean)

    if not decl_valid and not obs_valid:
        return FieldVerdict(
            field="isbn",
            declared_value=declared_clean,
            observed_value=observed_clean,
            verdict=VerdictKind.ambiguous,
            confidence=30,
            reason="Both ISBNs have invalid checksums.",
        )

    # If they're already the same string → confirmed
    if declared_clean == observed_clean:
        return FieldVerdict(
            field="isbn",
            declared_value=declared_clean,
            observed_value=observed_clean,
            verdict=VerdictKind.confirmed,
            confidence=99,
            reason="ISBNs match exactly.",
        )

    # Cross-format check: declared ISBN-10 vs observed ISBN-13 of same book
    if _is_isbn10(declared_clean) and len(observed_clean) == 13 and obs_valid:
        converted = _isbn13_from_isbn10(declared_clean)
        if converted == observed_clean:
            return FieldVerdict(
                field="isbn",
                declared_value=declared_clean,
                observed_value=observed_clean,
                verdict=VerdictKind.confirmed,
                confidence=90,
                reason="Declared ISBN-10 converts to observed ISBN-13.",
            )
    if _is_isbn10(observed_clean) and len(declared_clean) == 13 and decl_valid:
        converted = _isbn13_from_isbn10(observed_clean)
        if converted == declared_clean:
            return FieldVerdict(
                field="isbn",
                declared_value=declared_clean,
                observed_value=observed_clean,
                verdict=VerdictKind.confirmed,
                confidence=90,
                reason="Observed ISBN-10 converts to declared ISBN-13.",
            )

    # Both valid but different → high-risk conflict
    if decl_valid and obs_valid:
        return FieldVerdict(
            field="isbn",
            declared_value=declared_clean,
            observed_value=observed_clean,
            verdict=VerdictKind.mismatch,
            confidence=98,
            risk_flags=["isbn_conflict"],
            evidence=[
                EvidenceSpan(
                    source="isbn_block",
                    text=observed_clean,
                    page_range=page_range,
                    confidence=98,
                )
            ],
            reason=f"ISBN conflict: declared {declared_clean} but book has {observed_clean}.",
            requires_review=True,
        )

    # One valid, one invalid
    if obs_valid and not decl_valid:
        return FieldVerdict(
            field="isbn",
            declared_value=declared_clean,
            observed_value=observed_clean,
            verdict=VerdictKind.mismatch,
            confidence=92,
            risk_flags=["isbn_conflict"],
            reason=f"Declared ISBN has invalid checksum; book has valid {observed_clean}.",
            requires_review=True,
        )

    # Mirror: declared valid but observed has invalid checksum
    if decl_valid and not obs_valid:
        return FieldVerdict(
            field="isbn",
            declared_value=declared_clean,
            observed_value=observed_clean,
            verdict=VerdictKind.mismatch,
            confidence=88,
            risk_flags=["isbn_conflict"],
            reason=f"Observed ISBN {observed_clean} has invalid checksum.",
            requires_review=True,
        )

    return FieldVerdict(
        field="isbn",
        declared_value=declared_clean,
        observed_value=observed_clean,
        verdict=VerdictKind.ambiguous,
        confidence=40,
        reason="ISBN values differ but neither has a valid checksum.",
    )


def verify_title(
    declared: Any,
    observed: Any,
    *,
    page_range: str | None = None,
    evidence_quality: str = "high",
) -> FieldVerdict:
    """Title rule: fuzzy match after edition-tag and accent normalization."""
    low_signal = evidence_quality == "low"

    if declared is None and observed is None:
        return FieldVerdict(
            field="title",
            declared_value=None,
            observed_value=None,
            verdict=VerdictKind.ambiguous,
            confidence=0,
            reason="No title declared and none observed.",
        )
    if declared is None and observed:
        if low_signal:
            return FieldVerdict(
                field="title",
                declared_value=None,
                observed_value=observed,
                verdict=VerdictKind.ambiguous,
                confidence=55,
                reason="OCR quality is low; cannot trust title.",
            )
        return FieldVerdict(
            field="title",
            declared_value=None,
            observed_value=observed,
            verdict=VerdictKind.missing,
            confidence=95 if len(str(observed)) >= 3 else 70,
            evidence=[
                EvidenceSpan(
                    source="title_page",
                    text=str(observed),
                    page_range=page_range,
                    confidence=95,
                )
            ],
            reason=f"Title found on title page: {observed!r}.",
        )
    if declared and observed is None:
        return FieldVerdict(
            field="title",
            declared_value=declared,
            observed_value=None,
            verdict=VerdictKind.ambiguous,
            confidence=50,
            reason="Title declared but extraction returned no title-page text.",
        )

    norm_decl = _normalize_text(declared)
    norm_obs = _normalize_text(observed)
    ratio = _fuzzy_ratio(norm_decl, norm_obs)

    if not norm_decl or not norm_obs:
        return FieldVerdict(
            field="title",
            declared_value=declared,
            observed_value=observed,
            verdict=VerdictKind.ambiguous,
            confidence=40,
            reason="One or both titles are empty after normalization.",
        )

    # Detect trailing punctuation noise on the RAW declared string
    # (after normalization the punct is gone, but we want to flag it as a fix)
    raw_decl = str(declared).strip()
    raw_decl_stripped_punct = raw_decl.rstrip("!?.,;:*")
    if (
        raw_decl_stripped_punct and
        _normalize_text(raw_decl_stripped_punct) == norm_obs and
        raw_decl_stripped_punct != raw_decl
    ):
        return FieldVerdict(
            field="title",
            declared_value=declared,
            observed_value=observed,
            verdict=VerdictKind.mismatch,
            confidence=80,
            reason="Trailing punctuation noise on declared title.",
        )

    # Detect "by Author" suffix noise on the RAW declared string
    if " by " in raw_decl.lower():
        raw_decl_norm = _normalize_text(raw_decl)
        # If norm_obs equals the part BEFORE 'by' we have 'Title by Author' noise
        if norm_obs == raw_decl_norm.split(" by ")[0].strip():
            return FieldVerdict(
                field="title",
                declared_value=declared,
                observed_value=observed,
                verdict=VerdictKind.mismatch,
                confidence=88,
                reason="'by Author' suffix noise on declared title.",
            )

    # Detect edition-tag annotation on declared that the book doesn't have.
    # e.g. "Foundation (Special Edition)" → "Foundation"
    raw_decl_has_brackets = bool(_EDITION_TAGS.search(str(declared)))
    raw_obs_has_brackets = bool(_EDITION_TAGS.search(str(observed)))
    if raw_decl_has_brackets and not raw_obs_has_brackets and ratio >= 0.9:
        return FieldVerdict(
            field="title",
            declared_value=declared,
            observed_value=observed,
            verdict=VerdictKind.mismatch,
            confidence=82,
            reason=f"Declared title has edition annotation; canonical is {observed!r}.",
        )

    # Detect filename-derived noise (e.g. 'tolkien_lord_of_rings_final')
    # If the normalized declared contains no spaces but the observed has spaces,
    # that's almost certainly a filename-derived title.
    if "_" in raw_decl.lower() or raw_decl.lower().endswith((".epub", ".pdf", ".mobi")):
        raw_decl_norm = _normalize_text(raw_decl)
        if raw_decl_norm and norm_obs and _fuzzy_ratio(raw_decl_norm, norm_obs) >= 0.5:
            return FieldVerdict(
                field="title",
                declared_value=declared,
                observed_value=observed,
                verdict=VerdictKind.mismatch,
                confidence=92,
                reason="Declared title looks filename-derived.",
            )

    # High-confidence match thresholds
    if ratio >= 0.95:
        if low_signal:
            # Low OCR quality — even perfect ratio is suspicious
            return FieldVerdict(
                field="title",
                declared_value=declared,
                observed_value=observed,
                verdict=VerdictKind.ambiguous,
                confidence=55,
                reason="OCR quality is low; cannot confirm match.",
            )
        return FieldVerdict(
            field="title",
            declared_value=declared,
            observed_value=observed,
            verdict=VerdictKind.confirmed,
            confidence=95,
            evidence=[
                EvidenceSpan(
                    source="title_page",
                    text=str(observed),
                    page_range=page_range,
                    confidence=95,
                )
            ],
            reason=f"Titles match (similarity {ratio:.2f}).",
        )

    # Substring match — declared contains observed (handles "(Special Edition)" noise)
    if norm_obs in norm_decl or norm_decl in norm_obs:
        # If observed is short and wraps a declared form like 'Dune' inside 'Dune (novel)',
        # that's a variant of the same title — confirmed, NOT mismatch.
        if abs(len(norm_decl) - len(norm_obs)) <= 20:
            return FieldVerdict(
                field="title",
                declared_value=declared,
                observed_value=observed,
                verdict=VerdictKind.confirmed,
                confidence=85,
                evidence=[
                    EvidenceSpan(
                        source="title_page",
                        text=str(observed),
                        page_range=page_range,
                        confidence=85,
                    )
                ],
                reason="Titles match after normalization (substring or wrapped).",
            )
        # Significant length difference — but if observed is short and contained in
        # declared (e.g. 'Foundation' inside 'Foundation (Special Edition)'),
        # it's a mismatch (the canonical is observed).
        if norm_obs in norm_decl and len(norm_obs) < len(norm_decl):
            return FieldVerdict(
                field="title",
                declared_value=declared,
                observed_value=observed,
                verdict=VerdictKind.mismatch,
                confidence=80,
                evidence=[
                    EvidenceSpan(
                        source="title_page",
                        text=str(observed),
                        page_range=page_range,
                        confidence=80,
                    )
                ],
                reason=f"Declared title has edition annotation; canonical is {observed!r}.",
            )
        # Otherwise significant length difference — declared has extra noise OR
        # observed has annotation.  Canonical is what the book says.
        return FieldVerdict(
            field="title",
            declared_value=declared,
            observed_value=observed,
            verdict=VerdictKind.mismatch,
            confidence=80,
            evidence=[
                EvidenceSpan(
                    source="title_page",
                    text=str(observed),
                    page_range=page_range,
                    confidence=80,
                )
            ],
            reason=f"Declared title has noise or annotation; canonical is {observed!r}.",
        )

    # Low similarity → mismatch (wrong title)
    if ratio < 0.7:
        return FieldVerdict(
            field="title",
            declared_value=declared,
            observed_value=observed,
            verdict=VerdictKind.mismatch,
            confidence=95,
            risk_flags=["wrong_book"] if ratio < 0.4 else [],
            evidence=[
                EvidenceSpan(
                    source="title_page",
                    text=str(observed),
                    page_range=page_range,
                    confidence=95,
                )
            ],
            reason=f"Titles differ significantly (similarity {ratio:.2f}).",
        )

    # Mid-range — ambiguous, needs LLM witness
    return FieldVerdict(
        field="title",
        declared_value=declared,
        observed_value=observed,
        verdict=VerdictKind.ambiguous,
        confidence=60,
        reason=f"Title similarity {ratio:.2f} is ambiguous; needs LLM adjudication.",
    )


def _authors_equal(declared: list[str], observed: list[str]) -> bool:
    """Set-equality after normalization."""
    norm_decl = {_normalize_text(a) for a in declared if a}
    norm_obs = {_normalize_text(a) for a in observed if a}
    return norm_decl == norm_obs


def verify_authors(
    declared: Any,
    observed: Any,
    *,
    page_range: str | None = None,
    evidence_quality: str = "high",
) -> FieldVerdict:
    """Authors rule: set comparison after normalization.

    Includes transliteration tolerance (Cyrillic ↔ Latin) so that
    "Mikhail Bulgakov" matches "Михаил Булгаков".
    """
    low_signal = evidence_quality == "low"
    if isinstance(declared, str):
        declared_list = [a.strip() for a in declared.split("&") if a.strip()]
    elif isinstance(declared, list):
        declared_list = [str(a) for a in declared if a]
    else:
        declared_list = []

    if isinstance(observed, str):
        observed_list = [a.strip() for a in observed.split("&") if a.strip()]
    elif isinstance(observed, list):
        observed_list = [str(a) for a in observed if a]
    else:
        observed_list = []

    if not declared_list and not observed_list:
        return FieldVerdict(
            field="authors",
            declared_value=[],
            observed_value=None,
            verdict=VerdictKind.ambiguous,
            confidence=0,
            reason="No authors declared and none observed.",
        )

    if not declared_list and observed_list:
        return FieldVerdict(
            field="authors",
            declared_value=[],
            observed_value=observed_list,
            verdict=VerdictKind.missing,
            confidence=95,
            evidence=[
                EvidenceSpan(
                    source="title_page",
                    text=", ".join(observed_list),
                    page_range=page_range,
                    confidence=95,
                )
            ],
            reason=f"Author(s) found on title page: {observed_list}.",
        )

    if declared_list and not observed_list:
        return FieldVerdict(
            field="authors",
            declared_value=declared_list,
            observed_value=None,
            verdict=VerdictKind.ambiguous,
            confidence=50,
            reason="Authors declared but extraction returned no author info.",
        )

    assert declared_list and observed_list
    if _authors_equal(declared_list, observed_list):
        if low_signal:
            return FieldVerdict(
                field="authors",
                declared_value=declared_list,
                observed_value=observed_list,
                verdict=VerdictKind.ambiguous,
                confidence=55,
                reason="OCR quality is low; cannot confirm match.",
            )
        return FieldVerdict(
            field="authors",
            declared_value=declared_list,
            observed_value=observed_list,
            verdict=VerdictKind.confirmed,
            confidence=99,
            evidence=[
                EvidenceSpan(
                    source="title_page",
                    text=", ".join(observed_list),
                    page_range=page_range,
                    confidence=99,
                )
            ],
            reason="Authors match.",
        )

    # Transliteration tolerance: each declared author should have a cross-script match
    # somewhere in the observed set (or vice versa).
    def _is_transliteration_match(a: str, b: str) -> bool:
        if not a or not b:
            return False
        return _cross_script_similarity(_normalize_text(a), _normalize_text(b)) >= 0.9

    # Surname match: check if the last word of one is the first/last word of the other.
    # Catches "Tolkien" ↔ "J.R.R. Tolkien" — same person, partial form.
    def _surname_match(a: str, b: str) -> bool:
        a_words = _normalize_text(a).split()
        b_words = _normalize_text(b).split()
        if not a_words or not b_words:
            return False
        return a_words[-1] == b_words[-1] or a_words[-1] == b_words[0]

    def _surname_match_any(a: str, b_set: set[str]) -> bool:
        for b in b_set:
            if _surname_match(a, b):
                return True
        return False

    norm_decl = {_normalize_text(a) for a in declared_list if a}
    norm_obs = {_normalize_text(a) for a in observed_list if a}

    # Sentinel authors (Unknown, Anonymous, N.N., etc.) — these are NOT real conflicts.
    sentinels = {"unknown", "anonymous", "n n", "n n ", "various", "n a"}

    # Cross-script transliteration match
    def _cross(a_set: set[str], b_set: set[str]) -> bool:
        for a in a_set:
            for b in b_set:
                if _is_transliteration_match(a, b):
                    return True
        return False

    # If every declared author matches observed (modulo transliteration) AND the
    # sets are the same size, it's the same set.
    if len(declared_list) == len(observed_list) and all(
        any(_is_transliteration_match(d, o) or _surname_match(d_raw, o_raw)
            for o, o_raw in zip(norm_obs, observed_list))
        for d, d_raw in zip(norm_decl, declared_list)
    ) and all(
        any(_is_transliteration_match(o, d) or _surname_match(o_raw, d_raw)
            for d, d_raw in zip(norm_decl, declared_list))
        for o, o_raw in zip(norm_obs, observed_list)
    ):
        return FieldVerdict(
            field="authors",
            declared_value=declared_list,
            observed_value=observed_list,
            verdict=VerdictKind.confirmed,
            confidence=88,
            evidence=[
                EvidenceSpan(
                    source="title_page",
                    text=", ".join(observed_list),
                    page_range=page_range,
                    confidence=88,
                )
            ],
            reason="Authors match (transliteration tolerance).",
        )

    # Sentinel handling: if declared is only sentinels (e.g. 'Unknown'),
    # treat it as mismatch (not missing) — the book actually has a different author.
    if all(d in sentinels for d in norm_decl):
        return FieldVerdict(
            field="authors",
            declared_value=declared_list,
            observed_value=observed_list,
            verdict=VerdictKind.mismatch,
            confidence=92,
            evidence=[
                EvidenceSpan(
                    source="title_page",
                    text=", ".join(observed_list),
                    page_range=page_range,
                    confidence=92,
                )
            ],
            reason=f"Declared author is sentinel; book has real author(s): {observed_list}.",
        )

    # Disjoint sets → author_swap risk (only if no surname match)
    if not norm_decl.intersection(norm_obs) and not _cross(norm_decl, norm_obs) \
            and not any(_surname_match_any(d_raw, set(observed_list)) for d_raw in declared_list):
        return FieldVerdict(
            field="authors",
            declared_value=declared_list,
            observed_value=observed_list,
            verdict=VerdictKind.mismatch,
            confidence=92,
            risk_flags=["author_swap"],
            evidence=[
                EvidenceSpan(
                    source="title_page",
                    text=", ".join(observed_list),
                    page_range=page_range,
                    confidence=92,
                )
            ],
            reason=f"Author swap: declared {declared_list} but book has {observed_list}.",
            requires_review=True,
        )

    # Overlap (with transliteration + surname tolerance)
    def _covered_by(src_list: list[str], dst_list: list[str]) -> bool:
        for s_raw in src_list:
            s = _normalize_text(s_raw)
            for d_raw in dst_list:
                d = _normalize_text(d_raw)
                if s == d or _is_transliteration_match(s_raw, d_raw) or _surname_match(s_raw, d_raw):
                    break
            else:
                return False
        return True

    decl_covered_by_obs = _covered_by(norm_decl, norm_obs)
    obs_covered_by_decl = _covered_by(norm_obs, norm_decl)

    if obs_covered_by_decl and not decl_covered_by_obs:
        # declared has hallucinated extra
        extra = [
            d_raw for d_raw in declared_list
            if not any(
                _is_transliteration_match(d_raw, o_raw) or _surname_match(d_raw, o_raw)
                for o_raw in observed_list
            )
        ]
        return FieldVerdict(
            field="authors",
            declared_value=declared_list,
            observed_value=observed_list,
            verdict=VerdictKind.mismatch,
            confidence=88,
            risk_flags=["author_swap"] if extra else [],
            evidence=[
                EvidenceSpan(
                    source="title_page",
                    text=", ".join(observed_list),
                    page_range=page_range,
                    confidence=88,
                )
            ],
            reason=f"Declared authors include hallucinated extra(s): {extra}.",
            requires_review=bool(extra),
        )

    if decl_covered_by_obs and not obs_covered_by_decl:
        # observed has additional co-author
        extra = [
            o_raw for o_raw in observed_list
            if not any(
                _is_transliteration_match(o_raw, d_raw) or _surname_match(o_raw, d_raw)
                for d_raw in declared_list
            )
        ]
        return FieldVerdict(
            field="authors",
            declared_value=declared_list,
            observed_value=observed_list,
            verdict=VerdictKind.mismatch,
            confidence=92,
            evidence=[
                EvidenceSpan(
                    source="title_page",
                    text=", ".join(observed_list),
                    page_range=page_range,
                    confidence=92,
                )
            ],
            reason=f"Missing co-author(s): {extra}.",
        )

    # Partial overlap (rare)
    return FieldVerdict(
        field="authors",
        declared_value=declared_list,
        observed_value=observed_list,
        verdict=VerdictKind.mismatch,
        confidence=80,
        evidence=[
            EvidenceSpan(
                source="title_page",
                text=", ".join(observed_list),
                page_range=page_range,
                confidence=80,
            )
        ],
        reason="Author sets partially overlap.",
    )


def verify_publisher(declared: Any, observed: Any, *, page_range: str | None = None) -> FieldVerdict:
    """Publisher rule: variant-tolerant (Penguin ≈ Penguin Books)."""
    if declared is None and observed is None:
        return FieldVerdict(
            field="publisher",
            declared_value=None,
            observed_value=None,
            verdict=VerdictKind.ambiguous,
            confidence=0,
            reason="No publisher declared and none observed.",
        )
    if declared is None and observed:
        return FieldVerdict(
            field="publisher",
            declared_value=None,
            observed_value=observed,
            verdict=VerdictKind.missing,
            confidence=80,
            evidence=[
                EvidenceSpan(
                    source="copyright_page",
                    text=str(observed),
                    page_range=page_range,
                    confidence=80,
                )
            ],
            reason=f"Publisher found on copyright page: {observed!r}.",
        )
    if declared and observed is None:
        return FieldVerdict(
            field="publisher",
            declared_value=declared,
            observed_value=None,
            verdict=VerdictKind.ambiguous,
            confidence=50,
            reason="Publisher declared but extraction found none.",
        )

    norm_decl = _normalize_text(declared)
    norm_obs = _normalize_text(observed)
    ratio = _fuzzy_ratio(norm_decl, norm_obs)

    if norm_decl == norm_obs:
        return FieldVerdict(
            field="publisher",
            declared_value=declared,
            observed_value=observed,
            verdict=VerdictKind.confirmed,
            confidence=99,
            reason="Publisher matches exactly.",
        )

    # Variant tolerance: 'Penguin' vs 'Penguin Books' etc.
    if ratio >= 0.6 and (norm_decl in norm_obs or norm_obs in norm_decl):
        return FieldVerdict(
            field="publisher",
            declared_value=declared,
            observed_value=observed,
            verdict=VerdictKind.confirmed,
            confidence=85,
            evidence=[
                EvidenceSpan(
                    source="copyright_page",
                    text=str(observed),
                    page_range=page_range,
                    confidence=85,
                )
            ],
            reason=f"Publisher variant only (similarity {ratio:.2f}).",
        )

    if ratio < 0.5:
        return FieldVerdict(
            field="publisher",
            declared_value=declared,
            observed_value=observed,
            verdict=VerdictKind.mismatch,
            confidence=85,
            risk_flags=["publisher_mismatch"],
            reason=f"Publisher mismatch (similarity {ratio:.2f}).",
            requires_review=True,
        )

    return FieldVerdict(
        field="publisher",
        declared_value=declared,
        observed_value=observed,
        verdict=VerdictKind.ambiguous,
        confidence=60,
        reason=f"Publisher similarity {ratio:.2f}; ambiguous.",
    )


def verify_published_date(
    declared: Any, observed: Any, *, page_range: str | None = None
) -> FieldVerdict:
    """Date rule: ±1 day / year-only tolerance."""
    if declared is None and observed is None:
        return FieldVerdict(
            field="published_date",
            declared_value=None,
            observed_value=None,
            verdict=VerdictKind.ambiguous,
            confidence=0,
            reason="No date declared and none observed.",
        )
    if declared is None and observed:
        return FieldVerdict(
            field="published_date",
            declared_value=None,
            observed_value=observed,
            verdict=VerdictKind.missing,
            confidence=85,
            evidence=[
                EvidenceSpan(
                    source="copyright_page",
                    text=str(observed),
                    page_range=page_range,
                    confidence=85,
                )
            ],
            reason=f"Date found on copyright page: {observed!r}.",
        )
    if declared and observed is None:
        return FieldVerdict(
            field="published_date",
            declared_value=declared,
            observed_value=None,
            verdict=VerdictKind.ambiguous,
            confidence=50,
            reason="Date declared but extraction found none.",
        )

    decl_m = _IS_DATE_RE.match(str(declared).strip())
    obs_m = _IS_DATE_RE.match(str(observed).strip())
    if not decl_m or not obs_m:
        return FieldVerdict(
            field="published_date",
            declared_value=declared,
            observed_value=observed,
            verdict=VerdictKind.ambiguous,
            confidence=40,
            reason="Date parse failed.",
        )

    decl_year = decl_m.group(1)
    obs_year = obs_m.group(1)
    if decl_year != obs_year:
        # Allow ±1 year for off-by-one release-date vs publication-date common errors
        if abs(int(decl_year) - int(obs_year)) == 1:
            return FieldVerdict(
                field="published_date",
                declared_value=declared,
                observed_value=observed,
                verdict=VerdictKind.confirmed,
                confidence=80,
                reason="Years off by one; acceptable.",
            )
        return FieldVerdict(
            field="published_date",
            declared_value=declared,
            observed_value=observed,
            verdict=VerdictKind.mismatch,
            confidence=90,
            reason=f"Year mismatch: declared {decl_year}, observed {obs_year}.",
            requires_review=True,
        )

    # Years agree — fine
    return FieldVerdict(
        field="published_date",
        declared_value=declared,
        observed_value=observed,
        verdict=VerdictKind.confirmed,
        confidence=90,
        reason="Year matches.",
    )


def verify_language(declared: Any, observed: Any, *, page_range: str | None = None) -> FieldVerdict:
    """Language rule: 3-letter ISO code match."""
    if declared is None and observed is None:
        return FieldVerdict(
            field="language",
            declared_value=None,
            observed_value=None,
            verdict=VerdictKind.ambiguous,
            confidence=0,
            reason="No language declared and none observed.",
        )
    if declared is None and observed:
        return FieldVerdict(
            field="language",
            declared_value=None,
            observed_value=observed,
            verdict=VerdictKind.missing,
            confidence=90,
            evidence=[
                EvidenceSpan(
                    source="body_sample",
                    text=str(observed),
                    page_range=page_range,
                    confidence=90,
                )
            ],
            reason=f"Language detected: {observed!r}.",
        )
    if declared and observed is None:
        return FieldVerdict(
            field="language",
            declared_value=declared,
            observed_value=None,
            verdict=VerdictKind.ambiguous,
            confidence=50,
            reason="Language declared but body sample too small.",
        )

    norm_decl = str(declared).strip().lower()
    norm_obs = str(observed).strip().lower()
    if norm_decl == norm_obs:
        return FieldVerdict(
            field="language",
            declared_value=declared,
            observed_value=observed,
            verdict=VerdictKind.confirmed,
            confidence=99,
            reason="Language matches.",
        )

    return FieldVerdict(
        field="language",
        declared_value=declared,
        observed_value=observed,
        verdict=VerdictKind.mismatch,
        confidence=90,
        reason=f"Language mismatch: declared {norm_decl}, detected {norm_obs}.",
    )


def verify_series(
    declared: Any,
    observed: Any,
    *,
    page_range: str | None = None,
    observed_any: bool = True,
) -> FieldVerdict:
    """Series rule: presence/absence match.

    `observed_any` lets the caller tell us whether the extractor even attempted
    to find a series.  If False AND a series is declared, we still trust the
    declaration (some books legitimately don't have running headers).
    """
    if declared is None and observed is None:
        if not observed_any:
            # We didn't sample headers, but neither side claims anything — fine.
            return FieldVerdict(
                field="series",
                declared_value=None,
                observed_value=None,
                verdict=VerdictKind.confirmed,
                confidence=90,
                reason="No series declared; headers not sampled.",
            )
        return FieldVerdict(
            field="series",
            declared_value=None,
            observed_value=None,
            verdict=VerdictKind.confirmed,
            confidence=95,
            reason="No series declared and none observed.",
        )
    if declared is None and observed:
        return FieldVerdict(
            field="series",
            declared_value=None,
            observed_value=observed,
            verdict=VerdictKind.missing,
            confidence=85,
            evidence=[
                EvidenceSpan(
                    source="header",
                    text=str(observed),
                    page_range=page_range,
                    confidence=85,
                )
            ],
            reason=f"Series header found: {observed!r}.",
        )
    if declared and observed is None:
        if not observed_any:
            # We didn't look; can't confirm or deny
            return FieldVerdict(
                field="series",
                declared_value=declared,
                observed_value=None,
                verdict=VerdictKind.ambiguous,
                confidence=50,
                reason="Series declared but headers not sampled.",
            )
        return FieldVerdict(
            field="series",
            declared_value=declared,
            observed_value=None,
            verdict=VerdictKind.mismatch,
            confidence=80,
            risk_flags=["series_mismatch"],
            evidence=[
                EvidenceSpan(
                    source="header",
                    text="(no series header found)",
                    page_range=page_range,
                    confidence=80,
                )
            ],
            reason="Series declared but no series header on running pages.",
            requires_review=True,
        )

    if _normalize_text(declared) == _normalize_text(observed):
        return FieldVerdict(
            field="series",
            declared_value=declared,
            observed_value=observed,
            verdict=VerdictKind.confirmed,
            confidence=95,
            reason="Series matches.",
        )
    return FieldVerdict(
        field="series",
        declared_value=declared,
        observed_value=observed,
        verdict=VerdictKind.mismatch,
        confidence=85,
        risk_flags=["series_mismatch"],
        reason="Series differs.",
        requires_review=True,
    )


def verify_series_index(declared: Any, observed: Any, *, page_range: str | None = None) -> FieldVerdict:
    """Series index rule: float presence/absence match (e.g. 1.0 for vol 1)."""
    if declared is None and observed is None:
        return FieldVerdict(
            field="series_index",
            declared_value=None,
            observed_value=None,
            verdict=VerdictKind.confirmed,
            confidence=90,
            reason="No series_index declared and none observed.",
        )
    if declared is None and observed is not None:
        return FieldVerdict(
            field="series_index",
            declared_value=None,
            observed_value=observed,
            verdict=VerdictKind.missing,
            confidence=85,
            evidence=[
                EvidenceSpan(
                    source="header",
                    text=str(observed),
                    page_range=page_range,
                    confidence=85,
                )
            ],
            reason=f"Series index found in header: {observed!r}.",
        )
    if declared is not None and observed is None:
        return FieldVerdict(
            field="series_index",
            declared_value=declared,
            observed_value=None,
            verdict=VerdictKind.ambiguous,
            confidence=50,
            reason="Series_index declared but not found in header sample.",
        )
    # Both exist — check tolerance
    try:
        d = float(declared)
        o = float(observed)
    except (TypeError, ValueError):
        return FieldVerdict(
            field="series_index",
            declared_value=declared,
            observed_value=observed,
            verdict=VerdictKind.ambiguous,
            confidence=40,
            reason="Series index parse failed.",
        )
    if abs(d - o) < 0.01:
        return FieldVerdict(
            field="series_index",
            declared_value=declared,
            observed_value=observed,
            verdict=VerdictKind.confirmed,
            confidence=95,
            reason="Series index matches.",
        )
    return FieldVerdict(
        field="series_index",
        declared_value=declared,
        observed_value=observed,
        verdict=VerdictKind.mismatch,
        confidence=90,
        reason=f"Series index mismatch: declared {d}, observed {o}.",
    )


# Rule registry — the engine looks up by field name
RULE_REGISTRY = {
    "isbn": verify_isbn,
    "title": verify_title,
    "authors": verify_authors,
    "publisher": verify_publisher,
    "published_date": verify_published_date,
    "language": verify_language,
    "series": verify_series,
    "series_index": verify_series_index,
}


def get_rule(field: str):
    return RULE_REGISTRY.get(field)
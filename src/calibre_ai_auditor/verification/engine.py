"""ContentVerificationEngine — the orchestrator that produces a BookVerdict.

Given a BookRecord and its declared metadata, plus an ObservationSet from
content extraction (title page text, copyright page text, header samples,
OCR output, language detection, etc.), this engine:

  1. Runs every applicable deterministic rule.
  2. Aggregates per-field verdicts.
  3. Marks fields as "ambiguous" if no rule has high enough confidence.
  4. Computes overall action (no_change | suggest_fix | needs_review | defer).
  5. Decides auto_apply_eligibility per the conservative policy.

It does NOT call any LLM or any network.  LLM witness for ambiguous fields
is a separate concern (`engine_llm.py`).
"""

from __future__ import annotations

import logging
from dataclasses import dataclass, field
from typing import Any

from calibre_ai_auditor.verification.rules import get_rule
from calibre_ai_auditor.verification.verdict import (
    AUTO_APPLY_MIN_CONFIDENCE,
    AUTO_APPLY_MIN_FIELD_CONFIDENCE,
    HIGH_RISK_FLAGS,
    BookVerdict,
    FieldVerdict,
    VerdictAction,
    VerdictKind,
)

logger = logging.getLogger(__name__)


@dataclass
class ObservationSet:
    """What content extractors observed about the book.

    All fields are optional; missing fields mean "we didn't observe anything
    for this aspect", which the engine treats as ambiguous (not as confirmed).
    """

    title_page_text: str | None = None
    copyright_page_text: str | None = None
    header_running_text: str | None = None
    body_sample: str | None = None
    isbn_extracted: str | None = None
    publisher_extracted: str | None = None
    date_extracted: str | None = None
    language_detected: str | None = None
    cover_phash_distance: int | None = None  # 0 = exact, None = not checked
    title_extracted: str | None = None
    authors_extracted: list[str] | None = None
    series_extracted: str | None = None
    series_index_extracted: float | None = None
    page_range_title: str = "1-3"
    page_range_copyright: str = "2-5"
    page_range_header: str = "running"
    page_range_isbn: str = "2-5"
    # Quality of the extraction signal (low = noisy OCR, high = clean text)
    # "high" | "medium" | "low" — controls how much we trust the observations
    evidence_quality: str = "high"


@dataclass
class DeclaredMetadata:
    """What Calibre currently claims about the book."""

    title: str | None = None
    authors: list[str] = field(default_factory=list)
    publisher: str | None = None
    published_date: str | None = None
    language: str | None = None
    series: str | None = None
    series_index: float | None = None
    isbn: str | None = None


# Map from declared-metadata key to (rule name, observation key for primary observed value)
_FIELD_BINDINGS: list[tuple[str, str, str, str]] = [
    # (calibre_field, rule_name, observation_attr, page_range_attr)
    ("isbn", "isbn", "isbn_extracted", "page_range_isbn"),
    ("title", "title", "title_extracted", "page_range_title"),
    ("authors", "authors", "authors_extracted", "page_range_title"),
    ("publisher", "publisher", "publisher_extracted", "page_range_copyright"),
    ("published_date", "published_date", "date_extracted", "page_range_copyright"),
    ("language", "language", "language_detected", "page_range_header"),
    ("series", "series", "series_extracted", "page_range_header"),
    ("series_index", "series_index", "series_index_extracted", "page_range_header"),
]


class ContentVerificationEngine:
    """Pure-Python orchestrator. No I/O. Safe to call in tests."""

    def __init__(
        self,
        *,
        auto_apply_min_overall: int = AUTO_APPLY_MIN_CONFIDENCE,
        auto_apply_min_field: int = AUTO_APPLY_MIN_FIELD_CONFIDENCE,
    ) -> None:
        self.auto_apply_min_overall = auto_apply_min_overall
        self.auto_apply_min_field = auto_apply_min_field

    def verify(
        self,
        book_key: str,
        run_id: str,
        declared: DeclaredMetadata,
        observed: ObservationSet,
    ) -> BookVerdict:
        """Run all rules and aggregate into a BookVerdict."""
        field_verdicts: dict[str, FieldVerdict] = {}

        for calibre_field, rule_name, obs_attr, page_range_attr in _FIELD_BINDINGS:
            rule = get_rule(rule_name)
            if rule is None:
                continue
            declared_value = getattr(declared, calibre_field, None)
            observed_value = getattr(observed, obs_attr, None)
            page_range = getattr(observed, page_range_attr, None)

            # Special case: series rule needs to know if we even sampled headers
            if rule_name == "series":
                fv = rule(
                    declared_value,
                    observed_value,
                    page_range=page_range,
                    observed_any=observed.header_running_text is not None,
                )
            else:
                # Pass evidence_quality to rules that honor it
                if rule_name in ("title", "authors"):
                    fv = rule(
                        declared_value,
                        observed_value,
                        page_range=page_range,
                        evidence_quality=observed.evidence_quality,
                    )
                else:
                    fv = rule(declared_value, observed_value, page_range=page_range)
            fv.field = calibre_field
            field_verdicts[calibre_field] = fv

        # Aggregate
        risk_flags: set[str] = set()
        reasons: list[str] = []
        confidences: list[int] = []
        any_real_ambiguous = False  # ambiguous where we HAD data but couldn't decide
        any_declared_ambiguous = False  # ambiguous where declared was None and observed was None
        any_mismatch_with_risk = False
        proposed_patch: dict[str, Any] = {}

        for fname, fv in field_verdicts.items():
            for rf in fv.risk_flags:
                risk_flags.add(rf)
            if fv.requires_review and any(rf in HIGH_RISK_FLAGS for rf in fv.risk_flags):
                any_mismatch_with_risk = True
            if fv.verdict == VerdictKind.ambiguous:
                # Distinguish "no signal at all" from "we couldn't decide"
                decl = fv.declared_value
                obs = fv.observed_value
                if decl is None or (isinstance(decl, (list, str)) and not decl):
                    if obs is None or (isinstance(obs, (list, str)) and not obs):
                        # nothing declared, nothing observed — skip silently
                        any_declared_ambiguous = True
                        continue
                any_real_ambiguous = True
            elif fv.verdict in (VerdictKind.confirmed, VerdictKind.mismatch, VerdictKind.missing):
                confidences.append(fv.confidence)

            if fv.reason:
                reasons.append(f"[{fname}] {fv.reason}")

            if fv.verdict in (VerdictKind.mismatch, VerdictKind.missing):
                proposed_patch[fname] = fv.observed_value

        # Compute overall confidence: simple mean of decided fields, 0 if none
        overall_confidence = int(sum(confidences) / len(confidences)) if confidences else 0

        # Determine action
        action = self._decide_action(
            field_verdicts=field_verdicts,
            risk_flags=risk_flags,
            overall_confidence=overall_confidence,
            any_ambiguous=any_real_ambiguous,
            any_mismatch_with_risk=any_mismatch_with_risk,
            any_declared_ambiguous=any_declared_ambiguous,
        )

        # Auto-apply gate
        auto_apply = self._is_auto_apply_eligible(
            action=action,
            field_verdicts=field_verdicts,
            risk_flags=risk_flags,
            overall_confidence=overall_confidence,
            any_ambiguous=any_real_ambiguous,
        )

        return BookVerdict(
            book_key=book_key,
            run_id=run_id,
            field_verdicts=field_verdicts,
            overall_confidence=overall_confidence,
            risk_flags=sorted(risk_flags),
            action=action,
            auto_apply_eligible=auto_apply,
            proposed_patch=proposed_patch,
            reasons=reasons,
        )

    def _decide_action(
        self,
        *,
        field_verdicts: dict[str, FieldVerdict],
        risk_flags: set[str],
        overall_confidence: int,
        any_ambiguous: bool,
        any_mismatch_with_risk: bool,
        any_declared_ambiguous: bool = False,
    ) -> VerdictAction:
        # High-risk flag forces needs_review regardless of confidence
        if risk_flags & HIGH_RISK_FLAGS:
            return VerdictAction.needs_review

        # Any real ambiguous field (we had data, couldn't decide) → needs_review
        # (LLM witness will be queued).  Declared-ambiguous fields where both
        # declared and observed are empty are silently ignored.
        if any_ambiguous:
            return VerdictAction.needs_review

        # Build a "decision-bearing" view: only fields where we made a real call.
        # Silently-skipped (declared-None/observed-None) fields don't count.
        decided = [
            fv
            for fv in field_verdicts.values()
            if fv.verdict
            in (VerdictKind.confirmed, VerdictKind.mismatch, VerdictKind.missing)
        ]

        if not decided:
            return VerdictAction.no_change

        # All decided fields confirmed → no change needed
        if all(fv.verdict == VerdictKind.confirmed for fv in decided):
            return VerdictAction.no_change

        # Some mismatch/missing proposed
        if overall_confidence >= 85:
            return VerdictAction.suggest_fix
        if overall_confidence >= 70:
            return VerdictAction.needs_review
        return VerdictAction.defer

    def _is_auto_apply_eligible(
        self,
        *,
        action: VerdictAction,
        field_verdicts: dict[str, FieldVerdict],
        risk_flags: set[str],
        overall_confidence: int,
        any_ambiguous: bool,
    ) -> bool:
        if action != VerdictAction.suggest_fix:
            return False
        if any_ambiguous:
            return False
        if risk_flags & HIGH_RISK_FLAGS:
            return False
        # Compute confidence over ONLY the fields being patched.
        patched = [
            fv
            for fv in field_verdicts.values()
            if fv.verdict in (VerdictKind.mismatch, VerdictKind.missing)
        ]
        if not patched:
            return False
        patched_confidence = int(sum(fv.confidence for fv in patched) / len(patched))
        if patched_confidence < self.auto_apply_min_overall:
            return False
        # Every proposed fix field must meet per-field threshold
        for fv in patched:
            if fv.confidence < self.auto_apply_min_field:
                return False
            if fv.requires_review:
                return False
        return True


def build_observation_from_extraction(
    *,
    title_page_text: str | None = None,
    copyright_page_text: str | None = None,
    header_running_text: str | None = None,
    body_sample: str | None = None,
    cover_phash_distance: int | None = None,
    isbn_extracted: str | None = None,
    publisher_extracted: str | None = None,
    date_extracted: str | None = None,
    language_detected: str | None = None,
    title_extracted: str | None = None,
    authors_extracted: list[str] | None = None,
    series_extracted: str | None = None,
) -> ObservationSet:
    """Convenience builder."""
    return ObservationSet(
        title_page_text=title_page_text,
        copyright_page_text=copyright_page_text,
        header_running_text=header_running_text,
        body_sample=body_sample,
        cover_phash_distance=cover_phash_distance,
        isbn_extracted=isbn_extracted,
        publisher_extracted=publisher_extracted,
        date_extracted=date_extracted,
        language_detected=language_detected,
        title_extracted=title_extracted,
        authors_extracted=authors_extracted,
        series_extracted=series_extracted,
    )
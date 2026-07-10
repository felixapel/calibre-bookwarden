"""v1.0 verdict schemas.

These replace the v0.9 single-MetadataResolution object with a per-field structure
that is auditable, testable, and safe for conservative auto-apply.
"""

from __future__ import annotations

from datetime import UTC, datetime
from enum import StrEnum
from typing import Any

from pydantic import BaseModel, Field


class VerdictKind(StrEnum):
    """Per-field adjudication result."""

    confirmed = "confirmed"  # declared matches observed
    mismatch = "mismatch"  # declared != observed, observed has higher truth value
    missing = "missing"  # declared is None/empty but observed exists
    ambiguous = "ambiguous"  # cannot decide deterministically, needs LLM witness


class VerdictAction(StrEnum):
    """Overall book-level action (after all fields aggregated)."""

    no_change = "no_change"
    suggest_fix = "suggest_fix"
    needs_review = "needs_review"
    defer = "defer"


class EvidenceSpan(BaseModel):
    """A cited piece of content that supports a verdict.

    Anchored to a location in the source so the UI can render it inline.
    """

    source: str  # title_page | copyright_page | ocr | cover | header | isbn_block
    text: str  # the literal substring that supports the verdict
    page_range: str | None = None  # e.g. "1-3" or "p.42"
    confidence: int = Field(ge=0, le=100)
    locator: str | None = None  # free-form: "line 3", "regex match", etc.


class JudgeCall(BaseModel):
    """Transparent log of every LLM invocation that influenced a verdict.

    `ambiguous` verdicts MUST list at least one JudgeCall before being marked final.
    """

    provider: str  # openai | ollama | google | lmstudio
    model: str
    prompt_hash: str  # sha256 of the rendered prompt
    response_hash: str  # sha256 of the response
    tokens_in: int
    tokens_out: int
    duration_ms: int
    cost_usd: float = 0.0


class FieldVerdict(BaseModel):
    """Per-field adjudication result."""

    field: str  # title | authors | isbn | publisher | published_date | language | series | series_index | volume | chapter | series_position | cover | tags
    declared_value: Any
    observed_value: Any | None = None
    verdict: VerdictKind
    confidence: int = Field(ge=0, le=100)
    evidence: list[EvidenceSpan] = Field(default_factory=list)
    judge_calls: list[JudgeCall] = Field(default_factory=list)
    requires_review: bool = False
    risk_flags: list[str] = Field(default_factory=list)
    reason: str | None = None
    is_deterministic: bool = True  # True if verdict came from rules (no LLM)
    created_at: datetime = Field(default_factory=lambda: datetime.now(UTC))


class BookVerdict(BaseModel):
    """Aggregated verdict for a single book across all fields."""

    book_key: str
    run_id: str
    field_verdicts: dict[str, FieldVerdict] = Field(default_factory=dict)
    overall_confidence: int = 0
    risk_flags: list[str] = Field(default_factory=list)
    action: VerdictAction = VerdictAction.no_change
    auto_apply_eligible: bool = False
    proposed_patch: dict[str, Any] = Field(default_factory=dict)
    reasons: list[str] = Field(default_factory=list)
    created_at: datetime = Field(default_factory=lambda: datetime.now(UTC))


# Conservative auto-apply gate thresholds
AUTO_APPLY_MIN_CONFIDENCE: int = 80
AUTO_APPLY_MIN_FIELD_CONFIDENCE: int = 75

# Flags that ALWAYS force needs_review regardless of confidence.
HIGH_RISK_FLAGS: frozenset[str] = frozenset(
    {
        "author_swap",
        "isbn_conflict",
        "edition_ambiguous",
        "cover_mismatch",
        "wrong_book",
        "series_mismatch",
        "publisher_mismatch",
    }
)

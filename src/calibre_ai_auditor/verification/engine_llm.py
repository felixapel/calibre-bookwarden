"""LLM witness for v1.0 ambiguous-field adjudication.

When the deterministic ContentVerificationEngine returns `VerdictKind.ambiguous`
for a field (e.g. mid-range title fuzzy match, missing-extraction conflict),
the engine marks it as needs_review and emits a JudgeCall-shaped prompt.

LLMWitness:
  - Takes the deterministic verdict + extracted evidence + declared value
  - Builds a strict structured prompt that names the discrepancy and asks the
    LLM to adjudicate, NOT invent
  - Routes to the best available homelab host via HostRegistry + LLMRouter
  - Parses the response into a refined FieldVerdict
  - Records every JudgeCall for transparency (cost, tokens, hash, duration)
  - Honors privacy settings (allow_remote_text, allow_remote_images,
    max_remote_chars) via LLMRouter's existing filters
  - Records LLM response cache by prompt-hash for replay

The LLM is NEVER the primary truth source.  It only resolves cases the
deterministic rules explicitly cannot.
"""

from __future__ import annotations

import hashlib
import json
import logging
import time
from dataclasses import dataclass, field
from typing import Any

from calibre_ai_auditor.verification.verdict import (
    EvidenceSpan,
    FieldVerdict,
    JudgeCall,
    VerdictKind,
)

logger = logging.getLogger(__name__)


# ---------------------------------------------------------------------------
# Prompt contract — the LLM must respond with this exact JSON shape
# ---------------------------------------------------------------------------

WITNESS_SCHEMA: dict[str, Any] = {
    "type": "object",
    "properties": {
        "field": {"type": "string"},
        "verdict": {
            "type": "string",
            "enum": ["confirmed", "mismatch", "missing", "still_ambiguous"],
        },
        "confidence": {"type": "integer", "minimum": 0, "maximum": 100},
        "selected_value": {
            "type": ["string", "array", "number", "boolean", "null"],
        },
        "reasons": {
            "type": "array",
            "items": {"type": "string"},
            "minItems": 1,
        },
        "evidence_quote": {
            "type": ["string", "null"],
            "description": "The exact substring from the book that supports your verdict.",
        },
        "evidence_page": {
            "type": ["string", "null"],
        },
        "risk_flags": {
            "type": "array",
            "items": {
                "type": "string",
                "enum": [
                    "author_swap",
                    "isbn_conflict",
                    "edition_ambiguous",
                    "cover_mismatch",
                    "wrong_book",
                    "series_mismatch",
                    "publisher_mismatch",
                    "title_noisy",
                    "language_mismatch",
                ],
            },
        },
    },
    "required": [
        "field",
        "verdict",
        "confidence",
        "selected_value",
        "reasons",
        "evidence_quote",
    ],
    "additionalProperties": False,
}


_SYSTEM_PROMPT = """You are a library-metadata adjudicator, not a generator. Your only job is
to decide whether the metadata Calibre currently has for a book matches what the
book actually contains.

RULES:
1. Only respond with the JSON schema requested. No prose, no markdown.
2. If the book content supports the declared value, return verdict="confirmed".
3. If the book content disagrees with the declared value, return verdict="mismatch"
   and set selected_value to what the book actually says.
4. If the book content is missing or unreadable, return verdict="still_ambiguous".
5. ALWAYS cite the exact substring from the book that supports your verdict in
   evidence_quote. If you cannot, your confidence must be < 50.
6. NEVER invent metadata. If you are unsure, return still_ambiguous.
7. NEVER change a field's risk_flags to less severe than the deterministic engine's.
8. If the declared value and the book content differ only by trivial formatting
   (punctuation, edition tags like "(Special Edition)", transliteration), treat
   them as confirmed and note the variance in reasons.
"""


# ---------------------------------------------------------------------------
# Response cache
# ---------------------------------------------------------------------------


@dataclass
class WitnessCacheEntry:
    response: dict[str, Any]
    judge_call: JudgeCall | None
    created_at: float = field(default_factory=time.time)


class WitnessCache:
    """In-process LLM response cache keyed by prompt-hash.

    For production scale this should move to Valkey (already in the stack) but
    the interface stays the same.
    """

    def __init__(self, max_size: int = 10_000, ttl_seconds: int = 24 * 3600):
        self.max_size = max_size
        self.ttl = ttl_seconds
        self._entries: dict[str, WitnessCacheEntry] = {}

    @staticmethod
    def _hash(payload: dict[str, Any]) -> str:
        canonical = json.dumps(payload, sort_keys=True, ensure_ascii=False)
        return hashlib.sha256(canonical.encode("utf-8")).hexdigest()

    def get(self, prompt_hash: str) -> WitnessCacheEntry | None:
        entry = self._entries.get(prompt_hash)
        if entry is None:
            return None
        if time.time() - entry.created_at > self.ttl:
            self._entries.pop(prompt_hash, None)
            return None
        return entry

    def put(self, prompt_hash: str, entry: WitnessCacheEntry) -> None:
        if len(self._entries) >= self.max_size:
            # Evict oldest by created_at
            oldest = min(self._entries.items(), key=lambda kv: kv[1].created_at)
            self._entries.pop(oldest[0], None)
        self._entries[prompt_hash] = entry


# ---------------------------------------------------------------------------
# Prompt builder
# ---------------------------------------------------------------------------


def build_witness_prompt(
    *,
    field_name: str,
    declared_value: Any,
    observed_value: Any | None,
    observed_evidence: list[EvidenceSpan],
    deterministic_verdict: VerdictKind,
    deterministic_confidence: int,
    deterministic_reason: str | None,
    book_title: str | None = None,
    book_authors: list[str] | None = None,
    snippet_chars: int = 2000,
) -> list[dict[str, Any]]:
    """Build the user-turn message list for the LLM witness.

    Returns a list[dict] in the format LLMRouter.execute_structured expects.
    """
    # Truncate snippet text to respect remote privacy caps
    snippet_block = ""
    if observed_evidence:
        lines = []
        for ev in observed_evidence[:3]:
            text = ev.text or ""
            if len(text) > snippet_chars:
                text = text[:snippet_chars] + "..."
            lines.append(f"  - source={ev.source} page={ev.page_range or '?'} conf={ev.confidence}: {text!r}")
        snippet_block = "\n".join(lines)

    parts: list[str] = []
    parts.append(f"FIELD TO ADJUDICATE: {field_name}")
    parts.append("")
    parts.append(f"DECLARED (Calibre): {declared_value!r}")
    parts.append(f"OBSERVED (extracted from book): {observed_value!r}")
    parts.append("")
    parts.append(
        f"DETERMINISTIC ENGINE SAID: verdict={deterministic_verdict.value} confidence={deterministic_confidence}"
    )
    if deterministic_reason:
        parts.append(f"  reason: {deterministic_reason}")
    parts.append("")
    if book_title or book_authors:
        parts.append("BOOK CONTEXT (for disambiguation):")
        if book_title:
            parts.append(f"  title (from book content): {book_title!r}")
        if book_authors:
            parts.append(f"  authors (from book content): {book_authors!r}")
        parts.append("")
    if snippet_block:
        parts.append("EXTRACTED EVIDENCE SPANS:")
        parts.append(snippet_block)
        parts.append("")
    parts.append("Return JSON. verdict=still_ambiguous if you cannot decide.")

    user_text = "\n".join(parts)
    return [
        {"role": "system", "content": _SYSTEM_PROMPT},
        {"role": "user", "content": user_text},
    ]


# ---------------------------------------------------------------------------
# Witness runner — calls LLMRouter.execute_structured
# ---------------------------------------------------------------------------


@dataclass
class WitnessConfig:
    """Settings for the LLM witness."""

    provider_hint: str = "auto"  # "auto" | "local" | "remote"
    max_remote_chars: int = 4000
    cost_per_book_cap_usd: float = 0.02
    cache_responses: bool = True


@dataclass
class WitnessResult:
    """The outcome of a single field witness call."""

    field: str
    success: bool
    refined_verdict: FieldVerdict | None = None
    judge_call: JudgeCall | None = None
    error: str | None = None
    cache_hit: bool = False


class LLMWitness:
    """Calls an LLM to adjudicate ambiguous fields.

    Routes through the existing LLMRouter so privacy filters and provider
    selection are honored.  Caches every response by prompt hash for replay.
    """

    def __init__(
        self,
        router: Any,  # calibre_ai_auditor.llm.router.LLMRouter — typed as Any to avoid circular import
        config: WitnessConfig | None = None,
        cache: WitnessCache | None = None,
    ):
        self.router = router
        self.config = config or WitnessConfig()
        self.cache = cache or WitnessCache()

    async def witness_field(
        self,
        *,
        field_name: str,
        current_fv: FieldVerdict,
        book_title: str | None = None,
        book_authors: list[str] | None = None,
    ) -> WitnessResult:
        """Refine an ambiguous FieldVerdict by calling an LLM.

        If the current_fv is not ambiguous OR the LLM call fails, returns
        a WitnessResult with success=False and the original verdict preserved.
        """
        if current_fv.verdict != VerdictKind.ambiguous:
            return WitnessResult(
                field=field_name,
                success=False,
                error=f"field is {current_fv.verdict.value}, not ambiguous",
            )

        messages = build_witness_prompt(
            field_name=field_name,
            declared_value=current_fv.declared_value,
            observed_value=current_fv.observed_value,
            observed_evidence=current_fv.evidence,
            deterministic_verdict=current_fv.verdict,
            deterministic_confidence=current_fv.confidence,
            deterministic_reason=current_fv.reason,
            book_title=book_title,
            book_authors=book_authors,
            snippet_chars=self.config.max_remote_chars,
        )

        prompt_hash = WitnessCache._hash({"messages": messages, "schema": WITNESS_SCHEMA})

        # Cache lookup
        if self.config.cache_responses:
            cached = self.cache.get(prompt_hash)
            if cached is not None:
                logger.debug("Witness cache hit for %s (hash=%s)", field_name, prompt_hash[:8])
                refined = self._build_refined_verdict(field_name, current_fv, cached.response)
                return WitnessResult(
                    field=field_name,
                    success=True,
                    refined_verdict=refined,
                    judge_call=cached.judge_call,
                    cache_hit=True,
                )

        # Live LLM call via LLMRouter.execute_structured
        from calibre_ai_auditor.llm.schemas import LLMRequest

        task = "deep_reasoning"
        req = LLMRequest(
            messages=messages,
            model=str(getattr(self.router.settings, "judge_model", "auto")),
            temperature=0.0,
            json_schema=True,
        )

        started = time.monotonic()
        try:
            response = await self.router.execute_structured(task, req, WITNESS_SCHEMA)
            duration_ms = int((time.monotonic() - started) * 1000)

            # Parse the response into a dict
            content = response.content
            if isinstance(content, str):
                try:
                    parsed = json.loads(content)
                except json.JSONDecodeError as e:
                    return WitnessResult(
                        field=field_name,
                        success=False,
                        error=f"LLM returned non-JSON: {e}",
                    )
            elif isinstance(content, dict):
                parsed = content
            else:
                return WitnessResult(
                    field=field_name,
                    success=False,
                    error=f"Unexpected response type: {type(content)}",
                )

            # Resolve provider/model from router state
            provider = self.router.get_provider_for_task(task)
            tokens_in = int(parsed.get("_tokens_in", 0))
            tokens_out = int(parsed.get("_tokens_out", 0))

            judge_call = JudgeCall(
                provider=provider.name,
                model=req.model,
                prompt_hash=prompt_hash[:16],
                response_hash=hashlib.sha256(json.dumps(parsed, sort_keys=True).encode("utf-8")).hexdigest()[:16],
                tokens_in=tokens_in,
                tokens_out=tokens_out,
                duration_ms=duration_ms,
                cost_usd=0.0,
            )

            if self.config.cache_responses:
                self.cache.put(
                    prompt_hash,
                    WitnessCacheEntry(response=parsed, judge_call=judge_call),
                )

            refined = self._build_refined_verdict(field_name, current_fv, parsed, judge_call)
            return WitnessResult(
                field=field_name,
                success=True,
                refined_verdict=refined,
                judge_call=judge_call,
            )
        except Exception as e:
            logger.warning("LLM witness failed for %s: %s", field_name, e)
            return WitnessResult(field=field_name, success=False, error=str(e))

    def _build_refined_verdict(
        self,
        field_name: str,
        original: FieldVerdict,
        parsed: dict[str, Any],
        judge_call: JudgeCall | None = None,
    ) -> FieldVerdict:
        """Convert the LLM's JSON response back into a FieldVerdict.

        Critical: never downgrade a risk flag.  If the deterministic engine
        flagged something the LLM didn't, keep it.
        """
        verdict_str = str(parsed.get("verdict", "still_ambiguous")).lower()
        verdict_map = {
            "confirmed": VerdictKind.confirmed,
            "mismatch": VerdictKind.mismatch,
            "missing": VerdictKind.missing,
            "still_ambiguous": VerdictKind.ambiguous,
        }
        verdict = verdict_map.get(verdict_str, VerdictKind.ambiguous)

        confidence = int(parsed.get("confidence", 50))
        confidence = max(0, min(100, confidence))

        selected_value = parsed.get("selected_value", original.observed_value)

        reasons = parsed.get("reasons") or []
        if isinstance(reasons, str):
            reasons = [reasons]

        evidence_quote = parsed.get("evidence_quote")
        evidence_page = parsed.get("evidence_page")

        evidence: list[EvidenceSpan] = list(original.evidence)
        if evidence_quote:
            evidence.append(
                EvidenceSpan(
                    source="llm_witness",
                    text=str(evidence_quote),
                    page_range=evidence_page,
                    confidence=confidence,
                )
            )

        # Merge risk flags — never downgrade
        llm_flags = set(parsed.get("risk_flags") or [])
        merged_flags = set(original.risk_flags) | llm_flags

        judge_calls: list[JudgeCall] = list(original.judge_calls)
        if judge_call:
            judge_calls.append(judge_call)

        # If LLM says still_ambiguous, preserve the original confidence+reason
        # but mark is_deterministic=False.
        final_reason = "; ".join(str(r) for r in reasons) if reasons else original.reason

        return FieldVerdict(
            field=field_name,
            declared_value=original.declared_value,
            observed_value=selected_value,
            verdict=verdict,
            confidence=confidence if verdict != VerdictKind.ambiguous else original.confidence,
            evidence=evidence,
            judge_calls=judge_calls,
            requires_review=(verdict == VerdictKind.ambiguous) or original.requires_review,
            risk_flags=sorted(merged_flags),
            reason=final_reason,
            is_deterministic=False,
        )


__all__ = [
    "LLMWitness",
    "WitnessCache",
    "WitnessConfig",
    "WitnessResult",
    "WITNESS_SCHEMA",
    "build_witness_prompt",
]

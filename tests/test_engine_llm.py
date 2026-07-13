"""Recorded-cassette tests for the v1.0 LLM witness.

These tests replay pre-recorded LLM responses (no network in CI) to verify
that the LLMWitness:
  1. Builds the correct prompt for an ambiguous field
  2. Replays the recorded response verbatim (cache hit path)
  3. Converts the response into a refined FieldVerdict correctly
  4. Honors the "never downgrade risk flags" invariant
  5. Handles all 4 verdict outcomes (confirmed / mismatch / missing / still_ambiguous)
  6. Handles the "no evidence" edge case
  7. Is a no-op for non-ambiguous fields

Cassettes live in tests/cassettes/*.json — recording tool TBD (future VCR-style
recorder).  For now they are hand-crafted from real LLM outputs.
"""

from __future__ import annotations

import json
from pathlib import Path
from typing import Any
from unittest.mock import AsyncMock, MagicMock

import pytest

from calibre_ai_auditor.verification.engine_llm import (
    LLMWitness,
    WitnessCache,
    WitnessCacheEntry,
    WitnessConfig,
    build_witness_prompt,
)
from calibre_ai_auditor.verification.verdict import (
    EvidenceSpan,
    FieldVerdict,
    VerdictKind,
)

CASSETTE_DIR = Path(__file__).parent / "cassettes"


def _load_cassette(name: str) -> dict[str, Any]:
    path = CASSETTE_DIR / f"{name}.json"
    return json.loads(path.read_text())


def _build_deterministic_fv(cassette: dict[str, Any]) -> FieldVerdict:
    """Build the pre-witness FieldVerdict from the cassette's input section."""
    i = cassette["input"]
    return FieldVerdict(
        field=i["field_name"],
        declared_value=i["declared_value"],
        observed_value=i.get("observed_value"),
        verdict=VerdictKind(i["deterministic_verdict"]),
        confidence=i["deterministic_confidence"],
        evidence=[
            EvidenceSpan(
                source=e["source"],
                text=e["text"],
                page_range=e.get("page_range"),
                confidence=e["confidence"],
            )
            for e in i.get("evidence", [])
        ],
        reason=f"deterministic test fixture from {cassette['name']}",
    )


def _make_mock_router(response_payload: dict[str, Any]) -> MagicMock:
    """Build a mock LLMRouter that returns the recorded response verbatim."""
    router = MagicMock()
    router.settings = MagicMock()
    router.settings.judge_model = "test-judge-model"
    provider = MagicMock()
    provider.name = "recorded-cassette"
    provider.is_local = True
    router.get_provider_for_task = MagicMock(return_value=provider)
    router.execute_structured = AsyncMock(return_value=MagicMock(content=json.dumps(response_payload)))
    return router


# ---------------------------------------------------------------------------
# Tests
# ---------------------------------------------------------------------------


@pytest.mark.asyncio
async def test_witness_replays_recorded_confirms_match() -> None:
    """LLM witness confirms a fuzzy-match-ambiguous title with 92% confidence."""
    cassette = _load_cassette("title_witness_confirms_match")
    fv = _build_deterministic_fv(cassette)
    router = _make_mock_router(cassette["recorded_response"])
    witness = LLMWitness(router, WitnessConfig(cache_responses=False))

    result = await witness.witness_field(
        field_name="title",
        current_fv=fv,
    )

    assert result.success
    assert result.refined_verdict is not None
    assert result.refined_verdict.verdict == VerdictKind.confirmed
    assert result.refined_verdict.confidence == 92
    assert result.refined_verdict.observed_value == "The Hunchback of Notre-Dame"
    assert result.refined_verdict.is_deterministic is False
    assert result.judge_call is not None
    assert result.judge_call.provider == "recorded-cassette"
    # Evidence: original + LLM witness citation
    assert any(e.source == "llm_witness" for e in result.refined_verdict.evidence)


@pytest.mark.asyncio
async def test_witness_replays_recorded_detects_mismatch() -> None:
    """LLM witness returns mismatch for 'Moby Dick' vs canonical 'Moby-Dick; or, The Whale'."""
    cassette = _load_cassette("title_witness_detects_real_mismatch")
    fv = _build_deterministic_fv(cassette)
    router = _make_mock_router(cassette["recorded_response"])
    witness = LLMWitness(router, WitnessConfig(cache_responses=False))

    result = await witness.witness_field(field_name="title", current_fv=fv)

    assert result.success
    assert result.refined_verdict is not None
    assert result.refined_verdict.verdict == VerdictKind.mismatch
    assert result.refined_verdict.confidence == 88
    assert "Moby-Dick" in str(result.refined_verdict.observed_value)


@pytest.mark.asyncio
async def test_witness_refuses_to_guess_on_no_evidence() -> None:
    """LLM witness returns still_ambiguous when no evidence is provided."""
    cassette = _load_cassette("title_witness_refuses_to_guess")
    fv = _build_deterministic_fv(cassette)
    router = _make_mock_router(cassette["recorded_response"])
    witness = LLMWitness(router, WitnessConfig(cache_responses=False))

    result = await witness.witness_field(field_name="title", current_fv=fv)

    assert result.success
    assert result.refined_verdict is not None
    assert result.refined_verdict.verdict == VerdictKind.ambiguous
    assert result.refined_verdict.confidence <= 20  # low confidence


@pytest.mark.asyncio
async def test_witness_detects_author_swap_with_risk_flag() -> None:
    """LLM witness must set author_swap risk flag when it detects a real author swap."""
    cassette = _load_cassette("author_witness_author_swap")
    fv = _build_deterministic_fv(cassette)
    router = _make_mock_router(cassette["recorded_response"])
    witness = LLMWitness(router, WitnessConfig(cache_responses=False))

    result = await witness.witness_field(field_name="authors", current_fv=fv)

    assert result.success
    assert result.refined_verdict is not None
    assert result.refined_verdict.verdict == VerdictKind.mismatch
    assert "author_swap" in result.refined_verdict.risk_flags


@pytest.mark.asyncio
async def test_witness_is_noop_for_confirmed_field() -> None:
    """Witness should not call the LLM when the field is already confirmed."""
    fv = FieldVerdict(
        field="title",
        declared_value="The Great Gatsby",
        observed_value="The Great Gatsby",
        verdict=VerdictKind.confirmed,
        confidence=99,
        reason="exact match",
    )
    router = _make_mock_router({"any": "thing"})
    witness = LLMWitness(router, WitnessConfig(cache_responses=False))

    result = await witness.witness_field(field_name="title", current_fv=fv)

    assert not result.success
    assert "not ambiguous" in (result.error or "")
    router.execute_structured.assert_not_called()


@pytest.mark.asyncio
async def test_witness_cache_replay_avoids_second_llm_call() -> None:
    """Second call with same prompt should hit the cache."""
    cassette = _load_cassette("title_witness_confirms_match")
    fv = _build_deterministic_fv(cassette)
    router = _make_mock_router(cassette["recorded_response"])
    witness = LLMWitness(router, WitnessConfig(cache_responses=True))

    r1 = await witness.witness_field(field_name="title", current_fv=fv)
    r2 = await witness.witness_field(field_name="title", current_fv=fv)

    assert r1.success
    assert r2.success
    assert not r1.cache_hit
    assert r2.cache_hit
    # Mock's execute_structured should have been called only once
    assert router.execute_structured.call_count == 1


@pytest.mark.asyncio
async def test_witness_handles_provider_error_gracefully() -> None:
    """Witness must not crash if the provider raises; just returns success=False."""
    fv = FieldVerdict(
        field="title",
        declared_value="A",
        observed_value="B",
        verdict=VerdictKind.ambiguous,
        confidence=50,
    )
    router = MagicMock()
    router.settings = MagicMock()
    router.settings.judge_model = "x"
    provider = MagicMock()
    provider.name = "broken"
    router.get_provider_for_task = MagicMock(return_value=provider)
    router.execute_structured = AsyncMock(side_effect=ConnectionError("host unreachable"))
    witness = LLMWitness(router, WitnessConfig(cache_responses=False))

    result = await witness.witness_field(field_name="title", current_fv=fv)

    assert not result.success
    assert "unreachable" in (result.error or "")


@pytest.mark.asyncio
async def test_witness_does_not_downgrade_risk_flags() -> None:
    """If the deterministic verdict had a risk flag, the refined verdict must keep it."""
    cassette = _load_cassette("author_witness_author_swap")
    fv = _build_deterministic_fv(cassette)
    # Pre-flag something the LLM might omit
    fv.risk_flags = ["edition_ambiguous"]
    router = _make_mock_router(cassette["recorded_response"])  # only sets author_swap
    witness = LLMWitness(router, WitnessConfig(cache_responses=False))

    result = await witness.witness_field(field_name="authors", current_fv=fv)

    assert result.success
    assert result.refined_verdict is not None
    # Must keep BOTH flags
    assert "edition_ambiguous" in result.refined_verdict.risk_flags
    assert "author_swap" in result.refined_verdict.risk_flags


def test_witness_prompt_contains_required_context() -> None:
    """The built prompt must include all the context the LLM needs to adjudicate."""
    ev = [
        EvidenceSpan(source="title_page", text="X by Y", page_range="1", confidence=80),
    ]
    msgs = build_witness_prompt(
        field_name="title",
        declared_value="X",
        observed_value="Y",
        observed_evidence=ev,
        deterministic_verdict=VerdictKind.ambiguous,
        deterministic_confidence=50,
        deterministic_reason="ratio 0.62",
        book_title="X",
        book_authors=["Y"],
    )
    assert len(msgs) == 2
    assert msgs[0]["role"] == "system"
    assert msgs[1]["role"] == "user"
    user_text = msgs[1]["content"]
    assert "FIELD TO ADJUDICATE: title" in user_text
    assert "DECLARED (Calibre)" in user_text
    assert "OBSERVED (extracted from book)" in user_text
    assert "still_ambiguous" in user_text  # safety fallback instruction


def test_witness_cache_eviction() -> None:
    """Cache evicts oldest entries when max_size is exceeded."""
    cache = WitnessCache(max_size=3, ttl_seconds=60)

    for i in range(5):
        cache.put(f"k{i}", WitnessCacheEntry(response={"v": i}, judge_call=None))

    # Only the last 3 should remain
    for i in range(2):
        assert cache.get(f"k{i}") is None
    for i in range(2, 5):
        assert cache.get(f"k{i}") is not None


def test_witness_cache_hash_is_deterministic() -> None:
    """Same prompt + schema → same hash, regardless of dict key order."""
    from calibre_ai_auditor.verification.engine_llm import WitnessCache

    payload1 = {"a": 1, "b": 2}
    payload2 = {"b": 2, "a": 1}
    assert WitnessCache._hash(payload1) == WitnessCache._hash(payload2)

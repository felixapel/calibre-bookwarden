"""LLM witness cache hit/miss latency benchmark.

Measures the cost difference between cache hits and cache misses for the
LLMWitness. The cache hit should be ~1000x faster than the LLM call.

This is the key metric for the privacy/cost story: cache hits cost nothing,
LLM misses cost real money + latency.

Run: pytest --benchmark-only tests/benchmarks/test_bench_llm_witness.py
"""

from __future__ import annotations

import json
from unittest.mock import AsyncMock, MagicMock

import pytest

from calibre_ai_auditor.verification.engine_llm import (
    LLMWitness,
    WitnessCache,
    WitnessConfig,
)
from calibre_ai_auditor.verification.verdict import (
    EvidenceSpan,
    FieldVerdict,
    VerdictKind,
)


pytestmark = pytest.mark.benchmark


def _make_ambiguous_fv(field: str = "title") -> FieldVerdict:
    return FieldVerdict(
        field=field,
        declared_value="Ambiguous Declared",
        observed_value="Ambiguous Observed",
        verdict=VerdictKind.ambiguous,
        confidence=55,
        evidence=[
            EvidenceSpan(source="title_page", text="Some text", page_range="1", confidence=70),
        ],
        reason="deterministic engine returned ambiguous",
    )


def _make_mock_router(response_payload: dict, latency_ms: int = 50) -> MagicMock:
    """Mock router with controllable latency."""
    import asyncio

    router = MagicMock()
    router.settings = MagicMock()
    router.settings.judge_model = "bench-model"
    provider = MagicMock()
    provider.name = "bench-mock"
    provider.is_local = True
    router.get_provider_for_task = MagicMock(return_value=provider)
    router.execute_structured = AsyncMock(return_value=MagicMock(content=json.dumps(response_payload)))

    async def _slow_structured(*_args, **_kwargs):
        await asyncio.sleep(latency_ms / 1000.0)
        return MagicMock(content=json.dumps(response_payload))

    router.execute_structured = _slow_structured
    return router


CANNED_RESPONSE = {
    "field": "title",
    "verdict": "confirmed",
    "confidence": 90,
    "selected_value": "Observed",
    "reasons": ["evidence supports match"],
    "evidence_quote": "Some text",
    "evidence_page": "1",
    "risk_flags": [],
}


@pytest.mark.benchmark(group="witness_cache_miss")
def test_bench_witness_cache_miss_50ms_latency(benchmark) -> None:
    """Witness call with NO cache, 50ms simulated LLM latency."""
    import asyncio

    router = _make_mock_router(CANNED_RESPONSE, latency_ms=50)
    cache = WitnessCache()
    witness = LLMWitness(router, WitnessConfig(cache_responses=True), cache=cache)

    fv = _make_ambiguous_fv()

    async def _run() -> None:
        result = await witness.witness_field(field_name="title", current_fv=fv)
        assert result.success

    benchmark(lambda: asyncio.run(_run()))
    benchmark.extra_info["simulated_llm_latency_ms"] = 50


@pytest.mark.benchmark(group="witness_cache_miss")
def test_bench_witness_cache_miss_500ms_latency(benchmark) -> None:
    """Witness call with NO cache, 500ms simulated LLM latency (homelab)."""
    import asyncio

    router = _make_mock_router(CANNED_RESPONSE, latency_ms=500)
    cache = WitnessCache()
    witness = LLMWitness(router, WitnessConfig(cache_responses=True), cache=cache)

    fv = _make_ambiguous_fv()

    async def _run() -> None:
        result = await witness.witness_field(field_name="title", current_fv=fv)
        assert result.success

    benchmark(lambda: asyncio.run(_run()))
    benchmark.extra_info["simulated_llm_latency_ms"] = 500


@pytest.mark.benchmark(group="witness_cache_hit")
def test_bench_witness_cache_hit(benchmark) -> None:
    """Witness call with cache HIT (no LLM call)."""
    import asyncio

    router = _make_mock_router(CANNED_RESPONSE, latency_ms=5000)  # 5s if cache misses
    cache = WitnessCache()
    witness = LLMWitness(router, WitnessConfig(cache_responses=True), cache=cache)
    fv = _make_ambiguous_fv()

    # Prime the cache
    async def _prime() -> None:
        await witness.witness_field(field_name="title", current_fv=fv)

    asyncio.run(_prime())

    async def _run() -> None:
        result = await witness.witness_field(field_name="title", current_fv=fv)
        assert result.success
        assert result.cache_hit

    benchmark(lambda: asyncio.run(_run()))
    benchmark.extra_info["cache_size"] = cache.max_size


@pytest.mark.benchmark(group="witness_cache_operations")
def test_bench_witness_cache_put_get(benchmark) -> None:
    """WitnessCache put+get operation (the building block of cache hit)."""
    import hashlib

    cache = WitnessCache(max_size=100_000)
    payload = json.dumps({"foo": "bar" * 100, "baz": [1, 2, 3, 4, 5] * 10})

    def _run() -> None:
        for i in range(1000):
            key = hashlib.sha256(f"key:{i}".encode()).hexdigest()
            from calibre_ai_auditor.verification.engine_llm import WitnessCacheEntry

            cache.put(key, WitnessCacheEntry(response=json.loads(payload), judge_call=None))
            _ = cache.get(key)

    benchmark(_run)


@pytest.mark.benchmark(group="witness_prompt_build")
def test_bench_witness_prompt_build(benchmark) -> None:
    """Cost of building the witness prompt (no LLM call)."""
    from calibre_ai_auditor.verification.engine_llm import build_witness_prompt

    fv = _make_ambiguous_fv()

    def _run() -> None:
        for _ in range(1000):
            build_witness_prompt(
                field_name="title",
                declared_value=fv.declared_value,
                observed_value=fv.observed_value,
                observed_evidence=fv.evidence,
                deterministic_verdict=fv.verdict,
                deterministic_confidence=fv.confidence,
                deterministic_reason=fv.reason,
                snippet_chars=4000,
            )

    benchmark(_run)
    benchmark.extra_info["prompts_per_run"] = 1000

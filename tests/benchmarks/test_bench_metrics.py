"""Metrics render benchmark.

Measures the Prometheus text-exposition rendering throughput.
At 10k metrics, render time directly affects scrape latency on /metrics.

Run: pytest --benchmark-only tests/benchmarks/test_bench_metrics.py
"""

from __future__ import annotations

import pytest

from calibre_ai_auditor.verification.metrics import Metrics

pytestmark = pytest.mark.benchmark


@pytest.mark.benchmark(group="metrics_render")
def test_bench_metrics_render_empty(benchmark) -> None:
    """Render with no metrics (baseline)."""
    m = Metrics()

    def _run() -> None:
        m.render()

    benchmark(_run)
    benchmark.extra_info["metrics_count"] = 0


@pytest.mark.benchmark(group="metrics_render")
def test_bench_metrics_render_100(benchmark) -> None:
    """Render with 100 distinct counter+label combinations."""
    m = Metrics()
    for i in range(100):
        m.record_book_action("suggest_fix", f"run_{i}")

    def _run() -> None:
        m.render()

    benchmark(_run)
    benchmark.extra_info["metrics_count"] = 100


@pytest.mark.benchmark(group="metrics_render")
def test_bench_metrics_render_1000(benchmark) -> None:
    """Render with 1k metrics."""
    m = Metrics()
    for i in range(1000):
        m.record_book_action("suggest_fix", f"run_{i}")
    for i in range(1000):
        m.inc("custom", labels={"k": f"v{i}"})

    def _run() -> None:
        m.render()

    benchmark(_run)
    benchmark.extra_info["metrics_count"] = 2000


@pytest.mark.benchmark(group="metrics_render")
def test_bench_metrics_render_10000(benchmark) -> None:
    """Render with 10k metrics (the realistic scale for a long audit)."""
    m = Metrics()
    for i in range(5000):
        m.record_book_action("suggest_fix", f"run_{i}")
    for i in range(5000):
        m.record_llm_call(
            provider=f"prov_{i % 5}",
            model=f"model_{i % 10}",
            duration_ms=200,
            tokens_in=100,
            tokens_out=50,
        )

    def _run() -> None:
        m.render()

    benchmark(_run)
    benchmark.extra_info["metrics_count"] = 10000


@pytest.mark.benchmark(group="metrics_operations")
def test_bench_metrics_inc_100k(benchmark) -> None:
    """Throughput of metric increment (the hot path)."""
    m = Metrics()

    def _run() -> None:
        for _ in range(100_000):
            m.inc("hot_path_counter", labels={"kind": "a"})

    benchmark(_run)
    benchmark.extra_info["increments"] = 100_000

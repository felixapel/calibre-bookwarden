"""Full ContentVerificationEngine throughput benchmark.

Measures end-to-end engine performance: from declared/observed → BookVerdict.
The key metric is books/sec — this drives the 10k–50k book audit time estimates.

Baseline target (on RTX 3090 / 5060 Ti / equivalent): >50 books/sec
This means a 50k-book audit completes in ~17 minutes (engine only, no LLM).

Run: pytest --benchmark-only tests/benchmarks/test_bench_engine.py
"""

from __future__ import annotations

import time

import pytest

from calibre_ai_auditor.verification.engine import (
    ContentVerificationEngine,
    DeclaredMetadata,
    ObservationSet,
)

pytestmark = pytest.mark.benchmark


@pytest.fixture
def engine() -> ContentVerificationEngine:
    return ContentVerificationEngine()


@pytest.mark.benchmark(group="engine_throughput")
def test_bench_engine_100_books(benchmark, engine, factory) -> None:
    """100 books end-to-end through the engine."""
    corpus = factory.realistic_corpus(100)

    def _run() -> None:
        for i, book in enumerate(corpus):
            d = book["declared"]
            o = book["observed"]
            engine.verify(
                book_key=f"bench:book:{i}",
                run_id="bench_run",
                declared=DeclaredMetadata(
                    title=d["title"],
                    authors=d["authors"],
                    publisher=d["publisher"],
                    published_date=d["published_date"],
                    language=d["language"],
                    isbn=d["isbn"],
                ),
                observed=ObservationSet(
                    title_page_text="placeholder title page text " * 50,
                    body_sample="placeholder body sample " * 100,
                    title_extracted=o["title_extracted"],
                    authors_extracted=o["authors_extracted"],
                    isbn_extracted=o["isbn_extracted"],
                    publisher_extracted=o["publisher_extracted"],
                    date_extracted=o["date_extracted"],
                    language_detected=o["language_detected"],
                    evidence_quality="high",
                ),
            )

    benchmark(_run)
    benchmark.extra_info["books_per_run"] = len(corpus)
    # Throughput metric — books per second
    mean_s = benchmark.stats.stats.mean / 1000.0
    benchmark.extra_info["books_per_sec"] = round(len(corpus) / mean_s, 2)


@pytest.mark.benchmark(group="engine_throughput")
def test_bench_engine_1000_books(benchmark, engine, factory) -> None:
    """1000 books end-to-end (the realistic single-batch size)."""
    corpus = factory.realistic_corpus(1000)

    def _run() -> None:
        for i, book in enumerate(corpus):
            d = book["declared"]
            o = book["observed"]
            engine.verify(
                book_key=f"bench:book:{i}",
                run_id="bench_run",
                declared=DeclaredMetadata(
                    title=d["title"],
                    authors=d["authors"],
                    publisher=d["publisher"],
                    published_date=d["published_date"],
                    language=d["language"],
                    isbn=d["isbn"],
                ),
                observed=ObservationSet(
                    title_page_text="placeholder title page text " * 50,
                    body_sample="placeholder body sample " * 100,
                    title_extracted=o["title_extracted"],
                    authors_extracted=o["authors_extracted"],
                    isbn_extracted=o["isbn_extracted"],
                    publisher_extracted=o["publisher_extracted"],
                    date_extracted=o["date_extracted"],
                    language_detected=o["language_detected"],
                    evidence_quality="high",
                ),
            )

    benchmark(_run)
    benchmark.extra_info["books_per_run"] = len(corpus)
    mean_s = benchmark.stats.stats.mean / 1000.0
    benchmark.extra_info["books_per_sec"] = round(len(corpus) / mean_s, 2)


@pytest.mark.benchmark(group="engine_throughput")
def test_bench_engine_real_30_fixtures(benchmark, engine) -> None:
    """The 30 hand-crafted gold-truth synthetic fixtures (regression backbone)."""
    from tests.fixtures.synthetic_library.gold_truth import SYNTHETIC_FIXTURES
    from tests.test_verification_synthetic import _build_declared, _build_observed

    def _run() -> None:
        for fx in SYNTHETIC_FIXTURES:
            engine.verify(
                book_key=fx["id"],
                run_id="bench_run",
                declared=_build_declared(fx),
                observed=_build_observed(fx),
            )

    benchmark(_run)
    benchmark.extra_info["books_per_run"] = len(SYNTHETIC_FIXTURES)
    mean_s = benchmark.stats.stats.mean / 1000.0
    benchmark.extra_info["books_per_sec"] = round(len(SYNTHETIC_FIXTURES) / mean_s, 2)


@pytest.mark.benchmark(group="engine_throughput")
def test_bench_engine_scaling_check(engine, factory) -> None:
    """Non-benchmarked: measures throughput at 1k/5k/10k to confirm linear scaling."""
    sizes = [1000, 5000, 10000]
    results: dict[int, float] = {}
    for size in sizes:
        corpus = factory.realistic_corpus(size)
        start = time.monotonic()
        for i, book in enumerate(corpus):
            d = book["declared"]
            o = book["observed"]
            engine.verify(
                book_key=f"scaling:book:{i}",
                run_id="bench_run",
                declared=DeclaredMetadata(
                    title=d["title"],
                    authors=d["authors"],
                    publisher=d["publisher"],
                    published_date=d["published_date"],
                    language=d["language"],
                    isbn=d["isbn"],
                ),
                observed=ObservationSet(
                    title_extracted=o["title_extracted"],
                    authors_extracted=o["authors_extracted"],
                    isbn_extracted=o["isbn_extracted"],
                    publisher_extracted=o["publisher_extracted"],
                    date_extracted=o["date_extracted"],
                    language_detected=o["language_detected"],
                ),
            )
        elapsed = time.monotonic() - start
        results[size] = round(size / elapsed, 2)

    # Sanity: throughput should not degrade >2x as size grows
    assert results[10000] > results[1000] / 2, (
        f"Engine scaling looks bad: {results[10000]} books/sec at 10k vs {results[1000]} at 1k — non-linear slowdown"
    )
    # Print for the report
    for size, rate in results.items():
        print(f"\n  Scaling: {size:>6d} books → {rate:>6.1f} books/sec")

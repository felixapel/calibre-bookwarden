"""Per-rule latency benchmarks.

Measures the cost of each individual rule in the verification engine.
Helps identify bottlenecks — a slow rule on 10k books = significant cost.

Run: pytest --benchmark-only tests/benchmarks/test_bench_per_rule.py
"""

from __future__ import annotations

import pytest

from calibre_ai_auditor.verification.rules import (
    verify_authors,
    verify_isbn,
    verify_language,
    verify_published_date,
    verify_publisher,
    verify_series,
    verify_series_index,
    verify_title,
)


pytestmark = pytest.mark.benchmark


@pytest.mark.benchmark(group="rule_title")
def test_bench_verify_title_realistic(benchmark, factory) -> None:
    """Title rule over 1000 mixed declared/observed pairs."""
    pairs = [factory.random_title_pair() for _ in range(1000)]

    def _run() -> None:
        for p in pairs:
            verify_title(p["declared"], p["observed"])

    benchmark(_run)
    benchmark.extra_info["rule"] = "title"
    benchmark.extra_info["calls_per_run"] = len(pairs)


@pytest.mark.benchmark(group="rule_title")
def test_bench_verify_title_mismatch(benchmark) -> None:
    """Title rule with high-similarity mismatch (the expensive case)."""
    inputs = [
        ("Some Book Title", "Some Other Book"),
        ("The Great Novel", "The Great Novels"),
    ] * 500

    def _run() -> None:
        for d, o in inputs:
            verify_title(d, o)

    benchmark(_run)
    benchmark.extra_info["rule"] = "title"
    benchmark.extra_info["calls_per_run"] = len(inputs)


@pytest.mark.benchmark(group="rule_authors")
def test_bench_verify_authors_match(benchmark, factory) -> None:
    """Authors rule over 1000 exact-match pairs."""
    pairs = [factory.random_author_pair() for _ in range(1000)]

    def _run() -> None:
        for p in pairs:
            verify_authors(p["declared"], p["observed"])

    benchmark(_run)
    benchmark.extra_info["rule"] = "authors"
    benchmark.extra_info["calls_per_run"] = len(pairs)


@pytest.mark.benchmark(group="rule_authors")
def test_bench_verify_authors_with_transliteration(benchmark) -> None:
    """Authors rule with Cyrillic vs Latin — slowest path."""
    cyrillic_authors = [
        "Михаил Булгаков",
        "Лев Толстой",
        "Фёдор Достоевский",
    ] * 333
    latin_authors = [
        "Mikhail Bulgakov",
        "Leo Tolstoy",
        "Fyodor Dostoevsky",
    ] * 333

    def _run() -> None:
        for c, l in zip(cyrillic_authors, latin_authors):
            verify_authors([l], [c])

    benchmark(_run)
    benchmark.extra_info["rule"] = "authors"
    benchmark.extra_info["calls_per_run"] = len(cyrillic_authors)


@pytest.mark.benchmark(group="rule_isbn")
def test_bench_verify_isbn_match(benchmark, factory) -> None:
    """ISBN rule over 1000 valid ISBNs."""
    pairs = [factory.random_isbn_pair() for _ in range(1000)]

    def _run() -> None:
        for p in pairs:
            verify_isbn(p["declared"], p["observed"])

    benchmark(_run)
    benchmark.extra_info["rule"] = "isbn"
    benchmark.extra_info["calls_per_run"] = len(pairs)


@pytest.mark.benchmark(group="rule_isbn")
def test_bench_verify_isbn_invalid_checksum(benchmark) -> None:
    """ISBN rule with invalid checksum (the checksum-recompute path)."""
    inputs = [("9780451524935", "9780451524936")] * 1000

    def _run() -> None:
        for d, o in inputs:
            verify_isbn(d, o)

    benchmark(_run)
    benchmark.extra_info["rule"] = "isbn"
    benchmark.extra_info["calls_per_run"] = len(inputs)


@pytest.mark.benchmark(group="rule_publisher")
def test_bench_verify_publisher_match(benchmark, factory) -> None:
    """Publisher rule over 1000 pairs."""
    pairs = [factory.random_publisher_pair() for _ in range(1000)]

    def _run() -> None:
        for p in pairs:
            verify_publisher(p["declared"], p["observed"])

    benchmark(_run)
    benchmark.extra_info["rule"] = "publisher"
    benchmark.extra_info["calls_per_run"] = len(pairs)


@pytest.mark.benchmark(group="rule_date")
def test_bench_verify_published_date_match(benchmark, factory) -> None:
    """Date rule over 1000 pairs."""
    pairs = [factory.random_date_pair() for _ in range(1000)]

    def _run() -> None:
        for p in pairs:
            verify_published_date(p["declared"], p["observed"])

    benchmark(_run)
    benchmark.extra_info["rule"] = "date"
    benchmark.extra_info["calls_per_run"] = len(pairs)


@pytest.mark.benchmark(group="rule_language")
def test_bench_verify_language_match(benchmark, factory) -> None:
    """Language rule over 1000 pairs."""
    pairs = [factory.random_language_pair() for _ in range(1000)]

    def _run() -> None:
        for p in pairs:
            verify_language(p["declared"], p["observed"])

    benchmark(_run)
    benchmark.extra_info["rule"] = "language"
    benchmark.extra_info["calls_per_run"] = len(pairs)


@pytest.mark.benchmark(group="rule_series")
def test_bench_verify_series_no_header(benchmark) -> None:
    """Series rule when headers were not sampled (the cheap path)."""
    inputs = [("Foundation", None)] * 1000

    def _run() -> None:
        for d, o in inputs:
            verify_series(d, o, observed_any=False)

    benchmark(_run)
    benchmark.extra_info["rule"] = "series"
    benchmark.extra_info["calls_per_run"] = len(inputs)


@pytest.mark.benchmark(group="rule_series_index")
def test_bench_verify_series_index_present(benchmark) -> None:
    """Series index rule when both declared and observed exist."""
    inputs = [(1.0, 1.0), (2.0, 2.0), (3.5, 3.5)] * 333

    def _run() -> None:
        for d, o in inputs:
            verify_series_index(d, o)

    benchmark(_run)
    benchmark.extra_info["rule"] = "series_index"
    benchmark.extra_info["calls_per_run"] = len(inputs)


@pytest.mark.benchmark(group="rule_all_combined")
def test_bench_all_rules_8_fields(benchmark, factory) -> None:
    """All 8 rules together, simulating one book's full field-by-field check."""
    n = 100
    title_pairs = [factory.random_title_pair() for _ in range(n)]
    author_pairs = [factory.random_author_pair() for _ in range(n)]
    isbn_pairs = [factory.random_isbn_pair() for _ in range(n)]
    publisher_pairs = [factory.random_publisher_pair() for _ in range(n)]
    date_pairs = [factory.random_date_pair() for _ in range(n)]
    lang_pairs = [factory.random_language_pair() for _ in range(n)]
    series_pairs = [factory.random_series_pair() for _ in range(n)]
    series_idx_pairs = [(None, None)] * n

    def _run() -> None:
        for i in range(n):
            verify_title(title_pairs[i]["declared"], title_pairs[i]["observed"])
            verify_authors(author_pairs[i]["declared"], author_pairs[i]["observed"])
            verify_isbn(isbn_pairs[i]["declared"], isbn_pairs[i]["observed"])
            verify_publisher(publisher_pairs[i]["declared"], publisher_pairs[i]["observed"])
            verify_published_date(date_pairs[i]["declared"], date_pairs[i]["observed"])
            verify_language(lang_pairs[i]["declared"], lang_pairs[i]["observed"])
            verify_series(series_pairs[i]["declared"], series_pairs[i]["observed"], observed_any=False)
            verify_series_index(series_idx_pairs[i][0], series_idx_pairs[i][1])

    benchmark(_run)
    benchmark.extra_info["books_simulated"] = n

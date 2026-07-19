"""Miscellaneous infrastructure benchmarks.

Covers host discovery, restore point creation/cleanup, and content extraction
on a real EPUB file. These are the non-rule, non-engine pieces that also
contribute to overall audit latency.

Run: pytest --benchmark-only tests/benchmarks/test_bench_infrastructure.py
"""

from __future__ import annotations

from pathlib import Path

import pytest

from calibre_ai_auditor.extractors.text import extract_snippets
from calibre_ai_auditor.verification.host_registry import (
    HostRegistry,
    HostRegistryConfig,
    default_felix_homelab,
)
from calibre_ai_auditor.verification.restore import RestorePointStore

pytestmark = pytest.mark.benchmark


# Locate the test EPUB shipped with the repo
REPO_ROOT = Path(__file__).resolve().parents[2]
TEST_EPUB = Path(__file__).parent.parent / "fake_library"
# Fall back to any EPUB in the repo
if not any(TEST_EPUB.glob("*.epub")):
    for candidate in [REPO_ROOT]:
        epubs = list(candidate.glob("*.epub"))
        if epubs:
            TEST_EPUB = Path(epubs[0])
            break


def _real_epub_path() -> Path | None:
    if TEST_EPUB.is_file():
        return TEST_EPUB
    if TEST_EPUB.is_dir():
        epubs = list(TEST_EPUB.glob("*.epub"))
        if epubs:
            return epubs[0]
    return None


@pytest.fixture(scope="module")
def epub_path() -> Path | None:
    return _real_epub_path()


# ---------------------------------------------------------------------------
# Host discovery
# ---------------------------------------------------------------------------


@pytest.mark.benchmark(group="host_discovery")
def test_bench_host_discovery_static(benchmark) -> None:
    """Build the default 3-host config (no network)."""

    def _run() -> None:
        HostRegistryConfig(hosts=default_felix_homelab())

    benchmark(_run)


@pytest.mark.network
@pytest.mark.benchmark(group="host_discovery")
def test_bench_host_discovery_live(benchmark) -> None:
    """Live health-check of the 3 configured homelab hosts."""
    import asyncio

    cfg = HostRegistryConfig(hosts=default_felix_homelab())

    async def _run() -> None:
        reg = HostRegistry(cfg)
        try:
            await reg.health_check_all()
        finally:
            await reg.__aexit__(None, None, None)

    benchmark(lambda: asyncio.run(_run()))
    benchmark.extra_info["hosts_checked"] = len(cfg.hosts)


# ---------------------------------------------------------------------------
# Restore points
# ---------------------------------------------------------------------------


@pytest.mark.benchmark(group="restore_point")
def test_bench_restore_point_create_1000(benchmark, tmp_path) -> None:
    """Create 1000 restore points (with hardlinks)."""
    store = RestorePointStore(tmp_path)
    before = {"title": "Old Title", "authors": ["Author"]}
    after = {"title": "New Title", "authors": ["Author"]}

    def _run() -> None:
        for i in range(1000):
            store.create(
                run_id="bench_run",
                book_key=f"calibre:{i}",
                calibre_book_id=i,
                before_metadata=before,
                after_metadata=after,
                fields_changed=["title"],
            )

    benchmark(_run)
    benchmark.extra_info["restore_points_created"] = 1000


@pytest.mark.benchmark(group="restore_point")
def test_bench_restore_point_cleanup_1000(benchmark, tmp_path) -> None:
    """Create 1000 non-expired restore points, then run cleanup (no-op)."""
    store = RestorePointStore(tmp_path, default_ttl=__import__("datetime").timedelta(days=365))
    for i in range(1000):
        store.create(
            run_id="bench_run",
            book_key=f"calibre:{i}",
            calibre_book_id=i,
            before_metadata={"title": "x"},
            after_metadata={"title": "y"},
            fields_changed=["title"],
        )

    def _run() -> None:
        store.cleanup_expired()

    benchmark(_run)
    benchmark.extra_info["restore_points_scanned"] = 1000


@pytest.mark.benchmark(group="restore_point")
def test_bench_restore_point_list_all_1000(benchmark, tmp_path) -> None:
    """List all 1000 restore points (the bulk-undo path)."""
    store = RestorePointStore(tmp_path)
    for i in range(1000):
        store.create(
            run_id="bench_run",
            book_key=f"calibre:{i}",
            calibre_book_id=i,
            before_metadata={"title": "x"},
            after_metadata={"title": "y"},
            fields_changed=["title"],
        )

    def _run() -> None:
        _ = store.list_all()

    benchmark(_run)
    benchmark.extra_info["restore_points_scanned"] = 1000


# ---------------------------------------------------------------------------
# Content extraction
# ---------------------------------------------------------------------------


@pytest.mark.benchmark(group="extraction")
def test_bench_extract_snippets_real_epub(benchmark, epub_path) -> None:
    """Extract first 5 pages of text from the real test EPUB."""
    if epub_path is None:
        pytest.skip("No test EPUB available")
    _ = extract_snippets(epub_path, max_pages=5)  # warmup

    def _run() -> None:
        extract_snippets(epub_path, max_pages=5)

    benchmark(_run)
    benchmark.extra_info["file_size_kb"] = round(epub_path.stat().st_size / 1024, 1)


@pytest.mark.benchmark(group="extraction")
def test_bench_extract_snippets_200x(benchmark, epub_path) -> None:
    """200 sequential extractions (the cache amortization story)."""
    if epub_path is None:
        pytest.skip("No test EPUB available")

    def _run() -> None:
        for _ in range(200):
            extract_snippets(epub_path, max_pages=3)

    benchmark(_run)
    benchmark.extra_info["file_size_kb"] = round(epub_path.stat().st_size / 1024, 1)
    benchmark.extra_info["extractions_per_run"] = 200


# ---------------------------------------------------------------------------
# Resumable run state
# ---------------------------------------------------------------------------


@pytest.mark.benchmark(group="resumable")
def test_bench_resumable_50k_books(benchmark) -> None:
    """Build a ResumableRunStore for 50k books (the worst case scale)."""
    import asyncio

    from calibre_ai_auditor.verification.resumable import ResumableRunStore

    book_keys = [f"calibre:{i}" for i in range(50_000)]

    async def _run() -> None:
        store = ResumableRunStore("run_50k", book_keys)
        # Mark a chunk as various statuses
        for k in book_keys[:1000]:
            await store.mark_completed(k)
        for k in book_keys[1000:2000]:
            await store.mark_failed(k)
        for k in book_keys[2000:3000]:
            await store.mark_in_progress(k)
        # Simulate worker crash + recovery
        store.reset_in_progress()
        progress = store.progress()
        assert progress.total == 50_000

    benchmark(lambda: asyncio.run(_run()))
    benchmark.extra_info["book_keys"] = 50_000

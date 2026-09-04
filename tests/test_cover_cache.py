import tempfile
from pathlib import Path

from calibre_ai_auditor.covers.cache import LocalCoverAuditCache


def test_cover_cache_crud(tmp_path: Path):
    db_file = tmp_path / "test_cache.db"
    cache = LocalCoverAuditCache(db_file)

    assert cache.count() == 0

    # Put a result
    cache.put(
        rel_path="Author/Book/cover.jpg",
        book_id=42,
        file_size=150000,
        mtime_ns=1700000000000000,
        cqs=85,
        tier="Tier A",
        is_spurious=False,
        defect_type=None,
        entropy=5.42,
        penalties=[],
        fatal_defects=[],
    )

    assert cache.count() == 1

    # Cache hit
    hit = cache.get("Author/Book/cover.jpg", file_size=150000, mtime_ns=1700000000000000)
    assert hit is not None
    assert hit["cqs"] == 85
    assert hit["tier"] == "Tier A"
    assert hit["entropy"] == 5.42
    assert hit["is_spurious"] is False

    # Cache miss on changed mtime
    miss_mtime = cache.get("Author/Book/cover.jpg", file_size=150000, mtime_ns=1700000000000001)
    assert miss_mtime is None

    # Cache miss on changed size
    miss_size = cache.get("Author/Book/cover.jpg", file_size=150001, mtime_ns=1700000000000000)
    assert miss_size is None

    # Clear cache
    assert cache.clear() == 1
    assert cache.count() == 0

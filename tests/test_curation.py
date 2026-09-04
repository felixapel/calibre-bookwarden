import sqlite3
from pathlib import Path

from calibre_ai_auditor.curation.duplicates import DuplicateConsolidator
from calibre_ai_auditor.curation.series import SeriesGapHunter


def _setup_curation_mock_db(db_path: Path) -> sqlite3.Connection:
    conn = sqlite3.connect(str(db_path))
    c = conn.cursor()

    c.execute("""
        CREATE TABLE books (
            id INTEGER PRIMARY KEY AUTOINCREMENT,
            title TEXT NOT NULL,
            series_index REAL DEFAULT 1.0
        );
    """)
    c.execute("CREATE TABLE authors (id INTEGER PRIMARY KEY, name TEXT);")
    c.execute("CREATE TABLE books_authors_link (id INTEGER PRIMARY KEY, book INTEGER, author INTEGER);")
    c.execute("CREATE TABLE series (id INTEGER PRIMARY KEY, name TEXT);")
    c.execute("CREATE TABLE books_series_link (id INTEGER PRIMARY KEY, book INTEGER, series INTEGER);")
    c.execute("CREATE TABLE data (id INTEGER PRIMARY KEY, book INTEGER, format TEXT);")
    c.execute("CREATE TABLE identifiers (id INTEGER PRIMARY KEY, book INTEGER, type TEXT, val TEXT);")

    # Author
    c.execute("INSERT INTO authors (id, name) VALUES (1, 'Frank Herbert');")
    c.execute("INSERT INTO authors (id, name) VALUES (2, 'F. A. Hayek');")

    # Series 1: Dune (Volumes 1, 2, 4 -> Missing Volume 3)
    c.execute("INSERT INTO series (id, name) VALUES (1, 'Dune');")

    c.execute("INSERT INTO books (id, title, series_index) VALUES (1, 'Dune', 1.0);")
    c.execute("INSERT INTO books_authors_link (book, author) VALUES (1, 1);")
    c.execute("INSERT INTO books_series_link (book, series) VALUES (1, 1);")

    c.execute("INSERT INTO books (id, title, series_index) VALUES (2, 'Dune Messiah', 2.0);")
    c.execute("INSERT INTO books_authors_link (book, author) VALUES (2, 1);")
    c.execute("INSERT INTO books_series_link (book, series) VALUES (2, 1);")

    c.execute("INSERT INTO books (id, title, series_index) VALUES (4, 'God Emperor of Dune', 4.0);")
    c.execute("INSERT INTO books_authors_link (book, author) VALUES (4, 1);")
    c.execute("INSERT INTO books_series_link (book, series) VALUES (4, 1);")

    # Multi-format duplicates: Book 10 (EPUB) and Book 11 (PDF) of Road to Serfdom
    c.execute("INSERT INTO books (id, title, series_index) VALUES (10, 'The Road to Serfdom', 1.0);")
    c.execute("INSERT INTO books_authors_link (book, author) VALUES (10, 2);")
    c.execute("INSERT INTO data (book, format) VALUES (10, 'EPUB');")

    c.execute("INSERT INTO books (id, title, series_index) VALUES (11, 'Road to Serfdom', 1.0);")
    c.execute("INSERT INTO books_authors_link (book, author) VALUES (11, 2);")
    c.execute("INSERT INTO data (book, format) VALUES (11, 'PDF');")

    # ISBN Collision: Book 20 and 21
    c.execute("INSERT INTO books (id, title, series_index) VALUES (20, 'Unique Book A', 1.0);")
    c.execute("INSERT INTO identifiers (book, type, val) VALUES (20, 'isbn', '9780123456789');")
    c.execute("INSERT INTO data (book, format) VALUES (20, 'EPUB');")

    c.execute("INSERT INTO books (id, title, series_index) VALUES (21, 'Unique Book B', 1.0);")
    c.execute("INSERT INTO identifiers (book, type, val) VALUES (21, 'isbn', '9780123456789');")
    c.execute("INSERT INTO data (book, format) VALUES (21, 'MOBI');")

    conn.commit()
    return conn


def test_series_gap_hunter(tmp_path: Path):
    db_path = tmp_path / "metadata.db"
    conn = _setup_curation_mock_db(db_path)

    hunter = SeriesGapHunter(conn)
    gaps = hunter.find_all_gaps()

    assert len(gaps) == 1
    dune_gap = gaps[0]
    assert dune_gap.series_name == "Dune"
    assert dune_gap.missing_indices == [3]  # Volume 3 is missing!
    assert dune_gap.total_owned == 3

    wishlist = hunter.export_wishlist(gaps)
    assert len(wishlist) == 1
    assert wishlist[0]["volume"] == 3
    assert wishlist[0]["series"] == "Dune"


def test_duplicate_consolidator(tmp_path: Path):
    db_path = tmp_path / "metadata.db"
    conn = _setup_curation_mock_db(db_path)

    consolidator = DuplicateConsolidator(conn)
    clusters = consolidator.find_multi_format_duplicates()

    assert len(clusters) == 2

    # Find the title/author multi-format duplicate
    mfmt = next(c for c in clusters if c.cluster_type == "multi_format_duplicate")
    assert mfmt.primary_book_id == 10
    assert mfmt.duplicate_book_ids == [11]
    assert mfmt.recommendation == "MERGE_FORMATS"
    assert "EPUB" in mfmt.formats_by_book[10]
    assert "PDF" in mfmt.formats_by_book[11]

    # Find ISBN collision
    isbn_cluster = next(c for c in clusters if c.cluster_type == "isbn_collision")
    assert isbn_cluster.primary_book_id == 20
    assert isbn_cluster.duplicate_book_ids == [21]
    assert isbn_cluster.recommendation == "MERGE_FORMATS"


def test_isbn_validation_and_technical_titles():
    from calibre_ai_auditor.curation.duplicates import (
        _normalize_str,
        is_valid_isbn,
        is_valid_isbn10,
        is_valid_isbn13,
    )

    # 1. Technical symbols preservation
    assert _normalize_str("C++ Primer") == "c++ primer"
    assert _normalize_str("C# in Depth") == "c# in depth"
    assert _normalize_str("C++ Primer") != _normalize_str("C# in Depth")

    # 2. ISBN-10 Checksum verification
    assert is_valid_isbn10("0471958697") is True
    assert is_valid_isbn10("0471958698") is False

    # 3. ISBN-13 Checksum verification
    assert is_valid_isbn13("9780306406157") is True
    assert is_valid_isbn13("9780306406158") is False

    # 4. Dummy sequence and invalid character placement rejection
    assert is_valid_isbn("0000000000") is False
    assert is_valid_isbn("9999999999999") is False
    assert is_valid_isbn("1234567890") is False  # Dummy ascending ladder
    assert is_valid_isbn("9876543210") is False  # Dummy descending ladder
    assert is_valid_isbn("123X567890") is False  # X in middle


def test_series_gap_hunter_safety_span(tmp_path: Path):
    import sqlite3

    db_path = tmp_path / "corrupt_series.db"
    conn = sqlite3.connect(str(db_path))
    c = conn.cursor()
    c.execute("CREATE TABLE series (id INTEGER PRIMARY KEY, name TEXT);")
    c.execute("CREATE TABLE books (id INTEGER PRIMARY KEY, title TEXT, series_index REAL);")
    c.execute("CREATE TABLE books_series_link (id INTEGER PRIMARY KEY, book INTEGER, series INTEGER);")
    c.execute("CREATE TABLE authors (id INTEGER PRIMARY KEY, name TEXT, sort TEXT);")
    c.execute("CREATE TABLE books_authors_link (id INTEGER PRIMARY KEY, book INTEGER, author INTEGER);")

    # Series with crazy index 99999
    c.execute("INSERT INTO series (id, name) VALUES (1, 'Corrupted Index Series');")
    c.execute("INSERT INTO books (id, title, series_index) VALUES (1, 'Vol 1', 1.0);")
    c.execute("INSERT INTO books (id, title, series_index) VALUES (2, 'Vol 99999', 99999.0);")
    c.execute("INSERT INTO books_series_link (book, series) VALUES (1, 1), (2, 1);")
    conn.commit()

    hunter = SeriesGapHunter(conn)
    gaps = hunter.find_all_gaps()
    # Should be skipped safely without hanging or allocating huge range
    assert len(gaps) == 0
    conn.close()


def test_series_gap_hunter_supports_long_series_over_500(tmp_path: Path):
    import sqlite3

    db_path = tmp_path / "long_series.db"
    conn = sqlite3.connect(str(db_path))
    c = conn.cursor()
    c.execute("CREATE TABLE series (id INTEGER PRIMARY KEY, name TEXT);")
    c.execute("CREATE TABLE books (id INTEGER PRIMARY KEY, title TEXT, series_index REAL);")
    c.execute("CREATE TABLE books_series_link (id INTEGER PRIMARY KEY, book INTEGER, series INTEGER);")
    c.execute("CREATE TABLE authors (id INTEGER PRIMARY KEY, name TEXT, sort TEXT);")
    c.execute("CREATE TABLE books_authors_link (id INTEGER PRIMARY KEY, book INTEGER, author INTEGER);")

    # Series like One Piece with volumes around 600
    c.execute("INSERT INTO series (id, name) VALUES (1, 'One Piece');")
    c.execute("INSERT INTO books (id, title, series_index) VALUES (1, 'Vol 600', 600.0);")
    c.execute("INSERT INTO books (id, title, series_index) VALUES (2, 'Vol 601', 601.0);")
    c.execute("INSERT INTO books (id, title, series_index) VALUES (3, 'Vol 603', 603.0);")
    c.execute("INSERT INTO books_series_link (book, series) VALUES (1, 1), (2, 1), (3, 1);")
    conn.commit()

    hunter = SeriesGapHunter(conn)
    gaps = hunter.find_all_gaps()
    assert len(gaps) == 1
    assert gaps[0].series_name == "One Piece"
    assert 602 in gaps[0].missing_indices
    conn.close()

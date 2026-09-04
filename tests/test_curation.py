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

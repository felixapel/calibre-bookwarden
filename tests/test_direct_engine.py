import os
import sqlite3
from pathlib import Path
from unittest.mock import MagicMock, patch

import pytest
from PIL import Image

import calibre_ai_auditor.calibre.direct_engine as direct_engine_module
from calibre_ai_auditor.calibre.direct_engine import (
    DirectCalibreEngine,
    calibre_author_sort,
    calibre_title_sort,
)


def _setup_mock_calibre_library(tmp_path: Path) -> Path:
    lib_dir = tmp_path / "Calibre Library"
    lib_dir.mkdir(parents=True, exist_ok=True)
    db_path = lib_dir / "metadata.db"

    conn = sqlite3.connect(str(db_path))
    c = conn.cursor()

    # Create Calibre schema tables
    c.execute("""
        CREATE TABLE books (
            id INTEGER PRIMARY KEY AUTOINCREMENT,
            title TEXT NOT NULL,
            sort TEXT,
            author_sort TEXT,
            timestamp TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
            pubdate TIMESTAMP,
            series_index REAL DEFAULT 1.0,
            modify_date TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
            path TEXT NOT NULL DEFAULT '',
            has_cover BOOL DEFAULT 0,
            uuid TEXT
        )
    """)
    c.execute("""
        CREATE TABLE authors (
            id INTEGER PRIMARY KEY AUTOINCREMENT,
            name TEXT NOT NULL,
            sort TEXT,
            link TEXT DEFAULT ''
        )
    """)
    c.execute("""
        CREATE TABLE books_authors_link (
            id INTEGER PRIMARY KEY AUTOINCREMENT,
            book INTEGER NOT NULL,
            author INTEGER NOT NULL
        )
    """)
    c.execute("""
        CREATE TABLE ratings (
            id INTEGER PRIMARY KEY AUTOINCREMENT,
            rating INTEGER NOT NULL
        )
    """)
    c.execute("""
        CREATE TABLE books_ratings_link (
            id INTEGER PRIMARY KEY AUTOINCREMENT,
            book INTEGER NOT NULL,
            rating INTEGER NOT NULL
        )
    """)
    c.execute("""
        CREATE TABLE tags (
            id INTEGER PRIMARY KEY AUTOINCREMENT,
            name TEXT NOT NULL
        )
    """)
    c.execute("""
        CREATE TABLE books_tags_link (
            id INTEGER PRIMARY KEY AUTOINCREMENT,
            book INTEGER NOT NULL,
            tag INTEGER NOT NULL
        )
    """)
    c.execute("""
        CREATE TABLE series (
            id INTEGER PRIMARY KEY AUTOINCREMENT,
            name TEXT NOT NULL,
            sort TEXT
        )
    """)
    c.execute("""
        CREATE TABLE books_series_link (
            id INTEGER PRIMARY KEY AUTOINCREMENT,
            book INTEGER NOT NULL,
            series INTEGER NOT NULL
        )
    """)
    c.execute("""
        CREATE TABLE publishers (
            id INTEGER PRIMARY KEY AUTOINCREMENT,
            name TEXT NOT NULL,
            sort TEXT
        )
    """)
    c.execute("""
        CREATE TABLE books_publishers_link (
            id INTEGER PRIMARY KEY AUTOINCREMENT,
            book INTEGER NOT NULL,
            publisher INTEGER NOT NULL
        )
    """)
    c.execute("""
        CREATE TABLE data (
            id INTEGER PRIMARY KEY AUTOINCREMENT,
            book INTEGER NOT NULL,
            format TEXT NOT NULL,
            uncompressed_size INTEGER NOT NULL,
            name TEXT NOT NULL
        )
    """)
    c.execute("""
        CREATE TABLE comments (
            id INTEGER PRIMARY KEY AUTOINCREMENT,
            book INTEGER NOT NULL,
            text TEXT NOT NULL
        )
    """)
    c.execute("""
        CREATE TABLE identifiers (
            id INTEGER PRIMARY KEY AUTOINCREMENT,
            book INTEGER NOT NULL,
            type TEXT NOT NULL,
            val TEXT NOT NULL
        )
    """)
    c.execute("""
        CREATE TABLE books_custom_column_1_link (
            id INTEGER PRIMARY KEY AUTOINCREMENT,
            book INTEGER NOT NULL,
            value INTEGER NOT NULL
        )
    """)

    # Populate test records
    # Book 1: Normal book with valid file and cover
    c.execute(
        """INSERT INTO books (id, title, sort, author_sort, path, has_cover)
           VALUES (1, 'The Hobbit', 'Hobbit, The', 'Tolkien, J. R. R.', 'J. R. R. Tolkien/The Hobbit (1)', 1)"""
    )
    c.execute("INSERT INTO authors (id, name, sort) VALUES (1, 'J. R. R. Tolkien', 'Tolkien, J. R. R.')")
    c.execute("INSERT INTO books_authors_link (book, author) VALUES (1, 1)")
    c.execute("INSERT INTO ratings (id, rating) VALUES (1, 10)")
    c.execute("INSERT INTO books_ratings_link (book, rating) VALUES (1, 1)")
    c.execute(
        """INSERT INTO data (id, book, format, uncompressed_size, name)
           VALUES (1, 1, 'EPUB', 500000, 'The Hobbit - J. R. R. Tolkien')"""
    )

    # Create directory and files for Book 1
    b1_dir = lib_dir / "J. R. R. Tolkien/The Hobbit (1)"
    b1_dir.mkdir(parents=True, exist_ok=True)
    (b1_dir / "The Hobbit - J. R. R. Tolkien.epub").write_bytes(b"dummy epub data")
    im = Image.new("RGB", (400, 600), color="blue")
    im.save(b1_dir / "cover.jpg", "JPEG")

    # Book 2: Desynced author sort + junk title
    c.execute(
        """INSERT INTO books (id, title, sort, author_sort, path, has_cover)
           VALUES (2, 'Human Action [WeLib.org]', 'Human Action',
                   'Ludwig von Mises', 'Ludwig von Mises/Human Action (2)', 1)"""
    )
    c.execute("INSERT INTO authors (id, name, sort) VALUES (2, 'Ludwig von Mises', 'von Mises, Ludwig')")
    c.execute("INSERT INTO books_authors_link (book, author) VALUES (2, 2)")
    c.execute(
        """INSERT INTO data (id, book, format, uncompressed_size, name)
           VALUES (2, 2, 'EPUB', 800000, 'Human Action - Ludwig von Mises')"""
    )
    b2_dir = lib_dir / "Ludwig von Mises/Human Action (2)"
    b2_dir.mkdir(parents=True, exist_ok=True)
    (b2_dir / "Human Action - Ludwig von Mises.epub").write_bytes(b"dummy epub data 2")
    im2 = Image.new("RGB", (300, 450), color="darkred")
    im2.save(b2_dir / "cover.jpg", "JPEG")

    # Book 3: Empty format stub (0 format files)
    c.execute(
        """INSERT INTO books (id, title, sort, author_sort, path, has_cover)
           VALUES (3, 'Ghost Book', 'Ghost Book', 'Unknown', 'Ghost/Book (3)', 0)"""
    )
    c.execute("INSERT INTO authors (id, name, sort) VALUES (3, 'Unknown', 'Unknown')")
    c.execute("INSERT INTO books_authors_link (book, author) VALUES (3, 3)")
    c.execute("INSERT INTO books_custom_column_1_link (book, value) VALUES (3, 42)")

    # Orphan row in books_ratings_link (book 999 does not exist)
    c.execute("INSERT INTO books_ratings_link (book, rating) VALUES (999, 1)")

    conn.commit()
    conn.close()
    return lib_dir


def test_calibre_sorting_algorithms():
    assert calibre_title_sort("The Lord of the Rings") == "Lord of the Rings, The"
    assert calibre_title_sort("El Quijote") == "Quijote, El"
    assert calibre_title_sort("Der Prozeß") == "Prozeß, Der"
    assert calibre_title_sort("Meditations") == "Meditations"

    assert calibre_author_sort("Geoffrey Parker") == "Parker, Geoffrey"
    assert calibre_author_sort("Ludwig von Mises") == "von Mises, Ludwig"
    assert calibre_author_sort("Saint Thomas Aquinas") == "Thomas Aquinas, Saint"
    assert calibre_author_sort("Pope Benedict XVI") == "Benedict XVI, Pope"
    assert calibre_author_sort("The Economist") == "Economist, The"


def test_direct_engine_audit(tmp_path: Path):
    lib_dir = _setup_mock_calibre_library(tmp_path)
    engine = DirectCalibreEngine(lib_dir)

    report = engine.audit_library()
    assert report["sqlite_integrity"] == "ok"
    assert report["total_books"] == 3
    assert report["unrated_books"] == 2  # Books 2 and 3 are unrated
    assert report["author_desyncs_count"] >= 1  # Book 2 author_sort is desynced
    assert report["empty_format_records_count"] == 1  # Book 3 is empty
    assert report["bad_titles_count"] == 1  # Book 2 has [WeLib.org]
    assert report["foreign_key_issues"] == 1  # Orphan book 999


def test_direct_engine_snapshot_uses_sqlite_backup_and_includes_wal_changes(tmp_path: Path):
    lib_dir = _setup_mock_calibre_library(tmp_path)
    engine = DirectCalibreEngine(lib_dir)
    backup_dir = tmp_path / "metadata snapshots # 100%"

    # Keep the source writer open so the committed table is still represented
    # by WAL pages when the snapshot is requested.
    writer = sqlite3.connect(lib_dir / "metadata.db")
    writer.execute("PRAGMA journal_mode=WAL")
    writer.execute("PRAGMA wal_autocheckpoint=0")
    writer.execute("CREATE TABLE snapshot_probe (value TEXT)")
    writer.execute("INSERT INTO snapshot_probe VALUES ('committed-in-wal')")
    writer.commit()

    snap = engine.create_snapshot(backup_dir)
    writer.close()

    assert snap.exists()
    assert snap.parent == backup_dir
    assert snap.name.startswith("metadata.db.snapshot_")
    assert snap.suffix == ".sqlite"
    assert not snap.is_relative_to(lib_dir)

    snapshot = sqlite3.connect(snap)
    assert snapshot.execute("SELECT value FROM snapshot_probe").fetchone()[0] == "committed-in-wal"
    assert snapshot.execute("PRAGMA integrity_check").fetchone()[0] == "ok"
    snapshot.close()


def test_direct_engine_snapshot_rejects_library_target_and_cleans_incomplete_backup(tmp_path: Path):
    lib_dir = _setup_mock_calibre_library(tmp_path)
    engine = DirectCalibreEngine(lib_dir)
    backup_dir = tmp_path / "metadata-snapshots"

    with pytest.raises(ValueError, match="outside"):
        engine.create_snapshot(lib_dir)

    failing_source = MagicMock()
    failing_source.backup.side_effect = sqlite3.Error("backup failed")
    with (
        patch.object(engine, "get_connection", return_value=failing_source),
        pytest.raises(sqlite3.Error, match="backup failed"),
    ):
        engine.create_snapshot(backup_dir)

    assert not list(backup_dir.glob("*.sqlite"))
    failing_source.close.assert_called_once()


def test_direct_engine_rejects_direct_writes(tmp_path: Path):
    lib_dir = _setup_mock_calibre_library(tmp_path)
    engine = DirectCalibreEngine(lib_dir)

    connection = engine.get_connection()
    connection.execute("PRAGMA query_only = OFF")
    with pytest.raises(sqlite3.OperationalError):
        connection.execute("UPDATE books SET title = 'must not persist' WHERE id = 1")
    connection.close()
    with pytest.raises(PermissionError, match="supervised writer"):
        engine.get_connection(read_only=False)

    for method, args in (
        (engine.sync_all_author_sorts, ()),
        (engine.purge_orphan_foreign_keys, ()),
        (engine.delete_empty_format_records, ()),
    ):
        with pytest.raises(PermissionError, match="supervised writer"):
            method(*args)

    report = engine.audit_library()
    assert report["total_books"] == 3
    assert report["foreign_key_issues"] == 1


def test_direct_engine_unc_connection_is_uri_read_only_and_never_falls_back(tmp_path: Path):
    engine = DirectCalibreEngine(_setup_mock_calibre_library(tmp_path))
    engine.db_path = Path(r"\\server\share\metadata.db")

    with (
        patch(
            "calibre_ai_auditor.calibre.direct_engine.sqlite3.connect",
            side_effect=sqlite3.OperationalError("URI authority unsupported"),
        ) as connect,
        pytest.raises(sqlite3.OperationalError, match="authority unsupported"),
    ):
        engine.get_connection()

    assert connect.call_count == 1
    uri = connect.call_args.args[0]
    assert uri.startswith("file://server/share/")
    assert uri.endswith("metadata.db?mode=ro")
    assert connect.call_args.kwargs["uri"] is True


def test_direct_engine_audit_rejects_malicious_stored_book_path(tmp_path: Path):
    lib_dir = _setup_mock_calibre_library(tmp_path)
    outside = tmp_path / "outside"
    outside.mkdir()
    (outside / "cover.jpg").write_bytes(b"not a library cover")

    writer = sqlite3.connect(lib_dir / "metadata.db")
    writer.execute("UPDATE books SET path = '../outside' WHERE id = 1")
    writer.commit()
    writer.close()

    report = DirectCalibreEngine(lib_dir).audit_library()

    expected_reason = "UNSUPPORTED_SECURE_FILE_READ" if os.name == "nt" else "UNSAFE_BOOK_PATH"
    assert {entry["reason"] for entry in report["missing_data_files"]} >= {expected_reason}
    assert any(entry["error"] == expected_reason for entry in report["broken_covers"])


def test_direct_engine_windows_audit_fails_closed_before_opening_stored_files(tmp_path: Path):
    lib_dir = _setup_mock_calibre_library(tmp_path)
    book = {"id": 1, "title": "Unsafe on Windows", "path": "Author/Tiny One"}
    data_files = [{"name": "Book", "format": "EPUB"}]

    with (
        patch("calibre_ai_auditor.calibre.direct_engine.os.name", "nt"),
        patch("calibre_ai_auditor.calibre.direct_engine.open_file_beneath") as open_beneath,
    ):
        result = direct_engine_module._inspect_single_book(book, lib_dir, data_files, 30_000_000, [])

    open_beneath.assert_not_called()
    assert result["missing_data"][0]["reason"] == "UNSUPPORTED_SECURE_FILE_READ"
    assert result["broken_cover"]["error"] == "UNSUPPORTED_SECURE_FILE_READ"

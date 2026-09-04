import sqlite3
from pathlib import Path
from unittest.mock import patch

from fastapi.testclient import TestClient
from PIL import Image

from calibre_ai_auditor.config.settings import Settings
from calibre_ai_auditor.web.app import app


def _setup_test_library(tmp_path: Path) -> tuple[Path, Path]:
    lib_dir = tmp_path / "Calibre Library"
    lib_dir.mkdir(parents=True, exist_ok=True)
    db_path = lib_dir / "metadata.db"

    conn = sqlite3.connect(str(db_path))
    c = conn.cursor()

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
        CREATE TABLE data (
            id INTEGER PRIMARY KEY AUTOINCREMENT,
            book INTEGER NOT NULL,
            format TEXT NOT NULL,
            uncompressed_size INTEGER NOT NULL,
            name TEXT NOT NULL
        )
    """)

    # Insert a book
    c.execute("INSERT INTO authors (name, sort) VALUES ('Ludwig von Mises', 'von Mises, Ludwig')")
    c.execute(
        "INSERT INTO books (title, author_sort, path, has_cover) "
        "VALUES ('Human Action', 'Mises, Ludwig', 'Ludwig von Mises/Human Action (1)', 1)"
    )
    c.execute("INSERT INTO books_authors_link (book, author) VALUES (1, 1)")
    c.execute("INSERT INTO data (book, format, uncompressed_size, name) VALUES (1, 'EPUB', 1024, 'Human Action')")

    conn.commit()
    conn.close()

    # Create dummy cover image
    book_folder = lib_dir / "Ludwig von Mises/Human Action (1)"
    book_folder.mkdir(parents=True, exist_ok=True)
    cover_file = book_folder / "cover.jpg"
    img = Image.new("RGB", (600, 900), color="navy")
    img.save(cover_file, format="JPEG")

    # Create dummy ebook file
    epub_file = book_folder / "Human Action.epub"
    epub_file.write_bytes(b"dummy epub")

    return lib_dir, cover_file


def test_audit_360_api(tmp_path: Path):
    lib_dir, _ = _setup_test_library(tmp_path)
    test_settings = Settings()
    test_settings.library.path = lib_dir
    test_settings.profile = "development"

    with patch("calibre_ai_auditor.web.api.audit_360.load_settings", return_value=test_settings):
        client = TestClient(app)
        response = client.get("/api/audit/360")
        assert response.status_code == 200
        data = response.json()
        assert data["status"] == "success"
        assert data["data"]["total_books"] == 1
        assert data["data"]["sqlite_integrity"] == "ok"


def test_sync_author_sorts_api(tmp_path: Path):
    lib_dir, _ = _setup_test_library(tmp_path)
    test_settings = Settings()
    test_settings.library.path = lib_dir
    test_settings.profile = "development"
    test_settings.library.read_only = False

    with patch("calibre_ai_auditor.web.api.audit_360.load_settings", return_value=test_settings):
        client = TestClient(app)
        response = client.post("/api/audit/sync-author-sorts")
        assert response.status_code == 200
        data = response.json()
        assert data["status"] == "success"
        assert "updated_count" in data["data"]


def test_covers_score_api(tmp_path: Path):
    _, cover_file = _setup_test_library(tmp_path)
    client = TestClient(app)

    response = client.post("/api/covers/score", json={"cover_path": str(cover_file)})
    assert response.status_code == 200
    data = response.json()
    assert data["status"] == "success"
    assert "cqs" in data["data"]
    assert "score" in data["data"]["cqs"]
    assert "tier" in data["data"]["cqs"]
    assert "spurious" in data["data"]


def test_covers_deck_api(tmp_path: Path):
    lib_dir, _ = _setup_test_library(tmp_path)
    test_settings = Settings()
    test_settings.library.path = lib_dir
    test_settings.profile = "development"

    with patch("calibre_ai_auditor.web.api.covers_api.load_settings", return_value=test_settings):
        client = TestClient(app)
        response = client.get("/api/covers/deck?limit=10")
        assert response.status_code == 200
        data = response.json()
        assert data["status"] == "success"
        assert "deck" in data["data"]

import sqlite3
from pathlib import Path
from unittest.mock import patch

from fastapi.testclient import TestClient
from PIL import Image

from calibre_ai_auditor.config.settings import Settings
from calibre_ai_auditor.web.app import app


def _setup_deck_test_library(tmp_path: Path) -> Path:
    lib_dir = tmp_path / "Calibre Library"
    lib_dir.mkdir(parents=True, exist_ok=True)
    db_path = lib_dir / "metadata.db"

    conn = sqlite3.connect(str(db_path))
    c = conn.cursor()
    c.execute("""
        CREATE TABLE books (
            id INTEGER PRIMARY KEY,
            title TEXT,
            author_sort TEXT,
            path TEXT,
            has_cover BOOL
        );
    """)
    c.execute("CREATE TABLE authors (id INTEGER PRIMARY KEY, name TEXT, sort TEXT);")
    c.execute("CREATE TABLE books_authors_link (id INTEGER PRIMARY KEY, book INTEGER, author INTEGER);")
    c.execute("CREATE TABLE ratings (id INTEGER PRIMARY KEY, rating INTEGER);")
    c.execute("CREATE TABLE books_ratings_link (id INTEGER PRIMARY KEY, book INTEGER, rating INTEGER);")
    c.execute("CREATE TABLE data (id INTEGER PRIMARY KEY, book INTEGER, format TEXT, name TEXT, uncompressed_size INTEGER);")

    # Low quality cover book (tiny)
    c.execute("INSERT INTO books (id, title, path, has_cover) VALUES (1, 'Tiny Book', 'Author/Tiny (1)', 1);")
    c.execute("INSERT INTO authors (id, name, sort) VALUES (1, 'Test Author', 'Author, Test');")
    c.execute("INSERT INTO books_authors_link (book, author) VALUES (1, 1);")

    b_dir = lib_dir / "Author/Tiny (1)"
    b_dir.mkdir(parents=True, exist_ok=True)
    im = Image.new("RGB", (80, 120), color="gray")
    im.save(b_dir / "cover.jpg", "JPEG")

    conn.commit()
    conn.close()
    return lib_dir


def test_cover_deck_ui_and_image(tmp_path: Path):
    lib_dir = _setup_deck_test_library(tmp_path)
    test_settings = Settings()
    test_settings.library.path = lib_dir

    with patch("calibre_ai_auditor.web.api.covers_api.load_settings", return_value=test_settings):
        client = TestClient(app)

        # 1. Test image serving endpoint
        img_resp = client.get("/api/covers/book/1/image")
        assert img_resp.status_code == 200
        assert img_resp.headers["content-type"] == "image/jpeg"
        assert len(img_resp.content) > 0

        # 2. Test Deck UI rendering
        ui_resp = client.get("/api/covers/ui/deck")
        assert ui_resp.status_code == 200
        assert "text/html" in ui_resp.headers["content-type"]
        assert "Tiny Book" in ui_resp.text
        assert "Tier D" in ui_resp.text
        assert "Keyboard Shortcuts" in ui_resp.text

        # 3. Test Deck action (skip)
        action_resp = client.post("/api/covers/ui/deck/action?action=skip&book_id=1")
        assert action_resp.status_code == 200
        assert "text/html" in action_resp.headers["content-type"]
        assert "Cover Deck Clean!" in action_resp.text

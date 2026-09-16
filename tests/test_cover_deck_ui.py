import hashlib
import os
import sqlite3
import tempfile
from pathlib import Path
from unittest.mock import patch

from fastapi.testclient import TestClient
from PIL import Image

from calibre_ai_auditor.config.settings import Settings
from calibre_ai_auditor.security.files import SecurePathError
from calibre_ai_auditor.web.api import covers_api
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
    c.execute(
        "CREATE TABLE data (id INTEGER PRIMARY KEY, book INTEGER, format TEXT, name TEXT, uncompressed_size INTEGER);"
    )
    c.execute("INSERT INTO authors (id, name, sort) VALUES (1, 'Test Author', 'Author, Test');")

    for book_id, title, path in (
        (1, "Tiny Book One", "Author/Tiny One (1)"),
        (2, "Missing Cover Book", "Author/Missing (2)"),
        (3, "Tiny Book Three", "Author/Tiny Three (3)"),
    ):
        c.execute("INSERT INTO books (id, title, path, has_cover) VALUES (?, ?, ?, 1);", (book_id, title, path))
        c.execute("INSERT INTO books_authors_link (book, author) VALUES (?, 1);", (book_id,))

    for relative in ("Author/Tiny One (1)", "Author/Tiny Three (3)"):
        book_dir = lib_dir / relative
        book_dir.mkdir(parents=True, exist_ok=True)
        Image.new("RGB", (80, 120), color="gray").save(book_dir / "cover.jpg", "JPEG")

    conn.commit()
    conn.close()
    return lib_dir


def _settings(library: Path, *, read_only: bool = True) -> Settings:
    settings = Settings()
    settings.library.path = library
    settings.library.read_only = read_only
    return settings


def test_cover_deck_cursor_skip_is_stable_and_read_only(tmp_path: Path):
    lib_dir = _setup_deck_test_library(tmp_path)
    settings = _settings(lib_dir)

    with patch("calibre_ai_auditor.web.api.covers_api.load_settings", return_value=settings):
        client = TestClient(app)
        first = client.get("/api/covers/ui/deck?after_book_id=0")
        refresh = client.get("/api/covers/ui/deck?after_book_id=0")

        assert first.status_code == 200
        assert "Tiny Book One" in first.text
        assert refresh.text == first.text
        assert "Review only." in first.text
        assert "<script" not in first.text
        assert "onclick=" not in first.text
        assert "http://" not in first.text
        assert "https://" not in first.text
        assert "candidate" not in first.text.lower()

        skipped = client.post(
            "/api/covers/ui/deck/action?action=skip&book_id=1&after_book_id=0", follow_redirects=False
        )
        assert skipped.status_code == 303
        assert skipped.headers["location"] == "/api/covers/ui/deck?after_book_id=1"

        second = client.get(skipped.headers["location"])
        assert "Missing Cover Book" in second.text
        assert "No current cover image is safely available" in second.text
        assert (
            client.post(
                "/api/covers/ui/deck/action?action=skip&book_id=1&after_book_id=1", follow_redirects=False
            ).status_code
            == 400
        )

        third_location = client.post(
            "/api/covers/ui/deck/action?action=skip&book_id=2&after_book_id=1", follow_redirects=False
        ).headers["location"]
        third = client.get(third_location)
        assert "Tiny Book Three" in third.text


def test_cover_deck_apply_rejects_without_changing_current_cover(tmp_path: Path):
    lib_dir = _setup_deck_test_library(tmp_path)
    cover_path = lib_dir / "Author/Tiny One (1)/cover.jpg"
    before = hashlib.sha256(cover_path.read_bytes()).hexdigest()

    for read_only, expected_status in ((True, 403), (False, 409)):
        settings = _settings(lib_dir, read_only=read_only)
        with patch("calibre_ai_auditor.web.api.covers_api.load_settings", return_value=settings):
            response = TestClient(app).post(
                "/api/covers/ui/deck/action?action=apply&book_id=1&candidate=999&after_book_id=0",
                follow_redirects=False,
            )
        assert response.status_code == expected_status

    assert hashlib.sha256(cover_path.read_bytes()).hexdigest() == before


def test_cover_deck_rejects_malformed_cursor_and_unsafe_stored_path(tmp_path: Path):
    lib_dir = _setup_deck_test_library(tmp_path)
    outside = tmp_path / "outside"
    outside.mkdir()
    Image.new("RGB", (500, 750), color="red").save(outside / "cover.jpg", "JPEG")
    conn = sqlite3.connect(lib_dir / "metadata.db")
    conn.execute("UPDATE books SET path = '../outside' WHERE id = 3")
    conn.commit()
    conn.close()
    settings = _settings(lib_dir)

    with patch("calibre_ai_auditor.web.api.covers_api.load_settings", return_value=settings):
        client = TestClient(app)
        assert client.get("/api/covers/ui/deck?after_book_id=-1").status_code == 422
        assert client.get("/api/covers/ui/deck?after_book_id=not-an-integer").status_code == 422
        response = client.get("/api/covers/deck?after_book_id=2&limit=1")

    assert response.status_code == 200
    item = response.json()["data"]["deck"][0]
    assert item["book_id"] == 3
    assert item["has_current_image"] is False
    assert "outside" not in response.text


def test_cover_deck_rejects_a_mocked_path_swap_before_inspection(tmp_path: Path):
    lib_dir = _setup_deck_test_library(tmp_path)
    with (
        patch("calibre_ai_auditor.web.api.covers_api.os.name", "posix"),
        patch(
            "calibre_ai_auditor.web.api.covers_api.read_file_beneath",
            side_effect=SecurePathError("unsafe or symlinked parent during path swap"),
        ) as secure_read,
    ):
        assert covers_api._read_book_cover_bytes(lib_dir, "Author/Tiny One (1)") is None

    secure_read.assert_called_once()


def test_cover_deck_windows_read_is_explicitly_unavailable(tmp_path: Path):
    lib_dir = _setup_deck_test_library(tmp_path)
    settings = _settings(lib_dir)
    cover_bytes = (lib_dir / "Author/Tiny One (1)/cover.jpg").read_bytes()

    with (
        patch("calibre_ai_auditor.web.api.covers_api.load_settings", return_value=settings),
        patch("calibre_ai_auditor.web.api.covers_api._read_book_cover_bytes", return_value=cover_bytes),
    ):
        rendered = TestClient(app).get("/api/covers/ui/deck?after_book_id=0")
    assert rendered.status_code == 200
    assert "/api/covers/book/1/image" in rendered.text

    if os.name == "nt":
        with patch("calibre_ai_auditor.web.api.covers_api.load_settings", return_value=settings):
            assert TestClient(app).get("/api/covers/book/1/image").status_code == 404


def test_cover_deck_malformed_cover_redacts_private_inspection_paths(tmp_path: Path):
    malformed_bytes = b"not an image"
    private_temp_dir = tmp_path / "private-inspection-temp"
    private_temp_dir.mkdir()
    original_named_temporary_file = tempfile.NamedTemporaryFile

    with patch(
        "calibre_ai_auditor.web.api.covers_api.tempfile.NamedTemporaryFile",
        side_effect=lambda **kwargs: original_named_temporary_file(dir=private_temp_dir, **kwargs),
    ):
        score, _ = covers_api._inspect_cover_bytes(malformed_bytes)

    assert score.penalties == ["cover_decode_failed"]
    assert not list(private_temp_dir.iterdir())

    lib_dir = _setup_deck_test_library(tmp_path)
    conn = sqlite3.connect(lib_dir / "metadata.db")
    conn.execute("UPDATE books SET path = 'private-source-path' WHERE id = 3")
    conn.commit()
    conn.close()
    settings = _settings(lib_dir)

    with (
        patch("calibre_ai_auditor.web.api.covers_api.load_settings", return_value=settings),
        patch("calibre_ai_auditor.web.api.covers_api._read_book_cover_bytes", return_value=malformed_bytes),
    ):
        client = TestClient(app)
        json_response = client.get("/api/covers/deck?after_book_id=2&limit=1")
        html_response = client.get("/api/covers/ui/deck?after_book_id=2")

    assert json_response.status_code == 200
    assert json_response.json()["data"]["deck"][0]["penalties"] == ["cover_decode_failed"]
    assert "read_error:" not in json_response.text
    assert "private-source-path" not in json_response.text
    assert "private-source-path" not in html_response.text

from collections.abc import Generator
from typing import Any
from unittest.mock import AsyncMock, MagicMock, patch

import pytest
from fastapi import HTTPException
from fastapi.testclient import TestClient
from sqlmodel import Session, SQLModel, create_engine, select

from calibre_ai_auditor.config.settings import Settings
from calibre_ai_auditor.storage.models import BookRecord
from calibre_ai_auditor.web.api.apply import get_session as apply_get_session
from calibre_ai_auditor.web.api.books import get_session as books_get_session
from calibre_ai_auditor.web.api.config import save_config
from calibre_ai_auditor.web.api.runs import get_session as runs_get_session
from calibre_ai_auditor.web.app import app

client = TestClient(app)


@pytest.fixture(name="test_db")
def test_db_fixture(tmp_path: Any) -> Generator[Any, None, None]:
    db_file = tmp_path / "test_api_temp.db"
    test_engine = create_engine(f"sqlite:///{db_file}", connect_args={"check_same_thread": False})
    SQLModel.metadata.create_all(test_engine)

    def override_get_session() -> Generator[Session, None, None]:
        with Session(test_engine) as session:
            yield session

    app.dependency_overrides[apply_get_session] = override_get_session
    app.dependency_overrides[books_get_session] = override_get_session
    app.dependency_overrides[runs_get_session] = override_get_session

    # Populate dummy book
    with Session(test_engine) as session:
        book = BookRecord(
            book_key="calibre:1",
            run_id="test_run",
            calibre_book_id=1,
            source="calibre",
            current_metadata={"title": "Test Title"},
            files=[],
            status="scanned",
        )
        session.add(book)
        session.commit()

    yield test_engine

    # Clean up overrides
    app.dependency_overrides.clear()


def test_health_check(test_db: Any) -> None:
    response = client.get("/api/health")
    assert response.status_code == 200
    assert response.json() == {"status": "ok"}


def test_config(test_db: Any) -> None:
    response = client.get("/api/config")
    assert response.status_code == 200
    assert response.json()["status"] == "success"
    assert "profile" in response.json()["data"]["config"]
    assert response.json()["data"]["mutable"] is True


@pytest.mark.asyncio
async def test_production_config_is_explicitly_read_only() -> None:
    with pytest.raises(HTTPException) as exc_info:
        await save_config({"log_level": "DEBUG"}, Settings(profile="production"))

    assert getattr(exc_info.value, "status_code", None) == 403


def test_duplicates(test_db: Any) -> None:
    response = client.get("/api/books/all/duplicates")
    assert response.status_code == 200
    data = response.json()
    assert data["status"] == "success"
    assert isinstance(data["data"], list)


def test_approve_patch(test_db: Any) -> None:
    response = client.post("/api/review/calibre:1/approve")
    assert response.status_code == 200
    assert response.json()["status"] == "success"

    # Verify status in database
    with Session(test_db) as session:
        book = session.exec(select(BookRecord).where(BookRecord.book_key == "calibre:1")).first()
        assert book is not None
        assert book.status == "suggest_fix"


def test_lock_field_persists(test_db: Any) -> None:
    response = client.post(
        "/api/review/calibre:1/lock-field",
        json={"field": "title", "value": "Locked Title"},
    )
    assert response.status_code == 200
    data = response.json()
    assert data["data"]["field_locks"]["title"] == "Locked Title"

    with Session(test_db) as session:
        book = session.exec(select(BookRecord).where(BookRecord.book_key == "calibre:1")).first()
        assert book is not None
        assert book.field_locks.get("title") == "Locked Title"


def test_apply_only_queues_operations(test_db: Any) -> None:
    response = client.post("/api/apply", json={"force": True, "book_keys": ["calibre:1"]})

    assert response.status_code == 200
    assert response.json()["data"]["queued_count"] == 0


def test_reject_patch(test_db: Any) -> None:
    response = client.post("/api/review/calibre:1/reject")
    assert response.status_code == 200
    assert response.json()["status"] == "success"

    # Verify status in database
    with Session(test_db) as session:
        book = session.exec(select(BookRecord).where(BookRecord.book_key == "calibre:1")).first()
        assert book is not None
        assert book.status == "scanned"


def test_trailing_slash_redirection(test_db: Any) -> None:
    response = client.get("/undo/", follow_redirects=False)
    assert response.status_code == 301
    assert response.headers["location"] == "http://testserver/undo"


def test_cache_control_headers(test_db: Any) -> None:
    # Test assets
    response = client.get("/assets/index.js")
    assert response.headers["Cache-Control"] == "public, max-age=31536000, immutable"

    # Test api
    response = client.get("/api/health")
    assert response.headers["Cache-Control"] == "no-store, no-cache, must-revalidate, max-age=0"

    # Test default
    response = client.get("/undo")
    assert response.headers["Cache-Control"] == "no-cache, must-revalidate"


def test_paperless_webhook_endpoint_json_body(test_db: Any) -> None:
    from calibre_ai_auditor.config.settings import Settings

    settings = Settings()
    settings.paperless.enabled = True

    from calibre_ai_auditor.web.api.bridges import get_settings as bridges_get_settings

    app.dependency_overrides[bridges_get_settings] = lambda: settings

    with patch("calibre_ai_auditor.web.api.bridges.start_job", return_value="mock_job_id") as mock_start_job:
        # Test document_id key in JSON
        response = client.post("/api/bridges/paperless/webhook", json={"document_id": 123})
        assert response.status_code == 200
        data = response.json()
        assert data["status"] == "success"
        assert data["data"]["job_id"] == "mock_job_id"
        assert "run_paperless_webhook_123_" in data["data"]["run_id"]
        mock_start_job.assert_called_once()

    with patch("calibre_ai_auditor.web.api.bridges.start_job", return_value="mock_job_id") as mock_start_job:
        # Test id key in JSON
        response = client.post("/api/bridges/paperless/webhook", json={"id": 456})
        assert response.status_code == 200
        data = response.json()
        assert data["status"] == "success"
        assert data["data"]["job_id"] == "mock_job_id"
        assert "run_paperless_webhook_456_" in data["data"]["run_id"]
        mock_start_job.assert_called_once()

    with patch("calibre_ai_auditor.web.api.bridges.start_job", return_value="mock_job_id") as mock_start_job:
        # Test document int key in JSON
        response = client.post("/api/bridges/paperless/webhook", json={"document": 789})
        assert response.status_code == 200
        data = response.json()
        assert data["status"] == "success"
        mock_start_job.assert_called_once()

    with patch("calibre_ai_auditor.web.api.bridges.start_job", return_value="mock_job_id") as mock_start_job:
        # Test document object id key in JSON
        response = client.post("/api/bridges/paperless/webhook", json={"document": {"id": 111}})
        assert response.status_code == 200
        data = response.json()
        assert data["status"] == "success"
        mock_start_job.assert_called_once()

    with patch("calibre_ai_auditor.web.api.bridges.start_job", return_value="mock_job_id") as mock_start_job:
        # Test query parameter
        response = client.post("/api/bridges/paperless/webhook?document_id=222")
        assert response.status_code == 200
        data = response.json()
        assert data["status"] == "success"
        mock_start_job.assert_called_once()

    # Test missing document ID
    response = client.post("/api/bridges/paperless/webhook", json={})
    assert response.status_code == 400

    # Test invalid document ID format
    response = client.post("/api/bridges/paperless/webhook", json={"document_id": "abc"})
    assert response.status_code == 400

    app.dependency_overrides.clear()


@pytest.mark.asyncio
async def test_do_paperless_webhook_audit(tmp_path: Any) -> None:
    # Reset the global engine cache to ensure a fresh test database is created
    import calibre_ai_auditor.storage.db

    calibre_ai_auditor.storage.db._engine = None

    try:
        mock_doc = {"id": 123, "title": "Mocked Book", "created": "2026-05-20"}

        mock_bridge = MagicMock()
        mock_bridge.get_document = AsyncMock(return_value=mock_doc)
        mock_bridge.download_document_file = AsyncMock(return_value=tmp_path / "mocked_book.pdf")

        dummy_pdf = tmp_path / "mocked_book.pdf"
        dummy_pdf.write_bytes(b"dummy pdf bytes")

        from calibre_ai_auditor.config.settings import Settings

        settings = Settings()
        settings.paperless.enabled = True
        settings.storage.sqlite_path = tmp_path / "test.db"
        settings.storage.artifacts_dir = tmp_path / "artifacts"

        from sqlmodel import Session, SQLModel, select

        from calibre_ai_auditor.storage.db import get_engine

        engine = get_engine(settings)
        SQLModel.metadata.create_all(engine)

        from calibre_ai_auditor.storage.models import BookRecord, Run

        run_id = "test_webhook_run"
        with Session(engine) as session:
            run = Run(run_id=run_id, status="started")
            session.add(run)
            session.commit()

        with (
            patch("calibre_ai_auditor.integrations.paperless.PaperlessBridge", return_value=mock_bridge),
            patch("calibre_ai_auditor.audit.engine.run_audit", new_callable=AsyncMock) as mock_run_audit,
            patch("calibre_ai_auditor.web.api.bridges.get_engine", return_value=engine),
        ):
            from calibre_ai_auditor.web.api.bridges import do_paperless_webhook_audit

            await do_paperless_webhook_audit(settings, 123, run_id)

            mock_run_audit.assert_called_once_with(settings, run_id, judge=True, save_evidence=True)

            with Session(engine) as session:
                book = session.exec(select(BookRecord).where(BookRecord.book_key == "paperless:123")).first()
                assert book is not None
                assert book.current_metadata["title"] == "Mocked Book"
                assert book.paperless_document_id == 123
                assert book.source == "paperless"

                run = session.exec(select(Run).where(Run.run_id == run_id)).first()
                assert run is not None
                assert run.status == "completed"
    finally:
        # Clean up global engine cache
        calibre_ai_auditor.storage.db._engine = None

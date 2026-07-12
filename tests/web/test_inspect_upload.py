import io
from typing import Any
from unittest.mock import patch

from fastapi.testclient import TestClient

from calibre_ai_auditor.config.settings import Settings
from calibre_ai_auditor.web.api.inspect import get_settings as inspect_get_settings
from calibre_ai_auditor.web.app import app

client = TestClient(app)


def test_upload_disabled_by_default() -> None:
    settings = Settings()
    settings.allow_remote_file_upload = False
    app.dependency_overrides[inspect_get_settings] = lambda: settings
    try:
        response = client.post(
            "/api/inspect/upload",
            files={"file": ("book.epub", io.BytesIO(b"fake"), "application/epub+zip")},
        )
        assert response.status_code == 403
    finally:
        app.dependency_overrides.clear()


def test_upload_enabled(tmp_path: Any) -> None:
    settings = Settings()
    settings.allow_remote_file_upload = True
    settings.storage.artifacts_dir = tmp_path / "artifacts"
    app.dependency_overrides[inspect_get_settings] = lambda: settings
    try:
        with patch("calibre_ai_auditor.web.api.inspect._build_inspection_package") as mock_build:
            mock_build.return_value = {"evidence_id": "ev_test"}
            response = client.post(
                "/api/inspect/upload",
                files={"file": ("book.epub", io.BytesIO(b"fake"), "application/epub+zip")},
            )
        assert response.status_code == 200
        assert response.json()["evidence_id"] == "ev_test"
    finally:
        app.dependency_overrides.clear()

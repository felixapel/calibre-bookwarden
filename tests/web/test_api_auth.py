import pytest
from fastapi.testclient import TestClient

from calibre_ai_auditor.web.app import app


def test_production_api_fails_closed_without_key(monkeypatch: pytest.MonkeyPatch) -> None:
    monkeypatch.setenv("BOOKAUDIT_PROFILE", "production")
    monkeypatch.delenv("BOOKAUDIT_API_KEY", raising=False)

    response = TestClient(app).get("/api/config")

    assert response.status_code == 503
    assert response.json()["detail"] == "API authentication is not configured"


def test_production_api_requires_matching_key(monkeypatch: pytest.MonkeyPatch) -> None:
    monkeypatch.setenv("BOOKAUDIT_PROFILE", "production")
    monkeypatch.setenv("BOOKAUDIT_API_KEY", "test-api-key")
    client = TestClient(app)

    assert client.get("/api/config").status_code == 401
    assert client.get("/api/config", headers={"X-API-Key": "wrong"}).status_code == 401
    assert client.get("/api/config", headers={"X-API-Key": "test-api-key"}).status_code == 200


def test_liveness_does_not_require_api_key(monkeypatch: pytest.MonkeyPatch) -> None:
    monkeypatch.setenv("BOOKAUDIT_PROFILE", "production")
    monkeypatch.delenv("BOOKAUDIT_API_KEY", raising=False)

    response = TestClient(app).get("/api/health/live")

    assert response.status_code == 200
    assert response.json() == {"status": "alive"}


def test_browser_responses_include_production_security_headers(monkeypatch: pytest.MonkeyPatch) -> None:
    monkeypatch.setenv("BOOKAUDIT_PROFILE", "production")
    monkeypatch.setenv("BOOKAUDIT_API_KEY", "test-api-key")

    response = TestClient(app).get("/api/config", headers={"X-API-Key": "test-api-key"})

    assert response.headers["Content-Security-Policy"] == (
        "default-src 'self'; base-uri 'self'; frame-ancestors 'none'; "
        "form-action 'self'; object-src 'none'; img-src 'self' data:; "
        "style-src 'self' 'unsafe-inline'; script-src 'self'; connect-src 'self'"
    )
    assert response.headers["X-Content-Type-Options"] == "nosniff"
    assert response.headers["X-Frame-Options"] == "DENY"
    assert response.headers["Referrer-Policy"] == "no-referrer"
    assert response.headers["Permissions-Policy"] == "camera=(), microphone=(), geolocation=()"


def test_readiness_is_authenticated_and_fails_when_dependencies_are_unavailable(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    monkeypatch.setenv("BOOKAUDIT_PROFILE", "production")
    monkeypatch.setenv("BOOKAUDIT_API_KEY", "test-api-key")
    monkeypatch.setenv("BOOKAUDIT_LIBRARY_PATH", "/does/not/exist")

    response = TestClient(app).get(
        "/api/health/ready",
        headers={"X-API-Key": "test-api-key"},
    )

    assert response.status_code == 503
    assert response.json()["detail"]["status"] == "not_ready"
    assert response.json()["detail"]["checks"]["library"]["ok"] is False

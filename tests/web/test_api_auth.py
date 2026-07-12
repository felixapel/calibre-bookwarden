import pytest
from fastapi.testclient import TestClient

from calibre_ai_auditor.web.app import app

PRODUCTION_KEY = "test-api-key-with-at-least-32-characters"


def test_production_api_fails_closed_without_key(monkeypatch: pytest.MonkeyPatch) -> None:
    monkeypatch.setenv("BOOKAUDIT_PROFILE", "production")
    monkeypatch.delenv("BOOKAUDIT_API_KEY", raising=False)

    response = TestClient(app).get("/api/config")

    assert response.status_code == 503
    assert response.json()["detail"] == "API authentication is not configured"


def test_production_api_requires_matching_key(monkeypatch: pytest.MonkeyPatch) -> None:
    monkeypatch.setenv("BOOKAUDIT_PROFILE", "production")
    monkeypatch.setenv("BOOKAUDIT_API_KEY", PRODUCTION_KEY)
    client = TestClient(app)

    assert client.get("/api/config").status_code == 401
    assert client.get("/api/config", headers={"X-API-Key": "wrong"}).status_code == 401
    assert client.get("/api/config", headers={"X-API-Key": PRODUCTION_KEY}).status_code == 200


def test_production_rejects_weak_or_placeholder_api_key(monkeypatch: pytest.MonkeyPatch) -> None:
    monkeypatch.setenv("BOOKAUDIT_PROFILE", "production")
    client = TestClient(app)

    for weak_key in ("test-api-key", "replace-with-at-least-32-random-characters"):
        monkeypatch.setenv("BOOKAUDIT_API_KEY", weak_key)
        response = client.get("/api/config", headers={"X-API-Key": weak_key})
        assert response.status_code == 503
        assert response.json()["detail"] == "API authentication is not securely configured"


def test_liveness_does_not_require_api_key(monkeypatch: pytest.MonkeyPatch) -> None:
    monkeypatch.setenv("BOOKAUDIT_PROFILE", "production")
    monkeypatch.delenv("BOOKAUDIT_API_KEY", raising=False)

    response = TestClient(app).get("/api/health/live")

    assert response.status_code == 200
    assert response.json() == {"status": "alive"}


def test_browser_responses_include_production_security_headers(monkeypatch: pytest.MonkeyPatch) -> None:
    monkeypatch.setenv("BOOKAUDIT_PROFILE", "production")
    monkeypatch.setenv("BOOKAUDIT_API_KEY", PRODUCTION_KEY)

    response = TestClient(app).get("/api/config", headers={"X-API-Key": PRODUCTION_KEY})

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
    monkeypatch.setenv("BOOKAUDIT_API_KEY", PRODUCTION_KEY)
    monkeypatch.setenv("BOOKAUDIT_LIBRARY_PATH", "/does/not/exist")

    response = TestClient(app).get(
        "/api/health/ready",
        headers={"X-API-Key": PRODUCTION_KEY},
    )

    assert response.status_code == 503
    assert response.json()["detail"]["status"] == "not_ready"
    assert response.json()["detail"]["checks"]["library"]["ok"] is False


def test_production_rejects_untrusted_host_before_redirect(monkeypatch: pytest.MonkeyPatch) -> None:
    monkeypatch.setenv("BOOKAUDIT_PROFILE", "production")
    monkeypatch.setenv("BOOKAUDIT_API_KEY", PRODUCTION_KEY)

    response = TestClient(app).get("/dashboard/", headers={"Host": "attacker.example"}, follow_redirects=False)

    assert response.status_code == 400
    assert response.json()["detail"] == "Invalid host header"

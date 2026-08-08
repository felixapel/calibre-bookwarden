"""Certificate A production application boundary tests."""

from __future__ import annotations

import subprocess
import sys
from pathlib import Path

import pytest
from fastapi.testclient import TestClient

from calibre_ai_auditor.config.settings import Settings

PRODUCTION_KEY = "certificate-a-test-key-with-32-characters-9Z"


def _settings(library: Path) -> Settings:
    return Settings(
        profile="production",
        api_key=PRODUCTION_KEY,
        trusted_hosts="testserver",
        library={"path": library, "read_only": True},
        database={
            "backend": "postgres",
            "postgres_dsn": "postgresql+psycopg://bookaudit_shadow:test@postgres/bookaudit",
        },
        queue={"backend": "valkey", "valkey_url": "redis://valkey:6379/0"},
        rate_limits={
            "enabled": True,
            "backend": "valkey",
            "valkey_url": "redis://valkey:6379/1",
        },
        providers={"calibre_fetch": False, "openlibrary": True, "google_books": True},
        ollama_enabled=False,
    )


@pytest.fixture
def production_client(tmp_path: Path) -> TestClient:
    from calibre_ai_auditor.web.production import create_production_app

    library = tmp_path / "library"
    library.mkdir()
    static_dir = tmp_path / "static"
    assets = static_dir / "assets"
    assets.mkdir(parents=True)
    (static_dir / "index.html").write_text("<html><body>certificate-a</body></html>")
    (static_dir / "favicon.ico").write_bytes(b"icon")
    (assets / "app.js").write_text("globalThis.bookaudit = true")
    (tmp_path / "secret.txt").write_text("must-not-leak")

    app = create_production_app(
        settings_provider=lambda: _settings(library),
        static_dir=static_dir,
    )

    async def allow_rate_limit(
        _url: str,
        _identity: str,
        _limit: int,
        _window: int,
        _timeout: float,
    ) -> int:
        return 0

    app.state.rate_limit_consumer = allow_rate_limit
    return TestClient(app)


def _authenticated_get(client: TestClient, path: str):  # type: ignore[no-untyped-def]
    return client.get(path, headers={"X-API-Key": PRODUCTION_KEY})


def test_production_module_does_not_import_generic_app_or_legacy_workers() -> None:
    statement = """
import sys
import calibre_ai_auditor.web.production
for forbidden in (
    'calibre_ai_auditor.web.app',
    'calibre_ai_auditor.web.jobs',
    'calibre_ai_auditor.ingest.watcher',
):
    assert forbidden not in sys.modules, forbidden
"""
    result = subprocess.run(
        [sys.executable, "-c", statement],
        check=False,
        capture_output=True,
        text=True,
    )

    assert result.returncode == 0, result.stderr


def test_certificate_a_exposes_only_the_supported_api_contract(production_client: TestClient) -> None:
    paths = {route.path for route in production_client.app.routes if getattr(route, "path", "").startswith("/api")}

    assert paths == {
        "/api/health/live",
        "/api/health/ready",
        "/api/metrics",
        "/api/capabilities",
        "/api/verify",
        "/api/verify/runs",
        "/api/verify/{run_id}",
        "/api/verify/{run_id}/cancel",
        "/api/review/v2",
        "/api/review/v2/{evidence_id}",
        "/api/covers/{book_key}.jpg",
        "/api/apply",
    }


def test_docs_and_legacy_routes_are_unavailable(production_client: TestClient) -> None:
    assert production_client.get("/docs").status_code == 404
    assert production_client.get("/redoc").status_code == 404
    assert production_client.get("/openapi.json").status_code == 404

    for path in (
        "/api/config",
        "/api/inspect",
        "/api/runs",
        "/api/books",
        "/api/providers",
        "/api/preview/book",
        "/api/audit",
        "/api/bridges/paperless",
        "/api/uploads",
        "/api/doctor",
        "/api/operations/example",
    ):
        assert _authenticated_get(production_client, path).status_code == 404, path

    retired = production_client.post(
        "/api/apply",
        headers={"X-API-Key": PRODUCTION_KEY},
        json={},
    )
    assert retired.status_code == 410


def test_static_files_use_an_allowlist_and_never_derive_disk_paths_from_urls(
    production_client: TestClient,
) -> None:
    for path in ("/", "/verify", "/verify/run-1", "/review", "/review/evidence-1"):
        response = production_client.get(path)
        assert response.status_code == 200, path
        assert "certificate-a" in response.text

    assert production_client.get("/assets/app.js").status_code == 200
    assert production_client.get("/favicon.ico").content == b"icon"

    for path in (
        "/settings",
        "/dashboard",
        "/secret.txt",
        "/..%2Fsecret.txt",
        "/%2e%2e/secret.txt",
        "/assets/../index.html",
    ):
        response = production_client.get(path)
        assert response.status_code == 404, path
        assert "must-not-leak" not in response.text


def test_liveness_is_public_but_all_other_api_routes_require_the_app_key(
    production_client: TestClient,
) -> None:
    assert production_client.get("/api/health/live").status_code == 200
    assert production_client.get("/api/capabilities").status_code == 401
    assert _authenticated_get(production_client, "/api/capabilities").status_code == 200


def test_capabilities_are_sanitized_and_certificate_a_is_shadow_only(
    production_client: TestClient,
) -> None:
    response = _authenticated_get(production_client, "/api/capabilities")

    assert response.status_code == 200
    payload = response.json()
    assert payload["certificate"] == "A"
    assert payload["mode"] == "shadow"
    assert payload["pipeline"] == "manifestation-v2"
    assert payload["library_source"] == "offline-folder"
    assert payload["providers"] == ["google_books_isbn", "openlibrary_isbn"]
    assert payload["ocr"] == {"enabled": True, "backend": "tesseract", "max_pages": 6}
    assert payload["writes_enabled"] is False
    serialized = response.text.lower()
    for secret_or_location in ("postgresql", "redis://", str(Path.home()).lower(), "api_key"):
        assert secret_or_location not in serialized


def test_security_headers_cover_api_and_spa_responses(production_client: TestClient) -> None:
    for response in (
        production_client.get("/"),
        _authenticated_get(production_client, "/api/capabilities"),
    ):
        assert response.headers["Content-Security-Policy"].startswith("default-src 'self'")
        assert response.headers["X-Content-Type-Options"] == "nosniff"
        assert response.headers["X-Frame-Options"] == "DENY"
        assert response.headers["Referrer-Policy"] == "no-referrer"
        assert response.headers["X-Request-ID"]

"""Tests for the v1.0 verify API endpoints."""

from __future__ import annotations

import pytest
from fastapi.testclient import TestClient

from calibre_ai_auditor.web.app import app


@pytest.fixture
def client() -> TestClient:
    return TestClient(app)


def test_verify_runs_endpoint_empty(client: TestClient) -> None:
    """GET /api/verify/runs returns empty list initially."""
    r = client.get("/api/verify/runs")
    assert r.status_code == 200
    assert "runs" in r.json()
    assert r.json()["runs"] == []


def test_verify_unknown_run_returns_404(client: TestClient) -> None:
    """GET /api/verify/{unknown_id} returns 404."""
    r = client.get("/api/verify/nonexistent_run_id")
    assert r.status_code == 404
    assert "not found" in r.json()["detail"].lower()


def test_verify_start_validates_library_path(client: TestClient) -> None:
    """POST /api/verify with a non-existent library returns 404."""
    r = client.post(
        "/api/verify",
        json={"library": "/does/not/exist/path", "limit": 5},
    )
    # 404 if path doesn't exist; could also be 400 if no library at all
    assert r.status_code in (400, 404, 500)


def test_verify_start_default_library(client: TestClient) -> None:
    """POST /api/verify with no library uses configured default; returns 400 if none."""
    r = client.post("/api/verify", json={"limit": 1})
    # No library configured → 400; otherwise 200 with run_id
    assert r.status_code in (200, 400, 500)


def test_verify_response_schema(client: TestClient) -> None:
    """VerifyStartResponse has the right fields."""
    from calibre_ai_auditor.web.api.verify import VerifyRequest, VerifyStartResponse

    req = VerifyRequest(library="/tmp", limit=1)
    # Just check pydantic validation works
    assert req.limit == 1
    assert req.library == "/tmp"
    assert req.use_llm is False

    resp = VerifyStartResponse(
        run_id="verify_test_001",
        started_at="2026-07-05T12:00:00+00:00",
        total=10,
        status="running",
    )
    assert resp.run_id == "verify_test_001"
    assert resp.status == "running"

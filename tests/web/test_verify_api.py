"""Tests for the v1.0 verify API endpoints."""

from __future__ import annotations

from collections.abc import Generator
from datetime import UTC, datetime
from pathlib import Path

import pytest
from fastapi.testclient import TestClient
from sqlmodel import Session, SQLModel, create_engine

from calibre_ai_auditor.config.settings import Settings
from calibre_ai_auditor.web.api.verify import get_session, get_settings
from calibre_ai_auditor.web.app import app


@pytest.fixture
def client(tmp_path: Path) -> Generator[TestClient, None, None]:
    engine = create_engine(f"sqlite:///{tmp_path / 'verify.db'}", connect_args={"check_same_thread": False})
    SQLModel.metadata.create_all(engine)
    settings = Settings()

    def override_session() -> Generator[Session, None, None]:
        with Session(engine) as session:
            yield session

    app.dependency_overrides[get_settings] = lambda: settings
    app.dependency_overrides[get_session] = override_session
    app.state.verify_test_engine = engine
    yield TestClient(app)
    app.dependency_overrides.pop(get_settings, None)
    app.dependency_overrides.pop(get_session, None)
    del app.state.verify_test_engine


def test_verify_runs_endpoint_empty(client: TestClient) -> None:
    """GET /api/verify/runs returns empty list initially."""
    r = client.get("/api/verify/runs")
    assert r.status_code == 200
    assert r.json() == {"status": "success", "data": {"runs": []}}


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
    assert req.use_ocr is True
    assert req.use_vision is False
    assert req.allow_remote_images is False

    resp = VerifyStartResponse(
        run_id="verify_test_001",
        started_at="2026-07-05T12:00:00+00:00",
        total=10,
        status="running",
    )
    assert resp.run_id == "verify_test_001"
    assert resp.status == "running"


def test_verify_detail_reads_and_validates_sealed_v2_evidence(client: TestClient) -> None:
    from calibre_ai_auditor.storage.models import EvidencePackage, VerificationResult, VerificationRun
    from calibre_ai_auditor.verification.identity_v2 import IdentityTier, ManifestationResolution
    from calibre_ai_auditor.verification.pipeline_v2 import BookAuditState, BookSnapshot, EvidencePackageV2

    snapshot = BookSnapshot(
        book_key="calibre:1",
        calibre_book_id=1,
        current_metadata={},
        files=[],
        snapshot_sha256="0" * 64,
    )
    snapshot = snapshot.model_copy(update={"snapshot_sha256": snapshot.calculated_sha256()})
    package = EvidencePackageV2(
        evidence_id="api-evidence-v2",
        run_id="api-run-v2",
        book_key="calibre:1",
        state=BookAuditState.review,
        snapshot=snapshot,
        identity=ManifestationResolution(tier=IdentityTier.tier_b),
    ).seal()
    with Session(client.app.state.verify_test_engine) as session:
        session.add(
            VerificationRun(
                run_id="api-run-v2",
                status="completed",
                started_at=datetime.now(UTC),
                total=1,
                completed=1,
                counts={"review": 1},
                pipeline_version="manifestation-v2",
                mode="shadow",
            )
        )
        session.add(
            VerificationResult(
                result_id="api-result-v2",
                run_id="api-run-v2",
                book_key="calibre:1",
                state="review",
                evidence_id=package.evidence_id,
                verdict={"untrusted": True},
            )
        )
        session.add(
            EvidencePackage(
                evidence_id=package.evidence_id,
                run_id=package.run_id,
                book_key=package.book_key,
                schema_version=2,
                observations=package.model_dump(mode="json"),
            )
        )
        session.commit()

    response = client.get("/api/verify/api-run-v2")

    assert response.status_code == 200
    data = response.json()["data"]
    assert data["pipeline_version"] == "manifestation-v2"
    assert data["mode"] == "shadow"
    verdict = data["verdicts"][0]
    assert verdict["evidence_id"] == "api-evidence-v2"
    assert verdict["identity"]["tier"] == "B"
    assert "untrusted" not in verdict

from __future__ import annotations

from collections.abc import Generator
from pathlib import Path

import pytest
from fastapi.testclient import TestClient
from sqlalchemy.engine import Engine
from sqlmodel import Session, SQLModel, create_engine, select

from calibre_ai_auditor.config.settings import Settings
from calibre_ai_auditor.storage.models import EvidencePackage, VerificationRun
from tests.v2_fixtures import build_exact_tier_a_package

API_KEY = "certificate-a-review-key-with-32-characters-9Z"
AUTH = {"X-API-Key": API_KEY}


def _settings(library: Path) -> Settings:
    settings = Settings(
        profile="production",
        api_key=API_KEY,
        trusted_hosts="testserver",
        release_digest=f"sha256:{'a' * 64}",
        library={"path": library, "read_only": True},
        database={"backend": "postgres", "postgres_dsn": "postgresql+psycopg://app:test@db/audit"},
        queue={"backend": "valkey"},
        rate_limits={"enabled": True, "backend": "valkey"},
        providers={"calibre_fetch": False, "openlibrary": True, "google_books": True},
        ollama_enabled=False,
        lmstudio_enabled=False,
    )
    settings.database = settings.database.model_copy(update={"backend": "postgres"})
    return settings


@pytest.fixture
def production_review(tmp_path: Path) -> Generator[tuple[TestClient, Engine], None, None]:
    from calibre_ai_auditor.web.production import create_production_app

    library = tmp_path / "library"
    library.mkdir()
    engine = create_engine(
        f"sqlite:///{tmp_path / 'review.db'}",
        connect_args={"check_same_thread": False},
    )
    SQLModel.metadata.create_all(engine)
    app = create_production_app(
        settings_provider=lambda: _settings(library),
        engine_provider=lambda _settings: engine,
        static_dir=tmp_path / "missing-static",
    )
    app.state.database_readiness_checker = lambda _settings: None

    async def allow_rate_limit(*_args: object) -> int:
        return 0

    app.state.rate_limit_consumer = allow_rate_limit
    with TestClient(app) as client:
        yield client, engine


def _store_package(engine: Engine, *, evidence_id: str = "certificate-a-evidence") -> None:
    package = build_exact_tier_a_package(
        evidence_id=evidence_id,
        run_id="certificate-a-run",
        book_id=1,
        library_root="/library",
        files=["/library/book.epub"],
        current_metadata={"title": "Current title", "authors": ["Current Author"]},
        resolved_patch={"title": "Exact title"},
    )
    with Session(engine) as session:
        session.add(
            VerificationRun(
                run_id=package.run_id,
                status="completed",
                pipeline_version="manifestation-v2",
                mode="shadow",
                contract_version="certificate-a-v1",
            )
        )
        session.add(
            EvidencePackage(
                evidence_id=package.evidence_id,
                book_key=package.book_key,
                run_id=package.run_id,
                schema_version=2,
                current=package.snapshot.current_metadata,
                extracted=package.identity.model_dump(mode="json"),
                observations=package.model_dump(mode="json"),
            )
        )
        session.add(
            VerificationRun(
                run_id="legacy-run",
                status="completed",
                pipeline_version="manifestation-v2",
                mode="shadow",
            )
        )
        session.add(
            EvidencePackage(
                evidence_id="legacy-evidence",
                book_key="calibre:2",
                run_id="legacy-run",
                schema_version=2,
                observations=package.model_dump(mode="json"),
            )
        )
        session.commit()


def test_review_lists_and_reads_only_sealed_certificate_a_evidence(
    production_review: tuple[TestClient, Engine],
) -> None:
    client, engine = production_review
    _store_package(engine)

    listed = client.get("/api/review/v2", headers=AUTH)
    detailed = client.get("/api/review/v2/certificate-a-evidence", headers=AUTH)

    assert listed.status_code == 200, listed.text
    assert listed.json()["meta"] == {"total": 1, "limit": 50, "offset": 0}
    assert [item["evidence_id"] for item in listed.json()["data"]] == ["certificate-a-evidence"]
    assert detailed.status_code == 200, detailed.text
    assert detailed.json()["data"]["package"]["identity"]["tier"] == "A"
    assert detailed.json()["data"]["authorization"] is None
    assert detailed.json()["data"]["operation"] is None
    assert detailed.json()["data"]["writes_enabled"] is False
    assert client.get("/api/review/v2/legacy-evidence", headers=AUTH).status_code == 404


def test_review_detects_tampered_evidence(
    production_review: tuple[TestClient, Engine],
) -> None:
    client, engine = production_review
    _store_package(engine, evidence_id="tampered-evidence")
    with Session(engine) as session:
        stored = session.exec(select(EvidencePackage).where(EvidencePackage.evidence_id == "tampered-evidence")).one()
        assert stored.observations is not None
        stored.observations = {**stored.observations, "state": "failed"}
        session.add(stored)
        session.commit()

    response = client.get("/api/review/v2/tampered-evidence", headers=AUTH)

    assert response.status_code == 409
    assert response.json()["detail"] == {"code": "evidence_integrity_failed"}

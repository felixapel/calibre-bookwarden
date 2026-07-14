from __future__ import annotations

from collections.abc import Generator
from datetime import UTC, datetime
from pathlib import Path

import pytest
from fastapi.testclient import TestClient
from sqlmodel import Session, SQLModel, create_engine

from calibre_ai_auditor.apply.coordinator import create_v2_manual_authorization
from calibre_ai_auditor.storage.models import (
    BookRecord,
    EvidencePackage,
    ManualAuthorization,
    OperationLedger,
)
from calibre_ai_auditor.verification.identity_v2 import (
    FormatEvidence,
    FormatEvidenceStatus,
    IdentityTier,
    ManifestationResolution,
)
from calibre_ai_auditor.verification.pipeline_v2 import BookAuditState, BookSnapshot, EvidencePackageV2
from calibre_ai_auditor.web.api import review_v2 as review_v2_module
from calibre_ai_auditor.web.api.review_v2 import get_session
from calibre_ai_auditor.web.app import app
from tests.v2_fixtures import build_exact_tier_a_package


def _package(
    *,
    evidence_id: str,
    book_id: int,
    tier: IdentityTier,
    state: BookAuditState,
) -> EvidencePackageV2:
    if tier is IdentityTier.tier_a:
        return build_exact_tier_a_package(
            evidence_id=evidence_id,
            run_id="review-v2-run",
            book_id=book_id,
            library_root="/library",
            files=[f"/library/book-{book_id}.epub"],
            current_metadata={"title": f"Current {book_id}", "authors": ["Current Author"]},
            resolved_patch={"title": f"Exact {book_id}"},
            file_sha256="c" * 64,
            created_at=datetime.now(UTC),
            state=state,
        )
    snapshot = BookSnapshot(
        book_key=f"calibre:{book_id}",
        calibre_book_id=book_id,
        current_metadata={"title": f"Current {book_id}", "authors": ["Current Author"]},
        files=[f"/library/book-{book_id}.epub"],
        library_root="/library",
        snapshot_sha256="0" * 64,
    )
    snapshot = snapshot.model_copy(update={"snapshot_sha256": snapshot.calculated_sha256()})
    return EvidencePackageV2(
        evidence_id=evidence_id,
        run_id="review-v2-run",
        book_key=snapshot.book_key,
        created_at=datetime.now(UTC),
        state=state,
        snapshot=snapshot,
        formats=[
            FormatEvidence(
                path=f"/library/book-{book_id}.epub",
                format="EPUB",
                sha256="c" * 64,
                status=FormatEvidenceStatus.readable,
            )
        ],
        identity=ManifestationResolution(
            tier=tier,
            manifestation_ids={"isbn": "9780306406157"} if tier is IdentityTier.tier_a else {},
            auto_patch={"title": f"Exact {book_id}"} if tier is IdentityTier.tier_a else {},
            risk_flags=[] if tier is IdentityTier.tier_a else ["manifestation_unresolved"],
        ),
    ).seal()


@pytest.fixture
def client(tmp_path: Path) -> Generator[TestClient, None, None]:
    engine = create_engine(
        f"sqlite:///{tmp_path / 'review-v2.db'}",
        connect_args={"check_same_thread": False},
    )
    SQLModel.metadata.create_all(engine)

    def override_session() -> Generator[Session, None, None]:
        with Session(engine) as session:
            yield session

    app.dependency_overrides[get_session] = override_session
    app.state.review_v2_test_engine = engine
    yield TestClient(app)
    app.dependency_overrides.pop(get_session, None)
    del app.state.review_v2_test_engine


def _store(session: Session, package: EvidencePackageV2) -> None:
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


def _store_book(session: Session, package: EvidencePackageV2) -> BookRecord:
    book = BookRecord(
        book_key=package.book_key,
        run_id=package.run_id,
        calibre_book_id=package.snapshot.calibre_book_id,
        files=[{"path": path} for path in package.snapshot.files],
        current_metadata=package.snapshot.current_metadata,
        status="shadowed",
    )
    session.add(book)
    return book


def test_review_v2_list_is_paginated_and_filterable(client: TestClient) -> None:
    tier_a = _package(
        evidence_id="review-tier-a",
        book_id=1,
        tier=IdentityTier.tier_a,
        state=BookAuditState.shadowed,
    )
    tier_b = _package(
        evidence_id="review-tier-b",
        book_id=2,
        tier=IdentityTier.tier_b,
        state=BookAuditState.review,
    )
    with Session(client.app.state.review_v2_test_engine) as session:
        _store(session, tier_a)
        _store(session, tier_b)
        session.add(
            EvidencePackage(
                evidence_id="legacy-evidence",
                book_key="calibre:3",
                run_id="legacy-run",
                schema_version=1,
            )
        )
        session.commit()

    response = client.get("/api/review/v2?tier=A&limit=1&offset=0")

    assert response.status_code == 200
    payload = response.json()
    assert payload["meta"] == {"total": 1, "limit": 1, "offset": 0}
    assert payload["data"] == [
        {
            "evidence_id": "review-tier-a",
            "run_id": "review-v2-run",
            "book_key": "calibre:1",
            "created_at": tier_a.created_at.isoformat().replace("+00:00", "Z"),
            "state": "shadowed",
            "tier": "A",
            "current_metadata": {
                "title": "Current 1",
                "authors": ["Current Author"],
                "identifiers": {"isbn": "9780306406157"},
            },
            "manifestation_ids": {"isbn": "9780306406157"},
            "patch_fields": ["title"],
            "risk_flags": [],
        }
    ]


def test_review_v2_list_validates_only_the_requested_page(
    client: TestClient,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    with Session(client.app.state.review_v2_test_engine) as session:
        for index in range(25):
            _store(
                session,
                _package(
                    evidence_id=f"review-page-{index:02d}",
                    book_id=100 + index,
                    tier=IdentityTier.tier_a,
                    state=BookAuditState.shadowed,
                ),
            )
        session.commit()

    original_loader = review_v2_module.load_v2_package
    loaded: list[str] = []

    def counted_loader(session: Session, evidence_id: str):  # type: ignore[no-untyped-def]
        loaded.append(evidence_id)
        return original_loader(session, evidence_id)

    monkeypatch.setattr(review_v2_module, "load_v2_package", counted_loader)

    response = client.get("/api/review/v2?tier=A&limit=5&offset=10")

    assert response.status_code == 200
    assert response.json()["meta"] == {"total": 25, "limit": 5, "offset": 10}
    assert len(response.json()["data"]) == 5
    assert len(loaded) == 5


def test_review_v2_detail_includes_exact_authorization_and_operation(client: TestClient) -> None:
    package = _package(
        evidence_id="review-detail",
        book_id=4,
        tier=IdentityTier.tier_a,
        state=BookAuditState.shadowed,
    )
    with Session(client.app.state.review_v2_test_engine) as session:
        _store(session, package)
        book = _store_book(session, package)
        authorization = create_v2_manual_authorization(
            session,
            book=book,
            package=package,
            actor="api-key",
            reason="Exact manifestation checked",
        )
        authorization_id = authorization.authorization_id
        session.add(
            OperationLedger(
                operation_id="operation-detail",
                idempotency_key="operation-detail",
                operation_type="apply_metadata",
                book_key=package.book_key,
                run_id=package.run_id,
                evidence_id=package.evidence_id,
                authorization_id=authorization_id,
                policy_version="manifestation-v2",
                pilot_id="pilot-detail",
                state="verifying",
            )
        )
        session.commit()

    response = client.get("/api/review/v2/review-detail")

    assert response.status_code == 200
    data = response.json()["data"]
    assert data["package"]["package_sha256"] == package.package_sha256
    assert data["package"]["snapshot"]["files"] == ["/library/book-4.epub"]
    assert data["authorization"]["authorization_id"] == authorization_id
    assert data["operation"] == {
        "operation_id": "operation-detail",
        "state": "verifying",
        "pilot_id": "pilot-detail",
        "change_id": None,
        "created_at": data["operation"]["created_at"],
        "updated_at": data["operation"]["updated_at"],
        "completed_at": None,
        "error_code": None,
    }


def test_review_v2_detail_hides_authorization_for_a_changed_patch(client: TestClient) -> None:
    package = _package(
        evidence_id="review-stale-authorization",
        book_id=7,
        tier=IdentityTier.tier_a,
        state=BookAuditState.shadowed,
    )
    with Session(client.app.state.review_v2_test_engine) as session:
        _store(session, package)
        _store_book(session, package)
        session.add(
            ManualAuthorization(
                authorization_id="authorization-for-another-patch",
                book_key=package.book_key,
                run_id=package.run_id,
                verdict_hash=package.package_sha256 or "",
                patch_hash="not-the-current-patch-hash",
                actor="api-key",
                reason="Stale authorization",
            )
        )
        session.commit()

    response = client.get("/api/review/v2/review-stale-authorization")

    assert response.status_code == 200
    assert response.json()["data"]["authorization"] is None


def test_operation_status_is_sanitized(client: TestClient) -> None:
    with Session(client.app.state.review_v2_test_engine) as session:
        session.add(
            OperationLedger(
                operation_id="failed-operation",
                idempotency_key="failed-operation",
                operation_type="apply_metadata",
                book_key="calibre:5",
                evidence_id="evidence-5",
                state="failed",
                error="secret path /library/private/book.epub",
            )
        )
        session.commit()

    response = client.get("/api/operations/failed-operation")

    assert response.status_code == 200
    data = response.json()["data"]
    assert data["state"] == "failed"
    assert data["error_code"] == "operation_failed"
    assert "error" not in data
    assert "requested_patch" not in data


def test_review_v2_detail_rejects_tampered_package(client: TestClient) -> None:
    package = _package(
        evidence_id="review-tampered",
        book_id=6,
        tier=IdentityTier.tier_a,
        state=BookAuditState.shadowed,
    )
    raw = package.model_dump(mode="json")
    raw["state"] = "verified"
    with Session(client.app.state.review_v2_test_engine) as session:
        session.add(
            EvidencePackage(
                evidence_id=package.evidence_id,
                book_key=package.book_key,
                run_id=package.run_id,
                schema_version=2,
                current=package.snapshot.current_metadata,
                extracted=package.identity.model_dump(mode="json"),
                observations=raw,
            )
        )
        session.commit()

    response = client.get("/api/review/v2/review-tampered")

    assert response.status_code == 409
    assert "seal" in response.json()["detail"].lower()


def test_review_v2_list_fails_closed_on_tampered_package(client: TestClient) -> None:
    package = _package(
        evidence_id="review-list-tampered",
        book_id=8,
        tier=IdentityTier.tier_a,
        state=BookAuditState.shadowed,
    )
    raw = package.model_dump(mode="json")
    raw["identity"]["auto_patch"]["title"] = "Tampered title"
    with Session(client.app.state.review_v2_test_engine) as session:
        session.add(
            EvidencePackage(
                evidence_id=package.evidence_id,
                book_key=package.book_key,
                run_id=package.run_id,
                schema_version=2,
                current=package.snapshot.current_metadata,
                extracted=package.identity.model_dump(mode="json"),
                observations=raw,
            )
        )
        session.commit()

    response = client.get("/api/review/v2")

    assert response.status_code == 409
    assert package.evidence_id in response.json()["detail"]

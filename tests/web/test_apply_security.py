from datetime import UTC, datetime
from pathlib import Path
from typing import Any

import pytest
from fastapi import HTTPException
from pydantic import ValidationError
from sqlmodel import Session, SQLModel, create_engine, select

from calibre_ai_auditor.apply.coordinator import create_manual_authorization, queue_approved_operations
from calibre_ai_auditor.apply.heartbeat import library_root_sha256
from calibre_ai_auditor.config.settings import Settings
from calibre_ai_auditor.storage.db import expected_schema_revision
from calibre_ai_auditor.storage.models import BookRecord, EvidencePackage, ManualAuthorization, OperationLedger
from calibre_ai_auditor.verification.metrics import get_metrics, reset_metrics
from calibre_ai_auditor.verification.verdict import BookVerdict
from calibre_ai_auditor.web.api import apply as apply_module
from calibre_ai_auditor.web.api.apply import V2ApplyRequest
from calibre_ai_auditor.web.schemas import ApplyRequest, ManualAuthorizationRequest
from tests.v2_fixtures import build_exact_tier_a_package


def _sealed_v2_package() -> Any:
    return build_exact_tier_a_package(
        evidence_id="evidence-v2-api",
        run_id="run-v2-api",
        book_id=9,
        library_root="/library",
        files=["/library/book-9.epub"],
        current_metadata={"title": "Wrong"},
        created_at=datetime.now(UTC),
        resolved_patch={"title": "Exact"},
    )


@pytest.mark.asyncio
async def test_v2_api_authorizes_and_queues_only_the_exact_sealed_package(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    reset_metrics()
    engine = create_engine("sqlite:///:memory:")
    SQLModel.metadata.create_all(engine)
    package = _sealed_v2_package()
    with Session(engine) as session:
        session.add(
            BookRecord(
                book_key=package.book_key,
                run_id=package.run_id,
                calibre_book_id=9,
                status="shadowed",
                current_metadata=package.snapshot.current_metadata,
                files=[{"path": path, "format": "EPUB"} for path in package.snapshot.files],
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
        session.commit()

        authorized = await apply_module.authorize_v2_patch(
            package.evidence_id,
            ManualAuthorizationRequest(reason="Compared the exact manifestation"),
            session,
        )
        authorization_id = authorized["data"]["authorization_id"]
        monkeypatch.setattr(
            apply_module,
            "read_writer_heartbeat",
            lambda *_args, **_kwargs: {
                "owner": "writer-1",
                "timestamp": datetime.now(UTC).isoformat(),
                "release_digest": f"sha256:{'b' * 64}",
                "alembic_revision": expected_schema_revision(),
                "library_root_sha256": library_root_sha256("/library"),
                "pilot_id": "api-pilot",
                "max_operations": 5,
            },
        )
        settings = Settings(
            library={"path": "/library", "read_only": False},
            manifestation_v2={
                "supervised_pilot": {
                    "enabled": True,
                    "pilot_id": "api-pilot",
                    "release_digest": f"sha256:{'b' * 64}",
                    "max_operations": 5,
                }
            },
        )
        queued = await apply_module.apply_v2_patches(
            V2ApplyRequest(
                force=True,
                evidence_id=package.evidence_id,
                authorization_id=authorization_id,
            ),
            session=session,
            settings=settings,
        )
        operation = session.exec(select(OperationLedger)).one()

    assert queued["data"]["operation_id"] == operation.operation_id
    assert queued["data"]["pilot_id"] == "api-pilot"
    assert operation.evidence_id == package.evidence_id
    assert operation.policy_version == "manifestation-v2"
    rendered_metrics = get_metrics().render()
    assert 'bookaudit_v2_authorizations_total{outcome="success"} 1' in rendered_metrics
    assert 'bookaudit_v2_apply_requests_total{outcome="queued"} 1' in rendered_metrics


def test_v2_apply_request_rejects_batch_contract() -> None:
    with pytest.raises(ValidationError, match="evidence_id"):
        V2ApplyRequest.model_validate(
            {
                "force": True,
                "evidence_ids": ["evidence-1", "evidence-2"],
                "authorization_ids": {"evidence-1": "authorization-1"},
            }
        )


@pytest.mark.asyncio
async def test_v2_api_fails_closed_when_supervised_apply_is_disabled() -> None:
    reset_metrics()
    engine = create_engine("sqlite:///:memory:")
    SQLModel.metadata.create_all(engine)
    with Session(engine) as session, pytest.raises(HTTPException) as exc_info:
        await apply_module.apply_v2_patches(
            V2ApplyRequest(force=True, evidence_id="evidence-1", authorization_id="authorization-1"),
            session=session,
            settings=Settings(),
        )

    assert exc_info.value.status_code == 503
    assert "disabled" in str(exc_info.value.detail).lower()
    assert 'bookaudit_v2_apply_requests_total{outcome="disabled"} 1' in get_metrics().render()


@pytest.mark.asyncio
async def test_apply_skips_approved_book_without_eligible_persisted_verdict() -> None:
    engine = create_engine("sqlite:///:memory:")
    SQLModel.metadata.create_all(engine)
    with Session(engine) as session:
        session.add(
            BookRecord(
                book_key="calibre:1",
                run_id="run-1",
                calibre_book_id=1,
                status="suggest_fix",
                current_metadata={"title": "Old"},
            )
        )
        session.add(
            EvidencePackage(
                evidence_id="evidence-1",
                book_key="calibre:1",
                run_id="run-1",
                decision={
                    "book_key": "calibre:1",
                    "run_id": "run-1",
                    "action": "needs_review",
                    "auto_apply_eligible": False,
                    "overall_confidence": 99,
                    "proposed_patch": {"title": "Unsafe"},
                },
            )
        )
        session.commit()

        result = queue_approved_operations(session, book_keys=["calibre:1"], authorization_ids={})

    assert result.queued_operation_ids == []


@pytest.mark.asyncio
async def test_legacy_apply_endpoint_is_retired() -> None:
    engine = create_engine("sqlite:///:memory:")
    SQLModel.metadata.create_all(engine)
    with Session(engine) as session, pytest.raises(HTTPException) as exc_info:
        await apply_module.apply_patches(ApplyRequest(force=True, book_keys=["calibre:1"]), session)

    assert getattr(exc_info.value, "status_code", None) == 410
    assert "Manifestation V2" in str(getattr(exc_info.value, "detail", ""))


@pytest.mark.asyncio
async def test_legacy_apply_cannot_be_reenabled_with_force() -> None:
    engine = create_engine("sqlite:///:memory:")
    SQLModel.metadata.create_all(engine)
    with Session(engine) as session, pytest.raises(HTTPException) as exc_info:
        await apply_module.apply_patches(ApplyRequest(force=True), session)

    assert getattr(exc_info.value, "status_code", None) == 410


@pytest.mark.asyncio
async def test_apply_uses_eligible_persisted_verdict_and_honors_field_locks() -> None:
    engine = create_engine("sqlite:///:memory:")
    SQLModel.metadata.create_all(engine)
    with Session(engine) as session:
        session.add(
            BookRecord(
                book_key="calibre:1",
                run_id="run-1",
                calibre_book_id=1,
                status="suggest_fix",
                current_metadata={"title": "Old", "publisher": "Old Publisher"},
                field_locks={"publisher": "Old Publisher"},
            )
        )
        session.add(
            EvidencePackage(
                evidence_id="evidence-1",
                book_key="calibre:1",
                run_id="run-1",
                decision={
                    "book_key": "calibre:1",
                    "run_id": "run-1",
                    "action": "suggest_fix",
                    "auto_apply_eligible": True,
                    "overall_confidence": 95,
                    "proposed_patch": {
                        "title": "Verified Title",
                        "publisher": "Untrusted Publisher",
                    },
                },
            )
        )
        session.commit()

        result = queue_approved_operations(session, book_keys=["calibre:1"], authorization_ids={})
        operation = session.exec(select(OperationLedger)).one()

    assert len(result.queued_operation_ids) == 1
    assert operation.requested_patch == {"title": "Verified Title"}


@pytest.mark.asyncio
async def test_legacy_apply_translates_field_aliases_at_writer_boundary() -> None:
    engine = create_engine("sqlite:///:memory:")
    SQLModel.metadata.create_all(engine)
    with Session(engine) as session:
        session.add(
            BookRecord(
                book_key="calibre:2",
                run_id="run-2",
                calibre_book_id=2,
                status="suggest_fix",
                current_metadata={"pubdate": None, "languages": []},
            )
        )
        session.add(
            EvidencePackage(
                evidence_id="evidence-2",
                book_key="calibre:2",
                run_id="run-2",
                decision={
                    "book_key": "calibre:2",
                    "run_id": "run-2",
                    "action": "suggest_fix",
                    "auto_apply_eligible": True,
                    "overall_confidence": 99,
                    "proposed_patch": {"published_date": "2024-01-02", "language": "eng"},
                },
            )
        )
        session.commit()

        queue_approved_operations(session, book_keys=["calibre:2"], authorization_ids={})
        operation = session.exec(select(OperationLedger)).one()

    assert operation.requested_patch == {"pubdate": "2024-01-02", "languages": ["eng"]}


def test_only_one_session_can_claim_a_book_for_apply(tmp_path: Path) -> None:
    from calibre_ai_auditor.web.api.apply import claim_book_for_apply

    engine = create_engine(f"sqlite:///{tmp_path / 'claims.db'}")
    SQLModel.metadata.create_all(engine)
    with Session(engine) as setup_session:
        book = BookRecord(
            book_key="calibre:1",
            run_id="run-1",
            calibre_book_id=1,
            status="suggest_fix",
        )
        setup_session.add(book)
        setup_session.commit()
        setup_session.refresh(book)
        book_id = book.id

    assert book_id is not None
    with Session(engine) as first_session:
        assert claim_book_for_apply(first_session, book_id) is True
    with Session(engine) as second_session:
        assert claim_book_for_apply(second_session, book_id) is False


@pytest.mark.asyncio
async def test_exact_manual_authorization_allows_ineligible_verdict() -> None:
    engine = create_engine("sqlite:///:memory:")
    SQLModel.metadata.create_all(engine)
    decision = {
        "book_key": "calibre:3",
        "run_id": "run-3",
        "action": "needs_review",
        "auto_apply_eligible": False,
        "overall_confidence": 70,
        "proposed_patch": {"title": "Operator Approved"},
    }
    with Session(engine) as session:
        book = BookRecord(
            book_key="calibre:3",
            run_id="run-3",
            calibre_book_id=3,
            status="suggest_fix",
        )
        session.add(book)
        session.add(
            EvidencePackage(
                evidence_id="evidence-3",
                book_key="calibre:3",
                run_id="run-3",
                decision=decision,
            )
        )
        session.commit()
        authorization = create_manual_authorization(
            session,
            book=book,
            verdict=BookVerdict.model_validate(decision),
            actor="operator@example",
            reason="Verified against the title page",
        )
        session.commit()
        result = queue_approved_operations(
            session,
            book_keys=[book.book_key],
            authorization_ids={book.book_key: authorization.authorization_id},
        )

    assert len(result.queued_operation_ids) == 1


@pytest.mark.asyncio
async def test_authorization_actor_is_derived_from_server_principal() -> None:
    engine = create_engine("sqlite:///:memory:")
    SQLModel.metadata.create_all(engine)
    decision = {
        "book_key": "calibre:5",
        "run_id": "run-5",
        "action": "needs_review",
        "auto_apply_eligible": False,
        "overall_confidence": 70,
        "proposed_patch": {"title": "Operator Approved"},
    }
    with Session(engine) as session:
        session.add(
            BookRecord(
                book_key="calibre:5",
                run_id="run-5",
                calibre_book_id=5,
                status="suggest_fix",
            )
        )
        session.add(
            EvidencePackage(
                evidence_id="evidence-5",
                book_key="calibre:5",
                run_id="run-5",
                decision=decision,
            )
        )
        session.commit()

        await apply_module.authorize_patch(
            "calibre:5",
            ManualAuthorizationRequest(reason="Verified against the title page"),
            session,
        )
        authorization = session.exec(select(ManualAuthorization)).one()

    assert authorization.actor == "api-key"


@pytest.mark.asyncio
async def test_idempotent_retry_does_not_regress_completed_book_status() -> None:
    engine = create_engine("sqlite:///:memory:")
    SQLModel.metadata.create_all(engine)
    decision = {
        "book_key": "calibre:4",
        "run_id": "run-4",
        "action": "suggest_fix",
        "auto_apply_eligible": True,
        "overall_confidence": 99,
        "proposed_patch": {"title": "Verified"},
    }
    with Session(engine) as session:
        book = BookRecord(book_key="calibre:4", run_id="run-4", calibre_book_id=4, status="suggest_fix")
        session.add(book)
        session.add(EvidencePackage(evidence_id="evidence-4", book_key="calibre:4", run_id="run-4", decision=decision))
        session.commit()
        first = queue_approved_operations(session, book_keys=[book.book_key], authorization_ids={})
        operation = session.exec(select(OperationLedger)).one()
        operation.state = "succeeded"
        book.status = "suggest_fix"
        session.add(operation)
        session.add(book)
        session.commit()

        second = queue_approved_operations(session, book_keys=[book.book_key], authorization_ids={})
        session.refresh(book)

    assert len(first.queued_operation_ids) == 1
    assert second.queued_operation_ids == []
    assert book.status == "suggest_fix"

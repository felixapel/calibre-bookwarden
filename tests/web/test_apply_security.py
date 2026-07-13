from pathlib import Path

import pytest
from fastapi import HTTPException
from sqlmodel import Session, SQLModel, create_engine, select

from calibre_ai_auditor.apply.coordinator import create_manual_authorization
from calibre_ai_auditor.storage.models import BookRecord, EvidencePackage, ManualAuthorization, OperationLedger
from calibre_ai_auditor.verification.verdict import BookVerdict
from calibre_ai_auditor.web.api import apply as apply_module
from calibre_ai_auditor.web.schemas import ApplyRequest, ManualAuthorizationRequest


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

        result = await apply_module.apply_patches(ApplyRequest(force=True, book_keys=["calibre:1"]), session)

    assert result["data"]["queued_count"] == 0


@pytest.mark.asyncio
async def test_apply_requires_explicit_durable_write_confirmation() -> None:
    engine = create_engine("sqlite:///:memory:")
    SQLModel.metadata.create_all(engine)
    with Session(engine) as session, pytest.raises(HTTPException) as exc_info:
        await apply_module.apply_patches(ApplyRequest(force=False), session)

    assert getattr(exc_info.value, "status_code", None) == 400


@pytest.mark.asyncio
async def test_apply_requires_an_explicit_book_selection() -> None:
    engine = create_engine("sqlite:///:memory:")
    SQLModel.metadata.create_all(engine)
    with Session(engine) as session, pytest.raises(HTTPException) as exc_info:
        await apply_module.apply_patches(ApplyRequest(force=True), session)

    assert getattr(exc_info.value, "status_code", None) == 400
    assert "book" in str(getattr(exc_info.value, "detail", "")).lower()


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

        result = await apply_module.apply_patches(ApplyRequest(force=True, book_keys=["calibre:1"]), session)
        operation = session.exec(select(OperationLedger)).one()

    assert result["data"]["queued_count"] == 1
    assert operation.requested_patch == {"title": "Verified Title"}


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
        result = await apply_module.apply_patches(
            ApplyRequest(
                force=True,
                book_keys=[book.book_key],
                authorization_ids={book.book_key: authorization.authorization_id},
            ),
            session,
        )

    assert result["data"]["queued_count"] == 1


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
        first = await apply_module.apply_patches(ApplyRequest(force=True, book_keys=[book.book_key]), session)
        operation = session.exec(select(OperationLedger)).one()
        operation.state = "succeeded"
        book.status = "suggest_fix"
        session.add(operation)
        session.add(book)
        session.commit()

        second = await apply_module.apply_patches(ApplyRequest(force=True, book_keys=[book.book_key]), session)
        session.refresh(book)

    assert first["data"]["queued_count"] == 1
    assert second["data"]["queued_count"] == 0
    assert book.status == "suggest_fix"

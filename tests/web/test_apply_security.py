from pathlib import Path

import pytest
from sqlmodel import Session, SQLModel, create_engine, select

from calibre_ai_auditor.apply.coordinator import create_manual_authorization
from calibre_ai_auditor.storage.models import BookRecord, EvidencePackage, OperationLedger
from calibre_ai_auditor.verification.verdict import BookVerdict
from calibre_ai_auditor.web.api import apply as apply_module
from calibre_ai_auditor.web.schemas import ApplyRequest


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

        result = await apply_module.apply_patches(ApplyRequest(force=True), session)

    assert result["data"]["queued_count"] == 0


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

        result = await apply_module.apply_patches(ApplyRequest(force=True), session)
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
            ApplyRequest(authorization_ids={book.book_key: authorization.authorization_id}),
            session,
        )

    assert result["data"]["queued_count"] == 1

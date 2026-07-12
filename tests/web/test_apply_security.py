from pathlib import Path
from unittest.mock import MagicMock

import pytest
from sqlmodel import Session, SQLModel, create_engine

from calibre_ai_auditor.storage.models import BookRecord, Change, EvidencePackage
from calibre_ai_auditor.web.api import apply as apply_module
from calibre_ai_auditor.web.schemas import ApplyRequest


@pytest.mark.asyncio
async def test_apply_skips_approved_book_without_eligible_persisted_verdict(
    monkeypatch: pytest.MonkeyPatch, tmp_path: Path
) -> None:
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

        settings = MagicMock()
        settings.library.path = tmp_path
        settings.storage.artifacts_dir = tmp_path / "artifacts"
        monkeypatch.setattr(apply_module, "load_settings", lambda: settings)
        monkeypatch.setattr(apply_module, "require_write_confirmation", lambda *_args, **_kwargs: None)
        apply_engine = MagicMock()
        monkeypatch.setattr(apply_module, "ApplyEngine", lambda *_args, **_kwargs: apply_engine)

        result = await apply_module.apply_patches(ApplyRequest(force=True), session)

    assert result["data"]["applied_count"] == 0
    apply_engine.apply_patch.assert_not_called()


@pytest.mark.asyncio
async def test_apply_uses_eligible_persisted_verdict_and_honors_field_locks(
    monkeypatch: pytest.MonkeyPatch, tmp_path: Path
) -> None:
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

        settings = MagicMock()
        settings.library.path = tmp_path
        settings.storage.artifacts_dir = tmp_path / "artifacts"
        monkeypatch.setattr(apply_module, "load_settings", lambda: settings)
        monkeypatch.setattr(apply_module, "require_write_confirmation", lambda *_args, **_kwargs: None)
        apply_engine = MagicMock()
        apply_engine.apply_patch.return_value = Change(
            book_key="calibre:1",
            run_id="run-1",
            before_metadata={"title": "Old"},
            after_metadata={"title": "Verified Title"},
            backup_opf_path=str(tmp_path / "before.opf"),
        )
        monkeypatch.setattr(apply_module, "ApplyEngine", lambda *_args, **_kwargs: apply_engine)

        result = await apply_module.apply_patches(ApplyRequest(force=True), session)

    assert result["data"]["applied_count"] == 1
    assert apply_engine.apply_patch.call_args.args[2] == {"title": "Verified Title"}


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

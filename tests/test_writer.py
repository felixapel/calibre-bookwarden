import hashlib
from datetime import timedelta
from pathlib import Path
from unittest.mock import MagicMock

from sqlmodel import Session, SQLModel, create_engine, select

from calibre_ai_auditor.apply.coordinator import queue_approved_operations
from calibre_ai_auditor.apply.writer import MetadataWriter, claim_next_operation, reconcile_incomplete_operations
from calibre_ai_auditor.storage.models import BookRecord, Change, EvidencePackage, OperationLedger, OutboxEvent, utc_now
from calibre_ai_auditor.storage.operations import create_operation, transition_operation


def _setup_operation(session: Session) -> str:
    session.add(
        BookRecord(
            book_key="calibre:1",
            run_id="run-1",
            calibre_book_id=1,
            current_metadata={"title": "Old"},
            status="suggest_fix",
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
                "overall_confidence": 99,
                "proposed_patch": {"title": "New"},
            },
        )
    )
    session.commit()
    result = queue_approved_operations(session, authorization_ids={})
    return result.queued_operation_ids[0]


def test_writer_verifies_successful_target() -> None:
    engine = create_engine("sqlite://")
    SQLModel.metadata.create_all(engine)
    with Session(engine) as session:
        operation_id = _setup_operation(session)
        assert claim_next_operation(session) == operation_id
        cli = MagicMock()
        cli.show_metadata.side_effect = [{"title": "Old"}, {"title": "New"}]
        apply_engine = MagicMock()
        apply_engine.apply_patch.return_value = Change(
            book_key="calibre:1",
            run_id="run-1",
            backup_opf_path="/tmp/before.opf",
        )

        operation = MetadataWriter(cli, apply_engine).process(session, operation_id)

        assert operation.state == "succeeded"
        cli.set_metadata.assert_not_called()


def test_writer_restores_verified_partial_write() -> None:
    engine = create_engine("sqlite://")
    SQLModel.metadata.create_all(engine)
    with Session(engine) as session:
        operation_id = _setup_operation(session)
        assert claim_next_operation(session) == operation_id
        cli = MagicMock()
        cli.show_metadata.side_effect = [{"title": "Old"}, {"title": "Partial"}, {"title": "Old"}]
        apply_engine = MagicMock()
        apply_engine.apply_patch.return_value = Change(
            book_key="calibre:1",
            run_id="run-1",
            backup_opf_path="/tmp/before.opf",
        )

        operation = MetadataWriter(cli, apply_engine).process(session, operation_id)

        assert operation.state == "restored"
        cli.set_metadata.assert_called_once_with(1, Path("/tmp/before.opf"))


def test_writer_marks_restore_failed_when_backup_does_not_restore_before_state() -> None:
    engine = create_engine("sqlite://")
    SQLModel.metadata.create_all(engine)
    with Session(engine) as session:
        operation_id = _setup_operation(session)
        assert claim_next_operation(session) == operation_id
        cli = MagicMock()
        cli.show_metadata.side_effect = [{"title": "Old"}, {"title": "Partial"}, {"title": "Still Partial"}]
        apply_engine = MagicMock()
        apply_engine.apply_patch.return_value = Change(
            operation_id=operation_id,
            book_key="calibre:1",
            run_id="run-1",
            backup_opf_path="/tmp/before.opf",
        )

        operation = MetadataWriter(cli, apply_engine).process(session, operation_id)

        assert operation.state == "restore_failed"


def test_reconciliation_restores_partial_crash_state(tmp_path: Path) -> None:
    engine = create_engine("sqlite://")
    SQLModel.metadata.create_all(engine)
    with Session(engine) as session:
        operation_id = _setup_operation(session)
        assert claim_next_operation(session) == operation_id
        operation = session.exec(select(OperationLedger).where(OperationLedger.operation_id == operation_id)).one()
        operation.before_metadata = {"title": "Old"}
        operation.target_metadata = {"title": "New"}
        transition_operation(operation, "writing")
        session.add(operation)
        backup = tmp_path / "before.opf"
        backup.write_text("verified backup")
        session.add(
            Change(
                operation_id=operation_id,
                book_key="calibre:1",
                run_id="run-1",
                backup_opf_path=str(backup),
                backup_opf_sha256=hashlib.sha256(backup.read_bytes()).hexdigest(),
                status="pending_apply",
            )
        )
        session.commit()
        cli = MagicMock()
        cli.show_metadata.side_effect = [{"title": "Partial"}, {"title": "Old"}]

        reconciled = reconcile_incomplete_operations(session, cli)

        session.refresh(operation)
        assert reconciled == [operation_id]
        assert operation.state == "restored"
        cli.set_metadata.assert_called_once_with(1, backup)


def test_reconciliation_requeues_claimed_operation_before_any_write() -> None:
    engine = create_engine("sqlite://")
    SQLModel.metadata.create_all(engine)
    with Session(engine) as session:
        operation_id = _setup_operation(session)
        assert claim_next_operation(session) == operation_id
        operation = session.exec(select(OperationLedger).where(OperationLedger.operation_id == operation_id)).one()
        operation.lease_expires_at = utc_now() - timedelta(seconds=1)
        session.add(operation)
        session.commit()

        reconciled = reconcile_incomplete_operations(session, MagicMock())

        operation = session.exec(select(OperationLedger).where(OperationLedger.operation_id == operation_id)).one()
        event = session.exec(select(OutboxEvent).where(OutboxEvent.aggregate_id == operation_id)).one()
        assert reconciled == [operation_id]
        assert operation.state == "requested"
        assert event.status == "pending"
        assert claim_next_operation(session) == operation_id


def test_claim_skips_stale_event_and_claims_next_requested_operation() -> None:
    engine = create_engine("sqlite://")
    SQLModel.metadata.create_all(engine)
    with Session(engine) as session:
        stale_id = _setup_operation(session)
        assert claim_next_operation(session) == stale_id
        stale = session.exec(select(OperationLedger).where(OperationLedger.operation_id == stale_id)).one()
        transition_operation(stale, "failed", error="invalid request")
        stale_event = session.exec(select(OutboxEvent).where(OutboxEvent.aggregate_id == stale_id)).one()
        stale_event.status = "pending"
        session.add(stale)
        session.add(stale_event)
        second = create_operation(
            session,
            idempotency_key="apply:run-1:calibre-2:title",
            operation_type="apply_metadata",
            book_key="calibre:2",
            run_id="run-1",
            requested_patch={"title": "Second"},
        )
        session.commit()

        assert claim_next_operation(session) == second.operation_id


def test_writer_refuses_patch_tampered_after_queueing() -> None:
    engine = create_engine("sqlite://")
    SQLModel.metadata.create_all(engine)
    with Session(engine) as session:
        operation_id = _setup_operation(session)
        operation = session.exec(select(OperationLedger).where(OperationLedger.operation_id == operation_id)).one()
        operation.requested_patch = {"title": "Injected"}
        session.add(operation)
        session.commit()
        assert claim_next_operation(session) == operation_id
        cli = MagicMock()

        result = MetadataWriter(cli, MagicMock()).process(session, operation_id)

        assert result.state == "failed"
        assert "seal changed" in (result.error or "")
        cli.show_metadata.assert_not_called()

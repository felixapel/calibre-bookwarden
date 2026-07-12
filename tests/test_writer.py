from pathlib import Path
from unittest.mock import MagicMock

from sqlmodel import Session, SQLModel, create_engine, select

from calibre_ai_auditor.apply.writer import MetadataWriter, claim_next_operation, reconcile_incomplete_operations
from calibre_ai_auditor.storage.models import BookRecord, Change, OperationLedger
from calibre_ai_auditor.storage.operations import create_operation, transition_operation


def _setup_operation(session: Session) -> str:
    session.add(
        BookRecord(
            book_key="calibre:1",
            run_id="run-1",
            calibre_book_id=1,
            current_metadata={"title": "Old"},
            status="apply_queued",
        )
    )
    operation = create_operation(
        session,
        idempotency_key="apply:run-1:calibre-1:title",
        operation_type="apply_metadata",
        book_key="calibre:1",
        run_id="run-1",
        requested_patch={"title": "New"},
    )
    session.commit()
    return operation.operation_id


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
        cli.show_metadata.side_effect = [{"title": "Old"}, {"title": "Partial"}]
        apply_engine = MagicMock()
        apply_engine.apply_patch.return_value = Change(
            book_key="calibre:1",
            run_id="run-1",
            backup_opf_path="/tmp/before.opf",
        )

        operation = MetadataWriter(cli, apply_engine).process(session, operation_id)

        assert operation.state == "restored"
        cli.set_metadata.assert_called_once_with(1, Path("/tmp/before.opf"))


def test_reconciliation_restores_partial_crash_state() -> None:
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
        session.add(
            Change(
                book_key="calibre:1",
                run_id="run-1",
                backup_opf_path="/tmp/before.opf",
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
        cli.set_metadata.assert_called_once_with(1, Path("/tmp/before.opf"))

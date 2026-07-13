import pytest
from sqlmodel import Session, SQLModel, create_engine, select

from calibre_ai_auditor.storage.models import OperationLedger, OutboxEvent
from calibre_ai_auditor.storage.operations import create_operation, transition_operation


def test_create_operation_is_idempotent_and_emits_one_event() -> None:
    engine = create_engine("sqlite://")
    SQLModel.metadata.create_all(engine)

    with Session(engine) as session:
        first = create_operation(
            session,
            idempotency_key="apply:calibre-1:patch-a",
            operation_type="apply_metadata",
            book_key="calibre:1",
            requested_patch={"title": "Correct"},
        )
        second = create_operation(
            session,
            idempotency_key="apply:calibre-1:patch-a",
            operation_type="apply_metadata",
            book_key="calibre:1",
            requested_patch={"title": "Ignored retry"},
        )
        session.commit()

        assert second.operation_id == first.operation_id
        assert second.requested_patch == {"title": "Correct"}
        assert len(session.exec(select(OperationLedger)).all()) == 1
        assert len(session.exec(select(OutboxEvent)).all()) == 1


def test_operation_state_machine_rejects_invalid_transition() -> None:
    engine = create_engine("sqlite://")
    SQLModel.metadata.create_all(engine)

    with Session(engine) as session:
        operation = create_operation(
            session,
            idempotency_key="apply:calibre-2:patch-a",
            operation_type="apply_metadata",
            book_key="calibre:2",
            requested_patch={"title": "Correct"},
        )
        transition_operation(operation, "claimed")
        transition_operation(operation, "writing")

        with pytest.raises(ValueError, match="writing -> requested"):
            transition_operation(operation, "requested")

from __future__ import annotations

import os
from concurrent.futures import ThreadPoolExecutor
from threading import Barrier

import pytest
from sqlalchemy.engine import make_url
from sqlmodel import Session, SQLModel, create_engine, select

from calibre_ai_auditor.apply.coordinator import (
    PilotGuard,
    acknowledge_v2_failure,
    create_v2_manual_authorization,
    queue_v2_operation,
    validate_apply_operation,
)
from calibre_ai_auditor.apply.heartbeat import library_root_sha256
from calibre_ai_auditor.storage.db import expected_schema_revision
from calibre_ai_auditor.storage.models import (
    BookRecord,
    EvidencePackage,
    OperationIncidentAcknowledgement,
    OperationLedger,
    OutboxEvent,
    PilotSession,
    utc_now,
)
from calibre_ai_auditor.verification.pipeline_v2 import EvidencePackageV2
from tests.v2_fixtures import build_exact_tier_a_package


def _v2_package(*, calibre_book_id: int = 8, suffix: str = "") -> EvidencePackageV2:
    return build_exact_tier_a_package(
        evidence_id=f"evidence-v2-apply{suffix}",
        run_id=f"run-v2-apply{suffix}",
        book_id=calibre_book_id,
        library_root="/library",
        files=[f"/library/book-{calibre_book_id}.epub"],
        current_metadata={"title": "Old title", "#edition": "First edition"},
        resolved_patch={"title": "Exact title", "edition_statement": "Second edition"},
    )


def _seed(
    session: Session,
    *,
    calibre_book_id: int = 8,
    suffix: str = "",
) -> tuple[BookRecord, EvidencePackageV2]:
    package = _v2_package(calibre_book_id=calibre_book_id, suffix=suffix)
    book = BookRecord(
        book_key=package.book_key,
        run_id=package.run_id,
        calibre_book_id=calibre_book_id,
        status="shadowed",
        current_metadata=package.snapshot.current_metadata,
        files=[{"path": path} for path in package.snapshot.files],
    )
    session.add(book)
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
    return book, package


def _pilot_guard(
    *,
    writer_ready: bool = True,
    pilot_id: str = "pilot-2026-07-14",
) -> PilotGuard:
    return PilotGuard(
        enabled=True,
        pilot_id=pilot_id,
        library_root="/library",
        release_digest=f"sha256:{'a' * 64}",
        alembic_revision=expected_schema_revision(),
        max_operations=5,
        writer_ready=writer_ready,
    )


def test_v2_queue_is_bound_to_sealed_evidence_patch_and_manual_authorization() -> None:
    engine = create_engine("sqlite:///:memory:")
    SQLModel.metadata.create_all(engine)
    with Session(engine) as session:
        book, package = _seed(session)
        authorization = create_v2_manual_authorization(
            session,
            book=book,
            package=package,
            actor="operator",
            reason="Compared the exact edition",
        )
        session.commit()

        operation_id = queue_v2_operation(
            session,
            evidence_id=package.evidence_id,
            authorization_id=authorization.authorization_id,
            pilot=_pilot_guard(),
        )
        operation = session.exec(select(OperationLedger)).one()
        pilot = session.exec(select(PilotSession)).one()

        assert operation_id == operation.operation_id
        assert operation.policy_version == "manifestation-v2"
        assert operation.evidence_id == package.evidence_id
        assert operation.pilot_id == pilot.pilot_id
        assert pilot.reserved_operations == 1
        assert pilot.max_operations == 5
        assert pilot.library_root_sha256
        assert operation.requested_patch == {
            "title": "Exact title",
            "edition_statement": "Second edition",
        }
        assert operation.expected_before_metadata == {
            "title": "Old title",
            "edition_statement": "First edition",
        }
        validate_apply_operation(session, operation, book, pilot=_pilot_guard())


def test_v2_queue_requires_exact_authorization() -> None:
    engine = create_engine("sqlite:///:memory:")
    SQLModel.metadata.create_all(engine)
    with Session(engine) as session:
        _book, package = _seed(session)

        with pytest.raises(ValueError, match="exact manual authorization"):
            queue_v2_operation(
                session,
                evidence_id=package.evidence_id,
                authorization_id="wrong-authorization",
                pilot=_pilot_guard(),
            )


def test_v2_queue_fails_closed_when_writer_is_not_ready() -> None:
    engine = create_engine("sqlite:///:memory:")
    SQLModel.metadata.create_all(engine)
    with Session(engine) as session:
        book, package = _seed(session)
        authorization = create_v2_manual_authorization(
            session,
            book=book,
            package=package,
            actor="operator",
            reason="Compared the exact edition",
        )
        session.commit()

        with pytest.raises(ValueError, match="writer heartbeat"):
            queue_v2_operation(
                session,
                evidence_id=package.evidence_id,
                authorization_id=authorization.authorization_id,
                pilot=_pilot_guard(writer_ready=False),
            )

        assert session.exec(select(OperationLedger)).all() == []
        assert session.exec(select(PilotSession)).all() == []


def test_v2_queue_rejects_nonempty_outbox_before_reserving_pilot_slot() -> None:
    engine = create_engine("sqlite:///:memory:")
    SQLModel.metadata.create_all(engine)
    with Session(engine) as session:
        book, package = _seed(session)
        authorization = create_v2_manual_authorization(
            session,
            book=book,
            package=package,
            actor="operator",
            reason="Compared the exact edition",
        )
        session.add(
            OutboxEvent(
                event_id="existing-event",
                aggregate_id="existing-operation",
                event_type="operation.requested",
            )
        )
        session.commit()

        with pytest.raises(ValueError, match="outbox"):
            queue_v2_operation(
                session,
                evidence_id=package.evidence_id,
                authorization_id=authorization.authorization_id,
                pilot=_pilot_guard(),
            )

        assert session.exec(select(OperationLedger)).all() == []
        assert session.exec(select(PilotSession)).all() == []


def test_v2_queue_rejects_another_nonterminal_operation() -> None:
    engine = create_engine("sqlite:///:memory:")
    SQLModel.metadata.create_all(engine)
    with Session(engine) as session:
        book, package = _seed(session)
        authorization = create_v2_manual_authorization(
            session,
            book=book,
            package=package,
            actor="operator",
            reason="Compared the exact edition",
        )
        session.add(
            OperationLedger(
                operation_id="existing-operation",
                idempotency_key="existing-operation",
                operation_type="apply_metadata",
                book_key="calibre:99",
                state="writing",
            )
        )
        session.commit()

        with pytest.raises(ValueError, match="nonterminal operation"):
            queue_v2_operation(
                session,
                evidence_id=package.evidence_id,
                authorization_id=authorization.authorization_id,
                pilot=_pilot_guard(),
            )

        assert session.exec(select(PilotSession)).all() == []


def test_v2_queue_stops_after_five_reserved_operations() -> None:
    engine = create_engine("sqlite:///:memory:")
    SQLModel.metadata.create_all(engine)
    with Session(engine) as session:
        book, package = _seed(session)
        authorization = create_v2_manual_authorization(
            session,
            book=book,
            package=package,
            actor="operator",
            reason="Compared the exact edition",
        )
        guard = _pilot_guard()
        for index in range(5):
            session.add(
                OperationLedger(
                    operation_id=f"completed-operation-{index}",
                    idempotency_key=f"completed-operation-{index}",
                    operation_type="apply_metadata",
                    book_key=f"calibre:{100 + index}",
                    policy_version="manifestation-v2",
                    pilot_id=guard.pilot_id,
                    state="succeeded",
                    completed_at=utc_now(),
                )
            )
        session.add(
            PilotSession(
                pilot_id=guard.pilot_id,
                library_root_sha256=library_root_sha256(guard.library_root),
                release_digest=guard.release_digest,
                alembic_revision=guard.alembic_revision,
                max_operations=5,
                reserved_operations=5,
            )
        )
        session.commit()

        with pytest.raises(ValueError, match="limit"):
            queue_v2_operation(
                session,
                evidence_id=package.evidence_id,
                authorization_id=authorization.authorization_id,
                pilot=guard,
            )

        operations = session.exec(select(OperationLedger)).all()
        assert len(operations) == 5
        assert all(operation.evidence_id != package.evidence_id for operation in operations)


def test_v2_writer_validation_rejects_a_stopped_pilot() -> None:
    engine = create_engine("sqlite:///:memory:")
    SQLModel.metadata.create_all(engine)
    with Session(engine) as session:
        book, package = _seed(session)
        authorization = create_v2_manual_authorization(
            session,
            book=book,
            package=package,
            actor="operator",
            reason="Compared the exact edition",
        )
        queue_v2_operation(
            session,
            evidence_id=package.evidence_id,
            authorization_id=authorization.authorization_id,
            pilot=_pilot_guard(),
        )
        operation = session.exec(select(OperationLedger)).one()
        pilot = session.exec(select(PilotSession)).one()
        pilot.state = "stopped"
        session.add(pilot)
        session.commit()

        with pytest.raises(ValueError, match="pilot"):
            validate_apply_operation(session, operation, book, pilot=_pilot_guard())


def test_v2_writer_validation_rejects_a_mismatched_runtime_release() -> None:
    engine = create_engine("sqlite:///:memory:")
    SQLModel.metadata.create_all(engine)
    with Session(engine) as session:
        book, package = _seed(session)
        authorization = create_v2_manual_authorization(
            session,
            book=book,
            package=package,
            actor="operator",
            reason="Compared the exact edition",
        )
        session.commit()
        queue_v2_operation(
            session,
            evidence_id=package.evidence_id,
            authorization_id=authorization.authorization_id,
            pilot=_pilot_guard(),
        )
        operation = session.exec(select(OperationLedger)).one()
        mismatched = PilotGuard(
            **{
                **_pilot_guard().__dict__,
                "release_digest": f"sha256:{'b' * 64}",
            }
        )

        with pytest.raises(ValueError, match="runtime binding"):
            validate_apply_operation(session, operation, book, pilot=mismatched)


def test_v2_validation_rejects_tampered_stored_package_after_queue() -> None:
    engine = create_engine("sqlite:///:memory:")
    SQLModel.metadata.create_all(engine)
    with Session(engine) as session:
        book, package = _seed(session)
        authorization = create_v2_manual_authorization(
            session,
            book=book,
            package=package,
            actor="operator",
            reason="Compared the exact edition",
        )
        session.commit()
        queue_v2_operation(
            session,
            evidence_id=package.evidence_id,
            authorization_id=authorization.authorization_id,
            pilot=_pilot_guard(),
        )
        operation = session.exec(select(OperationLedger)).one()
        stored = session.exec(select(EvidencePackage)).one()
        assert stored.observations is not None
        stored.observations = {**stored.observations, "state": "verified"}
        session.add(stored)
        session.commit()

        with pytest.raises(ValueError, match="seal"):
            validate_apply_operation(session, operation, book, pilot=_pilot_guard())


def test_v2_writer_rejects_a_reset_pilot_budget_counter() -> None:
    engine = create_engine("sqlite:///:memory:")
    SQLModel.metadata.create_all(engine)
    with Session(engine) as session:
        book, package = _seed(session)
        authorization = create_v2_manual_authorization(
            session,
            book=book,
            package=package,
            actor="operator",
            reason="Compared the exact edition",
        )
        session.commit()
        queue_v2_operation(
            session,
            evidence_id=package.evidence_id,
            authorization_id=authorization.authorization_id,
            pilot=_pilot_guard(),
        )
        operation = session.exec(select(OperationLedger)).one()
        pilot = session.exec(select(PilotSession)).one()
        pilot.reserved_operations = 0
        session.add(pilot)
        session.commit()

        with pytest.raises(ValueError, match="budget ledger"):
            validate_apply_operation(session, operation, book, pilot=_pilot_guard())


@pytest.mark.skipif(not os.environ.get("TEST_POSTGRES_DSN"), reason="TEST_POSTGRES_DSN is not configured")
def test_postgres_serializes_concurrent_different_pilot_ids() -> None:
    dsn = os.environ["TEST_POSTGRES_DSN"]
    if "test" not in (make_url(dsn).database or "").lower():
        pytest.fail("TEST_POSTGRES_DSN must name an unmistakably disposable test database")
    engine = create_engine(dsn)
    SQLModel.metadata.drop_all(engine)
    SQLModel.metadata.create_all(engine)
    try:
        queued_inputs: list[tuple[str, str, str]] = []
        with Session(engine) as setup:
            for book_id, suffix, pilot_id in (
                (81, "-concurrent-a", "pilot-concurrent-a"),
                (82, "-concurrent-b", "pilot-concurrent-b"),
            ):
                book, package = _seed(setup, calibre_book_id=book_id, suffix=suffix)
                authorization = create_v2_manual_authorization(
                    setup,
                    book=book,
                    package=package,
                    actor="concurrency-test",
                    reason="Compared the exact concurrent edition",
                )
                setup.commit()
                queued_inputs.append((package.evidence_id, authorization.authorization_id, pilot_id))

        start = Barrier(2)

        def queue_one(item: tuple[str, str, str]) -> tuple[str, str]:
            evidence_id, authorization_id, pilot_id = item
            with Session(engine) as session:
                start.wait(timeout=10)
                try:
                    operation_id = queue_v2_operation(
                        session,
                        evidence_id=evidence_id,
                        authorization_id=authorization_id,
                        pilot=_pilot_guard(pilot_id=pilot_id),
                    )
                except ValueError as exc:
                    return "rejected", str(exc)
                return "queued", operation_id

        with ThreadPoolExecutor(max_workers=2) as executor:
            results = list(executor.map(queue_one, queued_inputs))

        assert sorted(status for status, _detail in results) == ["queued", "rejected"]
        rejection = next(detail for status, detail in results if status == "rejected")
        assert "another nonterminal operation" in rejection
        with Session(engine) as check:
            assert len(check.exec(select(OperationLedger)).all()) == 1
            assert len(check.exec(select(PilotSession)).all()) == 1
    finally:
        SQLModel.metadata.drop_all(engine)
        engine.dispose()


def test_acknowledged_safe_v2_failure_no_longer_blocks_the_next_queue() -> None:
    engine = create_engine("sqlite:///:memory:")
    SQLModel.metadata.create_all(engine)
    with Session(engine) as session:
        first_book, first_package = _seed(session)
        first_authorization = create_v2_manual_authorization(
            session,
            book=first_book,
            package=first_package,
            actor="operator",
            reason="Compared the exact edition",
        )
        session.commit()
        first_operation_id = queue_v2_operation(
            session,
            evidence_id=first_package.evidence_id,
            authorization_id=first_authorization.authorization_id,
            pilot=_pilot_guard(),
        )
        first_operation = session.exec(
            select(OperationLedger).where(OperationLedger.operation_id == first_operation_id)
        ).one()
        first_operation.state = "failed"
        first_operation.error = "pre-write validation failed"
        first_operation.completed_at = utc_now()
        first_outbox = session.exec(select(OutboxEvent).where(OutboxEvent.aggregate_id == first_operation_id)).one()
        first_outbox.status = "failed"
        first_pilot = session.get(PilotSession, _pilot_guard().pilot_id)
        assert first_pilot is not None
        session.add(first_operation)
        session.add(first_outbox)
        session.commit()

        second_book, second_package = _seed(session, calibre_book_id=9, suffix="-second")
        second_authorization = create_v2_manual_authorization(
            session,
            book=second_book,
            package=second_package,
            actor="operator",
            reason="Compared the second exact edition",
        )
        session.commit()
        with pytest.raises(ValueError, match="outbox"):
            queue_v2_operation(
                session,
                evidence_id=second_package.evidence_id,
                authorization_id=second_authorization.authorization_id,
                pilot=_pilot_guard(),
            )

        with pytest.raises(ValueError, match="stopped"):
            acknowledge_v2_failure(
                session,
                operation_id=first_operation_id,
                actor="on-call-operator",
                reason="Confirmed writer failed before Calibre mutation",
            )

        first_pilot.state = "stopped"
        session.add(first_pilot)
        session.commit()
        acknowledgement = acknowledge_v2_failure(
            session,
            operation_id=first_operation_id,
            actor="on-call-operator",
            reason="Confirmed writer failed before Calibre mutation",
        )
        session.commit()
        with pytest.raises(ValueError, match="closed"):
            queue_v2_operation(
                session,
                evidence_id=second_package.evidence_id,
                authorization_id=second_authorization.authorization_id,
                pilot=_pilot_guard(),
            )

        next_pilot = _pilot_guard(pilot_id="pilot-2026-07-15")
        second_operation_id = queue_v2_operation(
            session,
            evidence_id=second_package.evidence_id,
            authorization_id=second_authorization.authorization_id,
            pilot=next_pilot,
        )

        assert acknowledgement.operation_id == first_operation_id
        assert session.get(OperationIncidentAcknowledgement, first_operation_id) is not None
        assert second_operation_id != first_operation_id
        assert session.get(PilotSession, _pilot_guard().pilot_id).reserved_operations == 1  # type: ignore[union-attr]
        assert session.get(PilotSession, next_pilot.pilot_id).reserved_operations == 1  # type: ignore[union-attr]


@pytest.mark.parametrize("state", ["unknown", "restore_failed", "succeeded"])
def test_incident_acknowledgement_rejects_unsafe_or_successful_states(state: str) -> None:
    engine = create_engine("sqlite:///:memory:")
    SQLModel.metadata.create_all(engine)
    with Session(engine) as session:
        operation = OperationLedger(
            operation_id=f"operation-{state}",
            idempotency_key=f"operation-{state}",
            operation_type="apply_metadata",
            book_key="calibre:1",
            policy_version="manifestation-v2",
            state=state,
            completed_at=utc_now(),
        )
        session.add(operation)
        session.add(
            OutboxEvent(
                event_id=f"outbox-{state}",
                aggregate_id=operation.operation_id,
                event_type="operation.requested",
                status="failed",
            )
        )
        session.commit()

        with pytest.raises(ValueError, match="safe failed state"):
            acknowledge_v2_failure(
                session,
                operation_id=operation.operation_id,
                actor="operator",
                reason="Attempted incident acknowledgement",
            )


def test_even_a_forged_acknowledgement_cannot_clear_an_unknown_outbox() -> None:
    engine = create_engine("sqlite:///:memory:")
    SQLModel.metadata.create_all(engine)
    with Session(engine) as session:
        book, package = _seed(session)
        authorization = create_v2_manual_authorization(
            session,
            book=book,
            package=package,
            actor="operator",
            reason="Compared the exact edition",
        )
        uncertain = OperationLedger(
            operation_id="uncertain-operation",
            idempotency_key="uncertain-operation",
            operation_type="apply_metadata",
            book_key="calibre:99",
            policy_version="manifestation-v2",
            state="unknown",
            completed_at=utc_now(),
        )
        session.add(uncertain)
        session.add(
            OutboxEvent(
                event_id="uncertain-outbox",
                aggregate_id=uncertain.operation_id,
                event_type="operation.requested",
                status="failed",
            )
        )
        session.add(
            OperationIncidentAcknowledgement(
                operation_id=uncertain.operation_id,
                actor="forged",
                reason="This row must not bypass an uncertain operation",
            )
        )
        session.commit()

        with pytest.raises(ValueError, match="outbox"):
            queue_v2_operation(
                session,
                evidence_id=package.evidence_id,
                authorization_id=authorization.authorization_id,
                pilot=_pilot_guard(),
            )


@pytest.mark.parametrize("next_pilot_id", ["forged-open-pilot", "distinct-next-pilot"])
def test_even_a_forged_acknowledgement_cannot_clear_a_failed_open_pilot(next_pilot_id: str) -> None:
    engine = create_engine("sqlite:///:memory:")
    SQLModel.metadata.create_all(engine)
    with Session(engine) as session:
        book, package = _seed(session)
        authorization = create_v2_manual_authorization(
            session,
            book=book,
            package=package,
            actor="operator",
            reason="Compared the exact edition",
        )
        failed_pilot_id = "forged-open-pilot"
        failed_operation = OperationLedger(
            operation_id="forged-failed-operation",
            idempotency_key="forged-failed-operation",
            operation_type="apply_metadata",
            book_key="calibre:99",
            policy_version="manifestation-v2",
            pilot_id=failed_pilot_id,
            state="failed",
            completed_at=utc_now(),
        )
        session.add(
            PilotSession(
                pilot_id=failed_pilot_id,
                library_root_sha256=library_root_sha256("/library"),
                release_digest=f"sha256:{'a' * 64}",
                alembic_revision=expected_schema_revision(),
                max_operations=5,
                reserved_operations=1,
                state="open",
            )
        )
        session.add(failed_operation)
        session.add(
            OutboxEvent(
                event_id="forged-failed-outbox",
                aggregate_id=failed_operation.operation_id,
                event_type="operation.requested",
                status="failed",
            )
        )
        session.add(
            OperationIncidentAcknowledgement(
                operation_id=failed_operation.operation_id,
                actor="forged",
                reason="This row must not bypass a pilot that remains open",
            )
        )
        session.commit()

        with pytest.raises(ValueError, match="outbox"):
            queue_v2_operation(
                session,
                evidence_id=package.evidence_id,
                authorization_id=authorization.authorization_id,
                pilot=_pilot_guard(pilot_id=next_pilot_id),
            )

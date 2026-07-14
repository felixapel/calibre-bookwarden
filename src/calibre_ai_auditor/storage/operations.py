"""Transactional operation ledger and outbox primitives."""

from typing import Any
from uuid import uuid4

from sqlmodel import Session, select

from calibre_ai_auditor.storage.models import OperationLedger, OutboxEvent, utc_now

ALLOWED_TRANSITIONS: dict[str, frozenset[str]] = {
    "requested": frozenset({"claimed", "cancelled"}),
    # A claim is safe to release until the durable before/target evidence has
    # been recorded and the operation enters ``writing``.
    "claimed": frozenset({"requested", "writing", "failed", "cancelled"}),
    "writing": frozenset({"verifying", "restoring", "unknown"}),
    "verifying": frozenset({"succeeded", "restoring", "unknown"}),
    "restoring": frozenset({"restored", "restore_failed"}),
    "succeeded": frozenset(),
    "restored": frozenset(),
    "restore_failed": frozenset(),
    "unknown": frozenset(),
    "failed": frozenset(),
    "cancelled": frozenset(),
}


def create_operation(
    session: Session,
    *,
    idempotency_key: str,
    operation_type: str,
    book_key: str,
    requested_patch: dict[str, Any],
    run_id: str | None = None,
    authorization_id: str | None = None,
    calibre_book_id: int | None = None,
    verdict_hash: str | None = None,
    patch_hash: str | None = None,
    field_locks_hash: str | None = None,
    expected_before_metadata: dict[str, Any] | None = None,
    policy_version: str = "v1",
    evidence_id: str | None = None,
) -> OperationLedger:
    """Create one operation and outbox event, or return its prior retry."""
    existing = session.exec(select(OperationLedger).where(OperationLedger.idempotency_key == idempotency_key)).first()
    if existing is not None:
        return existing

    operation = OperationLedger(
        operation_id=str(uuid4()),
        idempotency_key=idempotency_key,
        operation_type=operation_type,
        book_key=book_key,
        run_id=run_id,
        requested_patch=requested_patch,
        authorization_id=authorization_id,
        calibre_book_id=calibre_book_id,
        verdict_hash=verdict_hash,
        patch_hash=patch_hash,
        field_locks_hash=field_locks_hash,
        expected_before_metadata=expected_before_metadata,
        policy_version=policy_version,
        evidence_id=evidence_id,
    )
    session.add(operation)
    session.add(
        OutboxEvent(
            event_id=str(uuid4()),
            aggregate_id=operation.operation_id,
            event_type="operation.requested",
            payload={
                "operation_id": operation.operation_id,
                "operation_type": operation_type,
                "book_key": book_key,
            },
        )
    )
    session.flush()
    return operation


def transition_operation(operation: OperationLedger, new_state: str, *, error: str | None = None) -> None:
    """Apply a valid state transition to a ledger row in the caller's transaction."""
    allowed = ALLOWED_TRANSITIONS.get(operation.state)
    if allowed is None or new_state not in allowed:
        raise ValueError(f"Invalid operation transition: {operation.state} -> {new_state}")

    operation.state = new_state
    operation.error = error
    operation.updated_at = utc_now()
    if not ALLOWED_TRANSITIONS[new_state]:
        operation.completed_at = operation.updated_at

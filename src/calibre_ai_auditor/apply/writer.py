"""Dedicated metadata writer and crash-reconciliation state transitions."""

import logging
from pathlib import Path
from typing import Any

from sqlmodel import Session, col, select

from calibre_ai_auditor.apply.engine import ApplyEngine
from calibre_ai_auditor.calibre.cli import CalibreCLI
from calibre_ai_auditor.storage.models import BookRecord, Change, OperationLedger, OutboxEvent, utc_now
from calibre_ai_auditor.storage.operations import transition_operation

logger = logging.getLogger(__name__)


def claim_next_operation(session: Session) -> str | None:
    """Claim one pending outbox operation using a row lock when supported."""
    event = session.exec(
        select(OutboxEvent)
        .where(OutboxEvent.status == "pending")
        .where(OutboxEvent.event_type == "operation.requested")
        .order_by(col(OutboxEvent.id))
        .with_for_update(skip_locked=True)
        .limit(1)
    ).first()
    if event is None:
        return None
    operation = session.exec(
        select(OperationLedger).where(OperationLedger.operation_id == event.aggregate_id).with_for_update()
    ).one()
    if operation.state != "requested":
        event.status = "published"
        event.published_at = utc_now()
        session.add(event)
        session.commit()
        return None
    transition_operation(operation, "claimed")
    event.status = "processing"
    event.attempts += 1
    session.add(operation)
    session.add(event)
    session.commit()
    return operation.operation_id


def reconcile_incomplete_operations(session: Session, cli: CalibreCLI) -> list[str]:
    """Resolve operations interrupted after an external write may have started."""
    reconciled: list[str] = []
    operations = session.exec(
        select(OperationLedger).where(col(OperationLedger.state).in_(("writing", "verifying", "restoring")))
    ).all()
    for operation in operations:
        book = session.exec(select(BookRecord).where(BookRecord.book_key == operation.book_key)).first()
        if (
            book is None
            or not book.calibre_book_id
            or operation.before_metadata is None
            or operation.target_metadata is None
        ):
            _mark_unknown(operation, "insufficient recovery evidence")
            session.add(operation)
            continue
        try:
            observed = cli.show_metadata(book.calibre_book_id)
            operation.observed_metadata = observed
            if _matches_patch(observed, operation.target_metadata):
                if operation.state == "writing":
                    transition_operation(operation, "verifying")
                if operation.state == "verifying":
                    transition_operation(operation, "succeeded")
                else:
                    _mark_unknown(operation, "target observed during restore")
                book.status = "applied"
                _finish_outbox(session, operation.operation_id, "published")
            elif _matches_patch(observed, operation.before_metadata):
                if operation.state in {"writing", "verifying"}:
                    transition_operation(operation, "restoring")
                transition_operation(operation, "restored")
                book.status = "suggest_fix"
                _finish_outbox(session, operation.operation_id, "failed", "previous state already restored")
            else:
                change = session.exec(
                    select(Change)
                    .where(Change.book_key == operation.book_key)
                    .where(Change.run_id == operation.run_id)
                    .order_by(col(Change.id).desc())
                ).first()
                if change is None:
                    _mark_unknown(operation, "partial state without restore point")
                else:
                    if operation.state in {"writing", "verifying"}:
                        transition_operation(operation, "restoring")
                    cli.set_metadata(book.calibre_book_id, Path(change.backup_opf_path))
                    restored = cli.show_metadata(book.calibre_book_id)
                    if _matches_patch(restored, operation.before_metadata):
                        transition_operation(operation, "restored")
                        book.status = "suggest_fix"
                        _finish_outbox(session, operation.operation_id, "failed", "partial state restored")
                    else:
                        transition_operation(operation, "restore_failed", error="restore verification failed")
                        book.status = "error"
                        _finish_outbox(session, operation.operation_id, "failed", "restore verification failed")
        except Exception as exc:
            _mark_unknown(operation, str(exc))
            book.status = "error"
        session.add(operation)
        session.add(book)
        reconciled.append(operation.operation_id)
    session.commit()
    return reconciled


class MetadataWriter:
    def __init__(self, cli: CalibreCLI, apply_engine: ApplyEngine) -> None:
        self.cli = cli
        self.apply_engine = apply_engine

    def process(self, session: Session, operation_id: str) -> OperationLedger:
        operation = session.exec(select(OperationLedger).where(OperationLedger.operation_id == operation_id)).one()
        if operation.state != "claimed":
            raise ValueError(f"Operation {operation_id} is not claimed")
        if operation.operation_type == "undo_change":
            return self._process_undo(session, operation)
        book = session.exec(select(BookRecord).where(BookRecord.book_key == operation.book_key)).one()
        if not book.calibre_book_id:
            raise ValueError(f"Operation {operation_id} has no Calibre book id")

        before = self.cli.show_metadata(book.calibre_book_id)
        operation.before_metadata = before
        operation.target_metadata = {**before, **operation.requested_patch}
        transition_operation(operation, "writing")
        session.add(operation)
        session.commit()

        try:
            change = self.apply_engine.apply_patch(session, book, operation.requested_patch)
        except Exception as exc:
            transition_operation(operation, "unknown", error=str(exc))
            session.add(operation)
            session.commit()
            return operation

        transition_operation(operation, "verifying")
        observed = self.cli.show_metadata(book.calibre_book_id)
        operation.observed_metadata = observed
        if _matches_patch(observed, operation.requested_patch):
            transition_operation(operation, "succeeded")
            book.status = "applied"
            _finish_outbox(session, operation.operation_id, "published")
        else:
            transition_operation(operation, "restoring")
            try:
                self.cli.set_metadata(book.calibre_book_id, Path(change.backup_opf_path))
                transition_operation(operation, "restored")
                book.status = "suggest_fix"
                _finish_outbox(session, operation.operation_id, "failed", "partial write restored")
            except Exception as exc:
                transition_operation(operation, "restore_failed", error=str(exc))
                book.status = "error"
                _finish_outbox(session, operation.operation_id, "failed", str(exc))
        session.add(operation)
        session.add(book)
        session.commit()
        return operation

    def _process_undo(self, session: Session, operation: OperationLedger) -> OperationLedger:
        change_id = operation.requested_patch.get("change_id")
        if not isinstance(change_id, int):
            raise ValueError(f"Undo operation {operation.operation_id} has no valid change id")
        change = session.get(Change, change_id)
        if change is None:
            raise ValueError(f"Change {change_id} not found")
        book = session.exec(select(BookRecord).where(BookRecord.book_key == change.book_key)).one()
        if not book.calibre_book_id:
            raise ValueError(f"Undo operation {operation.operation_id} has no Calibre book id")

        before = self.cli.show_metadata(book.calibre_book_id)
        current_backup = self.apply_engine.artifacts_dir / "undo" / operation.operation_id / "current.opf"
        current_backup.parent.mkdir(parents=True, exist_ok=True)
        self.cli.export_opf(book.calibre_book_id, current_backup)
        operation.before_metadata = before
        operation.target_metadata = change.before_metadata
        transition_operation(operation, "writing")
        session.add(operation)
        session.commit()

        self.cli.set_metadata(book.calibre_book_id, Path(change.backup_opf_path))
        transition_operation(operation, "verifying")
        observed = self.cli.show_metadata(book.calibre_book_id)
        operation.observed_metadata = observed
        if _matches_patch(observed, change.before_metadata):
            transition_operation(operation, "succeeded")
            change.status = "undone"
            book.status = "suggest_fix"
            _finish_outbox(session, operation.operation_id, "published")
        else:
            transition_operation(operation, "restoring")
            try:
                self.cli.set_metadata(book.calibre_book_id, current_backup)
                restored = self.cli.show_metadata(book.calibre_book_id)
                if not _matches_patch(restored, before):
                    raise RuntimeError("undo rollback verification failed")
                transition_operation(operation, "restored")
                book.status = "applied"
                _finish_outbox(session, operation.operation_id, "failed", "partial undo restored")
            except Exception as exc:
                transition_operation(operation, "restore_failed", error=str(exc))
                book.status = "error"
                _finish_outbox(session, operation.operation_id, "failed", str(exc))
        session.add(operation)
        session.add(change)
        session.add(book)
        session.commit()
        return operation


def _matches_patch(observed: dict[str, Any], patch: dict[str, Any]) -> bool:
    return all(observed.get(field) == expected for field, expected in patch.items())


def _finish_outbox(session: Session, operation_id: str, status: str, error: str | None = None) -> None:
    event = session.exec(select(OutboxEvent).where(OutboxEvent.aggregate_id == operation_id)).one()
    event.status = status
    event.last_error = error
    if status == "published":
        event.published_at = utc_now()
    session.add(event)


def _mark_unknown(operation: OperationLedger, error: str) -> None:
    if operation.state in {"writing", "verifying"}:
        transition_operation(operation, "unknown", error=error)
    elif operation.state == "restoring":
        transition_operation(operation, "restore_failed", error=error)

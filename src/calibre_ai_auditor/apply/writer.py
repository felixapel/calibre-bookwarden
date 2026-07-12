"""Dedicated metadata writer and crash-reconciliation state transitions."""

import hashlib
import logging
from datetime import UTC, datetime, timedelta
from pathlib import Path
from typing import Any
from uuid import uuid4

from sqlmodel import Session, col, select

from calibre_ai_auditor.apply.coordinator import validate_apply_operation
from calibre_ai_auditor.apply.engine import ApplyEngine
from calibre_ai_auditor.calibre.cli import CalibreCLI
from calibre_ai_auditor.storage.models import BookRecord, BookWriteLock, Change, OperationLedger, OutboxEvent, utc_now
from calibre_ai_auditor.storage.operations import transition_operation

logger = logging.getLogger(__name__)


def claim_next_operation(
    session: Session,
    *,
    lease_owner: str | None = None,
    lease_seconds: int = 60,
) -> str | None:
    """Claim one pending outbox operation using a row lock when supported."""
    while True:
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
            continue
        owner = lease_owner or str(uuid4())
        expires_at = utc_now() + timedelta(seconds=lease_seconds)
        # Lock the stable book row before inserting the lock row. This orders
        # first-claim races for the same book on PostgreSQL.
        session.exec(select(BookRecord).where(BookRecord.book_key == operation.book_key).with_for_update()).first()
        book_lock = session.get(BookWriteLock, operation.book_key, with_for_update=True)
        # Never steal an expired lock automatically: Calibre has no fencing
        # primitive, so only startup reconciliation may release a dead owner.
        if book_lock is not None:
            session.rollback()
            return None
        book_lock = BookWriteLock(
            book_key=operation.book_key,
            operation_id=operation.operation_id,
            lease_owner=owner,
            lease_expires_at=expires_at,
        )
        transition_operation(operation, "claimed")
        operation.lease_owner = owner
        operation.lease_expires_at = expires_at
        event.status = "processing"
        event.attempts += 1
        session.add(operation)
        session.add(event)
        session.add(book_lock)
        session.commit()
        return operation.operation_id


def reconcile_incomplete_operations(session: Session, cli: CalibreCLI) -> list[str]:
    """Resolve operations interrupted after an external write may have started."""
    reconciled: list[str] = []
    operations = session.exec(
        select(OperationLedger).where(col(OperationLedger.state).in_(("claimed", "writing", "verifying", "restoring")))
    ).all()
    for operation in operations:
        if operation.state == "claimed":
            if operation.lease_expires_at is not None and _lease_active(operation.lease_expires_at):
                continue
            transition_operation(operation, "requested")
            event = session.exec(select(OutboxEvent).where(OutboxEvent.aggregate_id == operation.operation_id)).one()
            event.status = "pending"
            event.last_error = None
            book_lock = session.get(BookWriteLock, operation.book_key)
            if book_lock is not None and book_lock.operation_id == operation.operation_id:
                session.delete(book_lock)
            operation.lease_owner = None
            operation.lease_expires_at = None
            session.add(operation)
            session.add(event)
            reconciled.append(operation.operation_id)
            session.commit()
            continue
        if operation.operation_type == "undo_change":
            _reconcile_undo(session, cli, operation)
            reconciled.append(operation.operation_id)
            session.commit()
            continue
        book = session.exec(select(BookRecord).where(BookRecord.book_key == operation.book_key)).first()
        change = session.exec(select(Change).where(Change.operation_id == operation.operation_id)).first()
        if (
            book is None
            or not book.calibre_book_id
            or operation.before_metadata is None
            or operation.target_metadata is None
        ):
            _mark_unknown(operation, "insufficient recovery evidence")
            _finish_outbox(session, operation.operation_id, "failed", operation.error)
            session.add(operation)
            if book is not None:
                book.status = "error"
                session.add(book)
            reconciled.append(operation.operation_id)
            session.commit()
            continue
        try:
            observed = cli.show_metadata(book.calibre_book_id)
            operation.observed_metadata = observed
            if _matches_patch(observed, operation.target_metadata):
                if operation.state == "writing":
                    transition_operation(operation, "verifying")
                if operation.state == "verifying":
                    transition_operation(operation, "succeeded")
                    if change is not None:
                        change.status = "applied"
                    book.status = "applied"
                    _finish_outbox(session, operation.operation_id, "published")
                else:
                    _mark_unknown(operation, "target observed during restore")
                    book.status = "error"
                    _finish_outbox(session, operation.operation_id, "failed", operation.error)
            elif _matches_patch(observed, operation.before_metadata):
                if operation.state in {"writing", "verifying"}:
                    transition_operation(operation, "restoring")
                transition_operation(operation, "restored")
                if change is not None:
                    change.status = "failed_rolled_back"
                book.status = "suggest_fix"
                _finish_outbox(session, operation.operation_id, "failed", "previous state already restored")
            else:
                if change is None:
                    _mark_unknown(operation, "partial state without restore point")
                    book.status = "error"
                    _finish_outbox(session, operation.operation_id, "failed", operation.error)
                else:
                    if operation.state in {"writing", "verifying"}:
                        transition_operation(operation, "restoring")
                    _require_artifact(change.backup_opf_path, change.backup_opf_sha256)
                    cli.set_metadata(book.calibre_book_id, Path(change.backup_opf_path))
                    restored = cli.show_metadata(book.calibre_book_id)
                    if _matches_patch(restored, operation.before_metadata):
                        transition_operation(operation, "restored")
                        change.status = "failed_rolled_back"
                        book.status = "suggest_fix"
                        _finish_outbox(session, operation.operation_id, "failed", "partial state restored")
                    else:
                        transition_operation(operation, "restore_failed", error="restore verification failed")
                        change.status = "failed_rollback_failed"
                        book.status = "error"
                        _finish_outbox(session, operation.operation_id, "failed", "restore verification failed")
        except Exception as exc:
            _mark_unknown(operation, str(exc))
            book.status = "error"
            if change is not None:
                change.status = "failed_rollback_failed"
            _finish_outbox(session, operation.operation_id, "failed", str(exc))
        session.add(operation)
        session.add(book)
        if change is not None:
            session.add(change)
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
        try:
            validate_apply_operation(session, operation, book)
        except (ValueError, TypeError) as exc:
            transition_operation(operation, "failed", error=str(exc))
            book.status = "error"
            _finish_outbox(session, operation.operation_id, "failed", str(exc))
            session.add(operation)
            session.add(book)
            session.commit()
            return operation

        before = self.cli.show_metadata(book.calibre_book_id)
        if operation.expected_before_metadata is None or not _matches_patch(before, operation.expected_before_metadata):
            transition_operation(operation, "failed", error="live metadata changed after authorization")
            book.status = "suggest_fix"
            _finish_outbox(session, operation.operation_id, "failed", operation.error)
            session.add(operation)
            session.add(book)
            session.commit()
            return operation
        operation.before_metadata = before
        operation.target_metadata = {**before, **operation.requested_patch}
        transition_operation(operation, "writing")
        session.add(operation)
        session.commit()

        try:
            change = self.apply_engine.apply_patch(
                session,
                book,
                operation.requested_patch,
                operation_id=operation.operation_id,
                live_before=before,
            )
        except Exception as exc:
            failed_change = session.exec(select(Change).where(Change.operation_id == operation.operation_id)).first()
            if failed_change is not None and failed_change.status == "failed_rolled_back":
                transition_operation(operation, "restoring")
                transition_operation(operation, "restored", error=str(exc))
                book.status = "suggest_fix"
                _finish_outbox(session, operation.operation_id, "failed", str(exc))
            elif failed_change is not None and failed_change.status == "failed_rollback_failed":
                transition_operation(operation, "restoring")
                transition_operation(operation, "restore_failed", error=str(exc))
                book.status = "error"
                _finish_outbox(session, operation.operation_id, "failed", str(exc))
            else:
                transition_operation(operation, "unknown", error=str(exc))
                book.status = "error"
                _finish_outbox(session, operation.operation_id, "failed", str(exc))
            session.add(operation)
            session.add(book)
            session.commit()
            return operation

        operation.change_id = change.id
        operation.rollback_opf_path = change.backup_opf_path
        operation.rollback_opf_sha256 = change.backup_opf_sha256

        transition_operation(operation, "verifying")
        observed = self.cli.show_metadata(book.calibre_book_id)
        operation.observed_metadata = observed
        if _matches_patch(observed, operation.target_metadata):
            transition_operation(operation, "succeeded")
            book.status = "applied"
            _finish_outbox(session, operation.operation_id, "published")
        else:
            transition_operation(operation, "restoring")
            try:
                _require_artifact(change.backup_opf_path, change.backup_opf_sha256)
                self.cli.set_metadata(book.calibre_book_id, Path(change.backup_opf_path))
                restored = self.cli.show_metadata(book.calibre_book_id)
                if not _matches_patch(restored, before):
                    raise RuntimeError("restore verification failed")
                transition_operation(operation, "restored")
                change.status = "failed_rolled_back"
                book.status = "suggest_fix"
                _finish_outbox(session, operation.operation_id, "failed", "partial write restored")
            except Exception as exc:
                transition_operation(operation, "restore_failed", error=str(exc))
                change.status = "failed_rollback_failed"
                book.status = "error"
                _finish_outbox(session, operation.operation_id, "failed", str(exc))
        session.add(operation)
        session.add(change)
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
        if (
            not book.calibre_book_id
            or book.calibre_book_id != operation.calibre_book_id
            or book.run_id != operation.run_id
            or change.status != "applied"
        ):
            transition_operation(operation, "failed", error="undo identity or change status is stale")
            book.status = "error"
            _finish_outbox(session, operation.operation_id, "failed", operation.error)
            session.add(operation)
            session.add(book)
            session.commit()
            return operation
        _require_artifact(change.backup_opf_path, change.backup_opf_sha256)

        before = self.cli.show_metadata(book.calibre_book_id)
        if operation.expected_before_metadata is None or not _matches_patch(before, operation.expected_before_metadata):
            transition_operation(operation, "failed", error="live metadata changed after undo was queued")
            book.status = "applied"
            _finish_outbox(session, operation.operation_id, "failed", operation.error)
            session.add(operation)
            session.add(book)
            session.commit()
            return operation
        current_backup = self.apply_engine.artifacts_dir / "undo" / operation.operation_id / "current.opf"
        current_backup.parent.mkdir(parents=True, exist_ok=True)
        self.cli.export_opf(book.calibre_book_id, current_backup)
        operation.rollback_opf_path = str(current_backup)
        operation.rollback_opf_sha256 = _sha256_file(current_backup)
        operation.change_id = change.id
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
                _require_artifact(str(current_backup), operation.rollback_opf_sha256)
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


def fail_operation(session: Session, operation_id: str, error: str) -> None:
    """Terminalize a validation failure or preserve an uncertain external-write incident."""
    operation = session.exec(select(OperationLedger).where(OperationLedger.operation_id == operation_id)).one()
    if operation.state == "claimed":
        transition_operation(operation, "failed", error=error)
    elif operation.state in {"writing", "verifying"}:
        transition_operation(operation, "unknown", error=error)
    elif operation.state == "restoring":
        transition_operation(operation, "restore_failed", error=error)
    book = session.exec(select(BookRecord).where(BookRecord.book_key == operation.book_key)).first()
    if book is not None:
        book.status = "error"
        session.add(book)
    _finish_outbox(session, operation.operation_id, "failed", error)
    session.add(operation)
    session.commit()


def _matches_patch(observed: dict[str, Any], patch: dict[str, Any]) -> bool:
    return all(observed.get(field) == expected for field, expected in patch.items())


def _lease_active(expires_at: datetime) -> bool:
    """Compare timestamps consistently across SQLite and timezone-aware PostgreSQL drivers."""
    if expires_at.tzinfo is None:
        return expires_at > utc_now().replace(tzinfo=None)
    return expires_at.astimezone(UTC) > utc_now()


def _sha256_file(path: Path) -> str:
    return hashlib.sha256(path.read_bytes()).hexdigest()


def _require_artifact(path_value: str, expected_sha256: str | None) -> Path:
    path = Path(path_value)
    if expected_sha256 is None or not path.is_file() or _sha256_file(path) != expected_sha256:
        raise RuntimeError("restore artifact is missing or has been tampered with")
    return path


def _reconcile_undo(session: Session, cli: CalibreCLI, operation: OperationLedger) -> None:
    """Reconcile undo using its own target and pre-undo rollback artifact."""
    change_id = operation.change_id or operation.requested_patch.get("change_id")
    change = session.get(Change, change_id) if isinstance(change_id, int) else None
    book = session.exec(select(BookRecord).where(BookRecord.book_key == operation.book_key)).first()
    if (
        change is None
        or book is None
        or not book.calibre_book_id
        or operation.before_metadata is None
        or operation.target_metadata is None
    ):
        _mark_unknown(operation, "insufficient undo recovery evidence")
        session.add(operation)
        return
    try:
        observed = cli.show_metadata(book.calibre_book_id)
        operation.observed_metadata = observed
        if _matches_patch(observed, operation.target_metadata):
            if operation.state == "writing":
                transition_operation(operation, "verifying")
            if operation.state == "verifying":
                transition_operation(operation, "succeeded")
                change.status = "undone"
                book.status = "suggest_fix"
                _finish_outbox(session, operation.operation_id, "published")
            else:
                raise RuntimeError("undo target observed while restore was in progress")
        elif _matches_patch(observed, operation.before_metadata):
            if operation.state in {"writing", "verifying"}:
                transition_operation(operation, "restoring")
            transition_operation(operation, "restored")
            book.status = "applied"
            _finish_outbox(session, operation.operation_id, "failed", "pre-undo state preserved")
        else:
            if operation.state in {"writing", "verifying"}:
                transition_operation(operation, "restoring")
            if operation.rollback_opf_path is None:
                raise RuntimeError("undo rollback artifact is unavailable")
            rollback = _require_artifact(operation.rollback_opf_path, operation.rollback_opf_sha256)
            cli.set_metadata(book.calibre_book_id, rollback)
            restored = cli.show_metadata(book.calibre_book_id)
            if not _matches_patch(restored, operation.before_metadata):
                raise RuntimeError("undo rollback verification failed")
            transition_operation(operation, "restored")
            book.status = "applied"
            _finish_outbox(session, operation.operation_id, "failed", "partial undo restored")
    except Exception as exc:
        _mark_unknown(operation, str(exc))
        book.status = "error"
        _finish_outbox(session, operation.operation_id, "failed", str(exc))
    session.add(operation)
    session.add(change)
    session.add(book)


def _finish_outbox(session: Session, operation_id: str, status: str, error: str | None = None) -> None:
    event = session.exec(select(OutboxEvent).where(OutboxEvent.aggregate_id == operation_id)).one()
    event.status = status
    event.last_error = error
    if status == "published":
        event.published_at = utc_now()
    session.add(event)
    operation = session.exec(select(OperationLedger).where(OperationLedger.operation_id == operation_id)).one()
    book_lock = session.get(BookWriteLock, operation.book_key)
    if book_lock is not None and book_lock.operation_id == operation_id:
        session.delete(book_lock)


def _mark_unknown(operation: OperationLedger, error: str) -> None:
    if operation.state in {"writing", "verifying"}:
        transition_operation(operation, "unknown", error=error)
    elif operation.state == "restoring":
        transition_operation(operation, "restore_failed", error=error)

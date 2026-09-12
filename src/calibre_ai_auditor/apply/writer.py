"""Dedicated metadata writer and crash-reconciliation state transitions."""

import hashlib
import logging
import os
from datetime import UTC, datetime, timedelta
from pathlib import Path
from typing import Any
from uuid import uuid4

from sqlmodel import Session, col, select

from calibre_ai_auditor.apply.artifacts import (
    MAX_COVER_BYTES,
    MAX_OPF_BYTES,
    bind_exported_artifact,
    set_cover_from_artifact,
    set_metadata_from_artifact,
    verify_artifact,
)
from calibre_ai_auditor.apply.coordinator import PilotGuard, load_v2_package, validate_apply_operation
from calibre_ai_auditor.apply.engine import ApplyEngine
from calibre_ai_auditor.calibre.cli import VOLATILE_METADATA_FIELDS, CalibreCLI
from calibre_ai_auditor.security.files import ensure_secure_directory, sha256_file_beneath
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
        try:
            session.commit()
        except Exception as exc:
            # Concurrent claim won the race (notably on SQLite where
            # FOR UPDATE is a no-op and duplicate PK raises IntegrityError).
            session.rollback()
            logger.info("Concurrent claim for %s lost race: %s", operation.book_key, exc)
            return None
        return operation.operation_id


def reconcile_incomplete_operations(
    session: Session,
    cli: CalibreCLI,
    artifacts_dir: Path | None = None,
) -> list[str]:
    """Resolve operations interrupted after an external write may have started."""
    artifact_root = Path(artifacts_dir).absolute() if artifacts_dir is not None else None
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
            _reconcile_undo(session, cli, operation, artifact_root)
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
            if operation.policy_version == "manifestation-v2":
                try:
                    _verify_v2_live_files(session, cli, operation, observed, allow_relocation=True)
                    _align_verified_calibre_paths(operation, observed)
                except (OSError, ValueError) as exc:
                    if change is None:
                        _mark_unknown(operation, f"V2 ebook snapshot changed: {exc}")
                    else:
                        if operation.state in {"writing", "verifying"}:
                            transition_operation(operation, "restoring")
                        _restore_change(cli, book.calibre_book_id, change, artifact_root)
                        restored = cli.show_metadata(book.calibre_book_id)
                        if not _matches_patch(restored, operation.before_metadata):
                            raise RuntimeError("restore verification failed after ebook snapshot change") from exc
                        transition_operation(operation, "restored", error=f"V2 ebook snapshot changed: {exc}")
                        change.status = "failed_rolled_back"
                    book.status = "error"
                    _finish_outbox(session, operation.operation_id, "failed", operation.error)
                    session.add(operation)
                    session.add(book)
                    if change is not None:
                        session.add(change)
                    reconciled.append(operation.operation_id)
                    session.commit()
                    continue
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
                    _restore_change(cli, book.calibre_book_id, change, artifact_root)
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
    def __init__(
        self,
        cli: CalibreCLI,
        apply_engine: ApplyEngine,
        *,
        pilot: PilotGuard | None = None,
    ) -> None:
        self.cli = cli
        self.apply_engine = apply_engine
        self.pilot = pilot

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
            validate_apply_operation(session, operation, book, pilot=self.pilot)
        except (ValueError, TypeError) as exc:
            transition_operation(operation, "failed", error=str(exc))
            book.status = "error"
            _finish_outbox(session, operation.operation_id, "failed", str(exc))
            session.add(operation)
            session.add(book)
            session.commit()
            return operation

        before = self.cli.show_metadata(book.calibre_book_id)
        if operation.policy_version == "manifestation-v2":
            try:
                _verify_v2_live_files(session, self.cli, operation, before)
            except (OSError, ValueError) as exc:
                transition_operation(
                    operation,
                    "failed",
                    error=f"live ebook snapshot changed after authorization: {exc}",
                )
                book.status = "error"
                _finish_outbox(session, operation.operation_id, "failed", operation.error)
                session.add(operation)
                session.add(book)
                session.commit()
                return operation
        if operation.expected_before_metadata is None or not _matches_patch(before, operation.expected_before_metadata):
            transition_operation(operation, "failed", error="live metadata changed after authorization")
            book.status = "suggest_fix"
            _finish_outbox(session, operation.operation_id, "failed", operation.error)
            session.add(operation)
            session.add(book)
            session.commit()
            return operation
        operation.before_metadata = before
        target = dict(before)
        if "edition_statement" in operation.requested_patch:
            target.pop("#edition", None)
            target.pop("edition_statement", None)
        target.update(operation.requested_patch)
        operation.target_metadata = target
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
        if operation.policy_version == "manifestation-v2":
            try:
                post_write = self.cli.show_metadata(book.calibre_book_id)
                _verify_v2_live_files(session, self.cli, operation, post_write, allow_relocation=True)
                _align_verified_calibre_paths(operation, post_write)
            except (OSError, ValueError) as exc:
                transition_operation(operation, "restoring")
                try:
                    _restore_change(
                        self.cli,
                        book.calibre_book_id,
                        change,
                        self.apply_engine.artifacts_dir,
                    )
                    restored = self.cli.show_metadata(book.calibre_book_id)
                    if not _matches_patch(restored, before):
                        raise RuntimeError("restore verification failed after ebook snapshot change")
                    transition_operation(operation, "restored", error=f"ebook changed during metadata write: {exc}")
                    change.status = "failed_rolled_back"
                except Exception as restore_exc:
                    transition_operation(operation, "restore_failed", error=str(restore_exc))
                    change.status = "failed_rollback_failed"
                book.status = "error"
                _finish_outbox(session, operation.operation_id, "failed", operation.error)
                session.add(operation)
                session.add(change)
                session.add(book)
                session.commit()
                return operation
        observed = self.cli.show_metadata(book.calibre_book_id)
        operation.observed_metadata = observed
        if _matches_patch(observed, operation.target_metadata):
            transition_operation(operation, "succeeded")
            book.status = "applied"
            _finish_outbox(session, operation.operation_id, "published")
        else:
            transition_operation(operation, "restoring")
            try:
                _restore_change(
                    self.cli,
                    book.calibre_book_id,
                    change,
                    self.apply_engine.artifacts_dir,
                )
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
        _require_artifact(
            self.apply_engine.artifacts_dir,
            change.backup_opf_path,
            change.backup_opf_sha256,
            max_bytes=MAX_OPF_BYTES,
        )

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
        ensure_secure_directory(current_backup.parent)
        exported_opf_sha256 = self.cli.export_opf(book.calibre_book_id, current_backup)
        operation.rollback_opf_path = str(current_backup)
        operation.rollback_opf_sha256 = bind_exported_artifact(
            self.apply_engine.artifacts_dir,
            current_backup,
            exported_opf_sha256,
            max_bytes=MAX_OPF_BYTES,
        )
        if change.backup_cover_path:
            current_cover = current_backup.with_suffix(".cover")
            exported_cover_sha256 = self.cli.export_cover(book.calibre_book_id, current_cover)
            if not exported_cover_sha256:
                raise RuntimeError("current cover could not be backed up before undo")
            operation.rollback_cover_path = str(current_cover)
            operation.rollback_cover_sha256 = bind_exported_artifact(
                self.apply_engine.artifacts_dir,
                current_cover,
                exported_cover_sha256,
                max_bytes=MAX_COVER_BYTES,
            )
        if change.before_custom:
            operation.rollback_custom = {
                column: str(before.get(column) or before.get("edition_statement") or "")
                for column in change.before_custom
            }
        operation.change_id = change.id
        operation.before_metadata = before
        operation.target_metadata = change.before_metadata
        transition_operation(operation, "writing")
        session.add(operation)
        session.commit()

        _restore_change(
            self.cli,
            book.calibre_book_id,
            change,
            self.apply_engine.artifacts_dir,
        )
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
                _require_artifact(
                    self.apply_engine.artifacts_dir,
                    str(current_backup),
                    operation.rollback_opf_sha256,
                    max_bytes=MAX_OPF_BYTES,
                )
                _restore_operation_rollback(
                    self.cli,
                    book.calibre_book_id,
                    operation,
                    self.apply_engine.artifacts_dir,
                )
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
    for field, expected in patch.items():
        if field in VOLATILE_METADATA_FIELDS:
            continue
        if field == "edition_statement":
            if str(observed.get("#edition") or observed.get("edition_statement") or "") != str(expected or ""):
                return False
        elif field == "cover" and isinstance(expected, dict):
            cover_path = observed.get("cover")
            if not isinstance(cover_path, str) or not Path(cover_path).is_file():
                return False
            if _sha256_file(Path(cover_path)) != expected.get("artifact_sha256"):
                return False
        elif observed.get(field) != expected:
            return False
    return True


def _lease_active(expires_at: datetime) -> bool:
    """Compare timestamps consistently across SQLite and timezone-aware PostgreSQL drivers."""
    if expires_at.tzinfo is None:
        return expires_at > utc_now().replace(tzinfo=None)
    return expires_at.astimezone(UTC) > utc_now()


def _sha256_file(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as stream:
        for chunk in iter(lambda: stream.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def _metadata_file_paths(metadata: dict[str, Any]) -> list[str]:
    raw = metadata.get("formats")
    if isinstance(raw, str):
        return [item.strip() for item in raw.split(",") if item.strip()]
    if not isinstance(raw, list):
        return []
    paths: list[str] = []
    for item in raw:
        if isinstance(item, str) and item.strip():
            paths.append(item.strip())
        elif isinstance(item, dict) and isinstance(item.get("path"), str):
            paths.append(item["path"].strip())
    return paths


def _secure_library_file_sha256(path_value: str, library_root: Path) -> str:
    path = Path(path_value)
    if not path.is_absolute():
        raise ValueError("ebook path is not absolute")
    return sha256_file_beneath(library_root, path)


def _verify_v2_live_files(
    session: Session,
    cli: CalibreCLI,
    operation: OperationLedger,
    live_metadata: dict[str, Any],
    *,
    allow_relocation: bool = False,
) -> None:
    if not operation.evidence_id:
        raise ValueError("V2 operation has no evidence package")
    _stored, package = load_v2_package(session, operation.evidence_id)
    library_path = getattr(cli, "library_path", None)
    if not isinstance(library_path, (str, Path)):
        raise ValueError("V2 writer requires an explicit configured Calibre library path")
    configured_root = Path(os.path.normpath(os.path.abspath(library_path)))
    sealed_root = (
        Path(os.path.normpath(os.path.abspath(package.snapshot.library_root)))
        if package.snapshot.library_root is not None
        else None
    )
    if sealed_root != configured_root:
        raise ValueError("configured Calibre library differs from the sealed snapshot root")
    live_paths = _metadata_file_paths(live_metadata)
    if len(live_paths) != len(set(live_paths)):
        raise ValueError("live Calibre format membership contains duplicate paths")
    expected_hashes = {item.path: item.sha256 for item in package.formats}
    if list(expected_hashes) != package.snapshot.files:
        raise ValueError("sealed format evidence is incomplete or reordered")
    if not allow_relocation:
        if live_paths != package.snapshot.files:
            raise ValueError("live Calibre format membership differs from the sealed snapshot")
        for path in live_paths:
            if _secure_library_file_sha256(path, configured_root) != expected_hashes[path]:
                raise ValueError(f"ebook content hash differs for {Path(path).name}")
        return

    expected_formats = sorted((item.format.casefold(), item.sha256) for item in package.formats)
    live_formats = sorted(
        (Path(path).suffix.removeprefix(".").casefold(), _secure_library_file_sha256(path, configured_root))
        for path in live_paths
    )
    if live_formats != expected_formats:
        raise ValueError("live Calibre formats or content differ from the sealed snapshot")


def _align_verified_calibre_paths(operation: OperationLedger, observed: dict[str, Any]) -> None:
    """Accept Calibre-managed relocation only after format and hash verification."""
    if operation.target_metadata is None:
        raise ValueError("operation target metadata is unavailable")
    target = dict(operation.target_metadata)
    for field in ("formats", "path"):
        if field in target and field in observed:
            target[field] = observed[field]
    operation.target_metadata = target


def _require_artifact(
    artifacts_root: Path | None,
    path_value: str,
    expected_sha256: str | None,
    *,
    max_bytes: int,
) -> Path:
    if artifacts_root is None:
        raise RuntimeError("recovery requires the configured artifact root")
    path = Path(path_value)
    verify_artifact(artifacts_root, path, expected_sha256, max_bytes=max_bytes)
    return path


def _restore_change(
    cli: CalibreCLI,
    book_id: int,
    change: Change,
    artifacts_root: Path | None,
) -> None:
    if artifacts_root is None:
        raise RuntimeError("recovery requires the configured artifact root")
    opf = Path(change.backup_opf_path)
    set_metadata_from_artifact(cli, book_id, artifacts_root, opf, change.backup_opf_sha256)
    for column, value in (change.before_custom or {}).items():
        cli.set_custom(book_id, column, str(value))
    if change.backup_cover_path:
        cover = Path(change.backup_cover_path)
        set_cover_from_artifact(cli, book_id, artifacts_root, cover, change.backup_cover_sha256)


def _restore_operation_rollback(
    cli: CalibreCLI,
    book_id: int,
    operation: OperationLedger,
    artifacts_root: Path | None,
) -> None:
    if operation.rollback_opf_path is None:
        raise RuntimeError("operation rollback OPF is unavailable")
    if artifacts_root is None:
        raise RuntimeError("recovery requires the configured artifact root")
    opf = Path(operation.rollback_opf_path)
    set_metadata_from_artifact(cli, book_id, artifacts_root, opf, operation.rollback_opf_sha256)
    for column, value in (operation.rollback_custom or {}).items():
        cli.set_custom(book_id, column, str(value))
    if operation.rollback_cover_path:
        cover = Path(operation.rollback_cover_path)
        set_cover_from_artifact(cli, book_id, artifacts_root, cover, operation.rollback_cover_sha256)


def _reconcile_undo(
    session: Session,
    cli: CalibreCLI,
    operation: OperationLedger,
    artifacts_root: Path | None,
) -> None:
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
        if book is not None:
            book.status = "error"
            session.add(book)
        _finish_outbox(session, operation.operation_id, "failed", operation.error)
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
            _restore_operation_rollback(cli, book.calibre_book_id, operation, artifacts_root)
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

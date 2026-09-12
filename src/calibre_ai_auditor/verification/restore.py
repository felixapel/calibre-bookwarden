"""Conservative auto-apply engine for v1.0.

Key differences from v0.9's ApplyEngine:
  1. Every apply creates a per-book RESTORE POINT (not just an OPF backup).
     The restore point holds: original OPF, original cover (if modified),
     original file copy, and a JSON snapshot of the before/after patch.
  2. Restore points have a retention target (default 30 days) and expose explicit cleanup.
  3. Conservative gate runs BEFORE any write:
     - Auto-apply eligible (per BookVerdict.auto_apply_eligible)
     - No high-risk flags in the verdict
     - Per-book restore point successfully written
     - User has explicitly opted in for this run
  4. Undo is per-book AND bulk by date/run.

Restore points live at `<artifacts_dir>/restore/<run_id>/<book_key>/` and
contain:
  - original.opf         (the OPF as it was before)
  - original.epub|.pdf   (a hardlink or copy of the original file when writable)
  - original.cover.jpg   (if cover was modified)
  - before.json          (snapshot of full before-metadata)
  - after.json           (what we wrote)
  - restore.json         (metadata for bulk-restore: run_id, book_key, applied_at)
"""

from __future__ import annotations

import hashlib
import json
import logging
import os
import re
import shutil
from dataclasses import dataclass
from datetime import UTC, datetime, timedelta
from pathlib import Path
from typing import Any, cast
from uuid import UUID, uuid4

from calibre_ai_auditor.security.files import (
    copy_file_replacing_beneath,
    ensure_secure_directory,
    replace_bytes_beneath,
)
from calibre_ai_auditor.verification.verdict import HIGH_RISK_FLAGS, BookVerdict

logger = logging.getLogger(__name__)

QUARANTINE_ROOT_NAME = ".retention-quarantine"
QUARANTINE_JOURNAL_NAME = "transaction.json"
QUARANTINE_PAYLOAD_NAME = "payload"
QUARANTINE_STAGING_PREFIX = ".staging-"
QUARANTINE_STATES = {"prepared", "quarantining", "quarantined", "deleting", "deleted"}


def _fsync_directory(path: Path) -> None:
    o_directory = getattr(os, "O_DIRECTORY", None)
    if o_directory is None:
        return
    descriptor = os.open(path, os.O_RDONLY | o_directory)
    try:
        os.fsync(descriptor)
    finally:
        os.close(descriptor)


def _write_json_atomically(path: Path, payload: dict[str, Any]) -> None:
    temporary = path.with_name(f".{path.name}.tmp-{uuid4().hex}")
    with temporary.open("w") as stream:
        json.dump(payload, stream, indent=2, sort_keys=True)
        stream.write("\n")
        stream.flush()
        os.fsync(stream.fileno())
    os.replace(temporary, path)
    _fsync_directory(path.parent)


@dataclass
class RestorePoint:
    """A single book-level restore point on disk."""

    path: Path
    run_id: str
    book_key: str
    calibre_book_id: int | None
    applied_at: datetime
    fields_changed: list[str]
    restore_point_ttl: timedelta = timedelta(days=30)

    def is_expired(self, now: datetime | None = None) -> bool:
        if now is None:
            now = datetime.now(UTC)
        return (now - self.applied_at) > self.restore_point_ttl

    def to_dict(self) -> dict[str, Any]:
        return {
            "path": str(self.path),
            "run_id": self.run_id,
            "book_key": self.book_key,
            "calibre_book_id": self.calibre_book_id,
            "applied_at": self.applied_at.isoformat(),
            "fields_changed": self.fields_changed,
            "ttl_seconds": int(self.restore_point_ttl.total_seconds()),
            "expired": self.is_expired(),
        }


@dataclass(frozen=True)
class RestoreDeletionCandidate:
    """Immutable identity for one restore directory approved for deletion."""

    path: Path
    manifest_sha256: str
    device: int
    inode: int


class RestorePointStore:
    """Manages on-disk restore points under <artifacts_dir>/restore/."""

    def __init__(self, artifacts_dir: Path, default_ttl: timedelta = timedelta(days=30)):
        self.artifacts_dir = Path(artifacts_dir).absolute()
        self.restore_root = self.artifacts_dir / "restore"
        ensure_secure_directory(self.restore_root)
        self.default_ttl = default_ttl

    def path_for(self, run_id: str, book_key: str) -> Path:
        safe_key = re.sub(r"[^A-Za-z0-9._-]", "_", book_key)
        safe_run = re.sub(r"[^A-Za-z0-9._-]", "_", run_id)
        if safe_key in {"", ".", ".."} or safe_run in {"", ".", ".."}:
            raise ValueError("restore point identifiers do not produce a safe path")
        return self.restore_root / safe_run / safe_key

    def create(
        self,
        *,
        run_id: str,
        book_key: str,
        calibre_book_id: int | None,
        before_metadata: dict[str, Any],
        after_metadata: dict[str, Any],
        fields_changed: list[str],
        original_file: Path | None = None,
        original_cover: Path | None = None,
        original_opf: Path | None = None,
    ) -> RestorePoint:
        """Create a restore point on disk. Returns its handle.

        original_opf: if provided, will be copied into the restore dir.
        original_file: hardlink or copy of the original book file.
        original_cover: copy of the original cover (if cover was modified).
        """
        rp_dir = ensure_secure_directory(self.path_for(run_id, book_key))

        # 1. Original OPF (always; this is the cheapest, most reliable restore)
        if original_opf:
            target = rp_dir / "original.opf"
            try:
                copy_file_replacing_beneath(
                    original_opf.parent,
                    original_opf,
                    self.artifacts_dir,
                    target,
                    max_bytes=32 * 1024 * 1024,
                )
            except OSError as e:
                logger.warning("Failed to copy original OPF: %s", e)

        # 2. Original book file (only if small enough; otherwise hardlink)
        if original_file:
            target = rp_dir / ("original" + original_file.suffix)
            try:
                copy_file_replacing_beneath(
                    original_file.parent,
                    original_file,
                    self.artifacts_dir,
                    target,
                )
            except OSError as e:
                logger.warning("Failed to backup original file: %s", e)

        # 3. Original cover (if changed)
        if original_cover:
            target = rp_dir / ("original.cover" + original_cover.suffix)
            try:
                copy_file_replacing_beneath(
                    original_cover.parent,
                    original_cover,
                    self.artifacts_dir,
                    target,
                    max_bytes=20 * 1024 * 1024,
                )
            except OSError as e:
                logger.warning("Failed to backup original cover: %s", e)

        # 4. Metadata snapshots
        now = datetime.now(UTC)
        replace_bytes_beneath(
            self.artifacts_dir,
            rp_dir / "before.json",
            json.dumps(before_metadata, indent=2, default=str).encode(),
        )
        replace_bytes_beneath(
            self.artifacts_dir,
            rp_dir / "after.json",
            json.dumps(after_metadata, indent=2, default=str).encode(),
        )
        replace_bytes_beneath(
            self.artifacts_dir,
            rp_dir / "restore.json",
            json.dumps(
                {
                    "run_id": run_id,
                    "book_key": book_key,
                    "calibre_book_id": calibre_book_id,
                    "applied_at": now.isoformat(),
                    "fields_changed": fields_changed,
                    "ttl_seconds": int(self.default_ttl.total_seconds()),
                },
                indent=2,
            ).encode(),
        )

        return RestorePoint(
            path=rp_dir,
            run_id=run_id,
            book_key=book_key,
            calibre_book_id=calibre_book_id,
            applied_at=now,
            fields_changed=fields_changed,
            restore_point_ttl=self.default_ttl,
        )

    def cleanup_expired(self, now: datetime | None = None) -> int:
        """Remove expired restore points. Returns count deleted."""
        if now is None:
            now = datetime.now(UTC)
        deleted = 0
        if not self.restore_root.exists():
            return 0
        for run_dir in self.restore_root.iterdir():
            if not run_dir.is_dir():
                continue
            for book_dir in run_dir.iterdir():
                if not book_dir.is_dir():
                    continue
                meta_file = book_dir / "restore.json"
                if not meta_file.exists():
                    continue
                try:
                    meta = json.loads(meta_file.read_text())
                    applied_at = datetime.fromisoformat(meta["applied_at"])
                    if applied_at.tzinfo is None:
                        # Legacy points written without offset: assume UTC
                        # rather than crashing naive-vs-aware subtraction.
                        applied_at = applied_at.replace(tzinfo=UTC)
                    ttl = timedelta(seconds=meta.get("ttl_seconds", int(self.default_ttl.total_seconds())))
                    if (now - applied_at) > ttl:
                        if book_dir.is_symlink():
                            logger.warning("Refusing to delete symlinked restore point: %s", book_dir)
                            continue
                        shutil.rmtree(book_dir)
                        deleted += 1
                        logger.info("Cleaned up expired restore point: %s", book_dir)
                except (json.JSONDecodeError, KeyError, ValueError, TypeError) as e:
                    logger.warning("Bad restore.json at %s: %s", meta_file, e)
        return deleted

    def list_expired(self, now: datetime | None = None) -> list[RestorePoint]:
        """Return expired restore points without mutating the artifact store."""
        if now is None:
            now = datetime.now(UTC)
        return [restore_point for restore_point in self.list_all() if restore_point.is_expired(now)]

    def build_deletion_manifest(self, now: datetime | None = None) -> list[RestoreDeletionCandidate]:
        """Build a fail-closed manifest of every expired, managed restore directory."""
        if now is None:
            now = datetime.now(UTC)
        candidates: list[RestoreDeletionCandidate] = []
        if not self.restore_root.exists():
            return candidates
        for run_dir in sorted(self.restore_root.iterdir()):
            if run_dir.name == QUARANTINE_ROOT_NAME:
                if self.list_pending_quarantines():
                    raise RuntimeError("Pending retention quarantine requires explicit recovery")
                continue
            if run_dir.name.startswith("."):
                raise RuntimeError(f"Unmanaged restore entry: {run_dir}")
            if run_dir.is_symlink() or not run_dir.is_dir():
                raise RuntimeError(f"Unmanaged restore entry: {run_dir}")
            for book_dir in sorted(run_dir.iterdir()):
                if book_dir.is_symlink() or not book_dir.is_dir():
                    raise RuntimeError(f"Unmanaged restore entry: {book_dir}")
                meta_file = book_dir / "restore.json"
                if meta_file.is_symlink() or not meta_file.is_file():
                    raise RuntimeError(f"Missing or unsafe restore manifest: {meta_file}")
                try:
                    payload = meta_file.read_bytes()
                    meta = json.loads(payload)
                    applied_at = datetime.fromisoformat(meta["applied_at"])
                    ttl = timedelta(seconds=meta.get("ttl_seconds", int(self.default_ttl.total_seconds())))
                except (OSError, json.JSONDecodeError, KeyError, TypeError, ValueError) as exc:
                    raise RuntimeError(f"Invalid restore manifest: {meta_file}") from exc
                if (now - applied_at) <= ttl:
                    continue
                stat = os.lstat(book_dir)
                candidates.append(
                    RestoreDeletionCandidate(
                        path=book_dir,
                        manifest_sha256=hashlib.sha256(payload).hexdigest(),
                        device=stat.st_dev,
                        inode=stat.st_ino,
                    )
                )
        return candidates

    def quarantine_and_delete(
        self,
        candidates: list[RestoreDeletionCandidate],
        *,
        backup_manifest_sha256: str,
    ) -> int:
        """Durably journal, quarantine, and delete exactly the supplied candidates."""
        if len(backup_manifest_sha256) != 64:
            raise RuntimeError("A verified backup manifest digest is required for retention")
        for candidate in candidates:
            self._validate_candidate(candidate.path, candidate)

        if not candidates:
            return 0
        quarantine_root = self.restore_root / QUARANTINE_ROOT_NAME
        if quarantine_root.exists():
            if quarantine_root.is_symlink() or not quarantine_root.is_dir():
                raise RuntimeError(f"Unsafe retention quarantine: {quarantine_root}")
        else:
            quarantine_root.mkdir()
            _fsync_directory(self.restore_root)
        transaction_id = uuid4().hex
        staging = quarantine_root / f"{QUARANTINE_STAGING_PREFIX}{transaction_id}"
        transaction = quarantine_root / transaction_id
        payload_root = staging / QUARANTINE_PAYLOAD_NAME
        staging.mkdir()
        _fsync_directory(quarantine_root)
        payload_root.mkdir()
        _fsync_directory(staging)
        transaction_stat = os.lstat(staging)
        payload_stat = os.lstat(payload_root)
        journal: dict[str, Any] = {
            "version": 1,
            "transaction_id": transaction_id,
            "created_at": datetime.now(UTC).isoformat(),
            "state": "prepared",
            "transaction_device": transaction_stat.st_dev,
            "transaction_inode": transaction_stat.st_ino,
            "payload_device": payload_stat.st_dev,
            "payload_inode": payload_stat.st_ino,
            "backup_manifest_sha256": backup_manifest_sha256,
            "moved": [],
            "candidates": [self._candidate_to_journal(candidate) for candidate in candidates],
        }
        self._write_quarantine_journal(staging, journal)
        os.rename(staging, transaction)
        _fsync_directory(quarantine_root)
        payload_root = transaction / QUARANTINE_PAYLOAD_NAME
        journal["state"] = "quarantining"
        self._write_quarantine_journal(transaction, journal)
        for candidate in candidates:
            self._validate_candidate(candidate.path, candidate)
            relative = candidate.path.relative_to(self.restore_root)
            target = payload_root / relative
            if not target.parent.exists():
                target.parent.mkdir()
                _fsync_directory(payload_root)
            self._validate_regular_directory(target.parent, "quarantine payload parent")
            os.rename(candidate.path, target)
            _fsync_directory(candidate.path.parent)
            _fsync_directory(target.parent)
            journal["moved"].append(str(relative))
            self._write_quarantine_journal(transaction, journal)
        journal["state"] = "quarantined"
        self._write_quarantine_journal(transaction, journal)
        journal["state"] = "deleting"
        self._write_quarantine_journal(transaction, journal)
        self._finish_quarantine_delete(transaction, journal)
        return len(candidates)

    def list_pending_quarantines(self) -> list[Path]:
        """Return validated incomplete retention transaction directories."""
        quarantine_root = self.restore_root / QUARANTINE_ROOT_NAME
        if not quarantine_root.exists():
            return []
        if quarantine_root.is_symlink() or not quarantine_root.is_dir():
            raise RuntimeError(f"Unsafe retention quarantine: {quarantine_root}")
        transactions: list[Path] = []
        for transaction in sorted(quarantine_root.iterdir()):
            if transaction.name.startswith(QUARANTINE_STAGING_PREFIX):
                self._discard_abandoned_staging(transaction)
                continue
            journal = self._read_quarantine_journal(transaction)
            self._preflight_quarantine(transaction, journal)
            if journal["state"] != "deleted":
                transactions.append(transaction)
        return transactions

    def _discard_abandoned_staging(self, staging: Path) -> None:
        """Remove a pre-publication transaction; candidates cannot have moved yet."""
        transaction_id = staging.name.removeprefix(QUARANTINE_STAGING_PREFIX)
        self._validate_transaction_id(transaction_id)
        self._validate_regular_directory(staging, "retention quarantine staging")
        allowed_files = {QUARANTINE_JOURNAL_NAME}
        for entry in staging.iterdir():
            if entry.name == QUARANTINE_PAYLOAD_NAME:
                self._validate_regular_directory(entry, "retention quarantine staging payload")
                if any(entry.iterdir()):
                    raise RuntimeError(f"Unsafe retention quarantine staging payload: {entry}")
                continue
            if entry.name in allowed_files or entry.name.startswith(f".{QUARANTINE_JOURNAL_NAME}.tmp-"):
                if entry.is_symlink() or not entry.is_file():
                    raise RuntimeError(f"Unsafe retention quarantine staging file: {entry}")
                continue
            raise RuntimeError(f"Unsafe retention quarantine staging entry: {entry}")
        for entry in list(staging.iterdir()):
            if entry.name == QUARANTINE_PAYLOAD_NAME:
                entry.rmdir()
            else:
                entry.unlink()
            _fsync_directory(staging)
        staging.rmdir()
        _fsync_directory(staging.parent)

    def recover_quarantine(self, transaction_id: str, *, backup_manifest_sha256: str) -> int:
        """Resume one explicitly selected, fully preflighted deletion transaction."""
        self._validate_transaction_id(transaction_id)
        quarantine_root = self.restore_root / QUARANTINE_ROOT_NAME
        if quarantine_root.is_symlink() or not quarantine_root.is_dir():
            raise RuntimeError(f"Unsafe retention quarantine: {quarantine_root}")
        transaction = quarantine_root / transaction_id
        journal = self._read_quarantine_journal(transaction)
        self._preflight_quarantine(transaction, journal)
        if journal["backup_manifest_sha256"] != backup_manifest_sha256:
            raise RuntimeError("Backup manifest does not match the transaction")
        candidates = journal["candidates"]
        if journal["state"] == "deleted":
            return 0
        if journal["state"] != "deleting":
            payload_root = transaction / QUARANTINE_PAYLOAD_NAME
            self._validate_payload_root(payload_root, journal)
            for item in candidates:
                relative = Path(item["relative_path"])
                source = self.restore_root / relative
                target = payload_root / relative
                source_exists = self._safe_candidate_exists(source, self.restore_root)
                target_exists = self._safe_candidate_exists(target, payload_root)
                if target_exists:
                    self._validate_journal_candidate(target, item)
                elif source_exists:
                    self._validate_journal_candidate(source, item)
                    if not target.parent.exists():
                        target.parent.mkdir()
                        _fsync_directory(payload_root)
                    self._validate_regular_directory(target.parent, "quarantine payload parent")
                    os.rename(source, target)
                    _fsync_directory(source.parent)
                    _fsync_directory(target.parent)
                else:  # pragma: no cover - preflight proves one location exists
                    raise RuntimeError(f"Missing quarantine recovery path: {relative}")
                relative_text = str(relative)
                if relative_text not in journal["moved"]:
                    journal["moved"].append(relative_text)
                    self._write_quarantine_journal(transaction, journal)
            journal["state"] = "quarantined"
            self._write_quarantine_journal(transaction, journal)
            journal["state"] = "deleting"
            self._write_quarantine_journal(transaction, journal)
        self._finish_quarantine_delete(transaction, journal)
        return len(candidates)

    @staticmethod
    def _validate_transaction_id(transaction_id: str) -> None:
        try:
            if UUID(transaction_id).hex != transaction_id:
                raise ValueError("non-canonical transaction id")
        except (AttributeError, ValueError) as exc:
            raise RuntimeError("Invalid retention quarantine transaction id") from exc

    @staticmethod
    def _validate_regular_directory(path: Path, label: str) -> os.stat_result:
        if path.is_symlink() or not path.is_dir():
            raise RuntimeError(f"Unsafe {label}: {path}")
        return os.lstat(path)

    def _validate_payload_root(self, payload_root: Path, journal: dict[str, Any]) -> None:
        stat = self._validate_regular_directory(payload_root, "retention quarantine payload")
        if stat.st_dev != journal["payload_device"] or stat.st_ino != journal["payload_inode"]:
            raise RuntimeError(f"Unsafe retention quarantine payload: {payload_root}")

    def _safe_candidate_exists(self, path: Path, trusted_root: Path) -> bool:
        parent = path.parent
        if parent.exists():
            self._validate_regular_directory(parent, "retention candidate parent")
        elif parent.is_symlink():
            raise RuntimeError(f"Unsafe retention candidate parent: {parent}")
        if path.is_symlink():
            raise RuntimeError(f"Unsafe retention candidate path: {path}")
        if not path.exists():
            return False
        if not path.is_relative_to(trusted_root):  # defensive; journal paths are already canonical
            raise RuntimeError(f"Unsafe retention candidate path: {path}")
        return True

    def _preflight_quarantine(self, transaction: Path, journal: dict[str, Any]) -> None:
        transaction_stat = self._validate_regular_directory(transaction, "retention quarantine transaction")
        if (
            transaction_stat.st_dev != journal["transaction_device"]
            or transaction_stat.st_ino != journal["transaction_inode"]
        ):
            raise RuntimeError(f"Unsafe retention quarantine transaction: {transaction}")
        payload_root = transaction / QUARANTINE_PAYLOAD_NAME
        state = journal["state"]
        if state == "deleted":
            if payload_root.exists() or payload_root.is_symlink():
                raise RuntimeError(f"Invalid deleted quarantine payload: {payload_root}")
        elif state == "deleting":
            if payload_root.exists() or payload_root.is_symlink():
                self._validate_payload_root(payload_root, journal)
        else:
            self._validate_payload_root(payload_root, journal)

        moved = set(journal["moved"])
        for item in journal["candidates"]:
            relative = Path(item["relative_path"])
            source = self.restore_root / relative
            source_exists = self._safe_candidate_exists(source, self.restore_root)
            if state in {"deleting", "deleted"}:
                if source_exists:
                    raise RuntimeError(f"Original restore point reappeared during deletion: {relative}")
                continue
            target = payload_root / relative
            target_exists = self._safe_candidate_exists(target, payload_root)
            if source_exists == target_exists:
                raise RuntimeError(f"Ambiguous or missing quarantine recovery path: {relative}")
            selected = target if target_exists else source
            self._validate_journal_candidate(selected, item)
            relative_text = str(relative)
            if state == "prepared" and (target_exists or relative_text in moved):
                raise RuntimeError(f"Invalid prepared quarantine state: {relative}")
            if relative_text in moved and not target_exists:
                raise RuntimeError(f"Invalid moved quarantine state: {relative}")
            if state == "quarantined" and (not target_exists or relative_text not in moved):
                raise RuntimeError(f"Invalid quarantined state: {relative}")

    def _candidate_to_journal(self, candidate: RestoreDeletionCandidate) -> dict[str, Any]:
        return {
            "relative_path": str(candidate.path.relative_to(self.restore_root)),
            "manifest_sha256": candidate.manifest_sha256,
            "device": candidate.device,
            "inode": candidate.inode,
        }

    def _read_quarantine_journal(self, transaction: Path) -> dict[str, Any]:
        self._validate_transaction_id(transaction.name)
        self._validate_regular_directory(transaction, "retention quarantine transaction")
        journal_path = transaction / QUARANTINE_JOURNAL_NAME
        if journal_path.is_symlink() or not journal_path.is_file():
            raise RuntimeError(f"Missing or unsafe quarantine journal: {journal_path}")
        try:
            journal = cast(dict[str, Any], json.loads(journal_path.read_text()))
            if (
                journal["version"] != 1
                or journal["transaction_id"] != transaction.name
                or journal["state"] not in QUARANTINE_STATES
                or not isinstance(journal["moved"], list)
                or not isinstance(journal["candidates"], list)
                or not journal["candidates"]
                or not isinstance(journal["updated_at"], str)
                or not isinstance(journal["transaction_device"], int)
                or not isinstance(journal["transaction_inode"], int)
                or not isinstance(journal["payload_device"], int)
                or not isinstance(journal["payload_inode"], int)
                or not isinstance(journal["backup_manifest_sha256"], str)
                or len(journal["backup_manifest_sha256"]) != 64
            ):
                raise ValueError("invalid journal fields")
            datetime.fromisoformat(journal["created_at"])
            datetime.fromisoformat(journal["updated_at"])
            candidate_paths: list[str] = []
            for item in journal["candidates"]:
                relative = Path(item["relative_path"])
                if (
                    relative.is_absolute()
                    or len(relative.parts) != 2
                    or any(part in {"", ".", ".."} or part.startswith(".") for part in relative.parts)
                    or str(relative) != item["relative_path"]
                ):
                    raise ValueError("unsafe candidate path")
                if not isinstance(item["device"], int) or not isinstance(item["inode"], int):
                    raise ValueError("invalid candidate identity")
                if not isinstance(item["manifest_sha256"], str) or len(item["manifest_sha256"]) != 64:
                    raise ValueError("invalid candidate digest")
                candidate_paths.append(item["relative_path"])
            if len(candidate_paths) != len(set(candidate_paths)):
                raise ValueError("duplicate candidate path")
            if any(not isinstance(item, str) for item in journal["moved"]):
                raise ValueError("invalid moved path")
            if len(journal["moved"]) != len(set(journal["moved"])):
                raise ValueError("duplicate moved path")
            if not set(journal["moved"]).issubset(candidate_paths):
                raise ValueError("unknown moved path")
        except (OSError, KeyError, TypeError, ValueError, json.JSONDecodeError) as exc:
            raise RuntimeError(f"Invalid retention quarantine journal: {journal_path}") from exc
        return journal

    def _write_quarantine_journal(self, transaction: Path, journal: dict[str, Any]) -> None:
        journal["updated_at"] = datetime.now(UTC).isoformat()
        _write_json_atomically(transaction / QUARANTINE_JOURNAL_NAME, journal)

    def _finish_quarantine_delete(self, transaction: Path, journal: dict[str, Any]) -> None:
        payload_root = transaction / QUARANTINE_PAYLOAD_NAME
        if payload_root.exists():
            self._validate_payload_root(payload_root, journal)
            shutil.rmtree(payload_root)
            _fsync_directory(transaction)
        journal["state"] = "deleted"
        self._write_quarantine_journal(transaction, journal)

    def _validate_candidate(self, path: Path, candidate: RestoreDeletionCandidate) -> None:
        self._validate_candidate_identity(
            path,
            manifest_sha256=candidate.manifest_sha256,
            device=candidate.device,
            inode=candidate.inode,
        )

    def _validate_journal_candidate(self, path: Path, item: dict[str, Any]) -> None:
        self._validate_candidate_identity(
            path,
            manifest_sha256=item["manifest_sha256"],
            device=item["device"],
            inode=item["inode"],
        )

    @staticmethod
    def _validate_candidate_identity(path: Path, *, manifest_sha256: str, device: int, inode: int) -> None:
        if path.is_symlink() or not path.is_dir():
            raise RuntimeError(f"Restore point changed after preview: {path}")
        stat = os.lstat(path)
        manifest = path / "restore.json"
        if (
            stat.st_dev != device
            or stat.st_ino != inode
            or manifest.is_symlink()
            or not manifest.is_file()
            or hashlib.sha256(manifest.read_bytes()).hexdigest() != manifest_sha256
        ):
            raise RuntimeError(f"Restore point changed after preview: {path}")

    def list_all(self) -> list[RestorePoint]:
        out: list[RestorePoint] = []
        if not self.restore_root.exists():
            return out
        for run_dir in self.restore_root.iterdir():
            if not run_dir.is_dir():
                continue
            for book_dir in run_dir.iterdir():
                if not book_dir.is_dir():
                    continue
                meta_file = book_dir / "restore.json"
                if not meta_file.exists():
                    continue
                try:
                    meta = json.loads(meta_file.read_text())
                    out.append(
                        RestorePoint(
                            path=book_dir,
                            run_id=meta["run_id"],
                            book_key=meta["book_key"],
                            calibre_book_id=meta.get("calibre_book_id"),
                            applied_at=datetime.fromisoformat(meta["applied_at"]),
                            fields_changed=meta.get("fields_changed", []),
                            restore_point_ttl=timedelta(seconds=meta.get("ttl_seconds", 30 * 24 * 3600)),
                        )
                    )
                except (json.JSONDecodeError, KeyError, ValueError):
                    continue
        return out


class ConservativeAutoApply:
    """The v1.0 conservative auto-apply gate.

    Combines the BookVerdict from ContentVerificationEngine with the on-disk
    RestorePointStore to safely apply only the books that pass the gate.
    """

    def __init__(
        self,
        store: RestorePointStore | None = None,
        *,
        allow_high_risk: bool = False,
        dry_run: bool = True,
    ):
        self.store = store
        self.allow_high_risk = allow_high_risk
        self.dry_run = dry_run

    def is_eligible(self, verdict: BookVerdict) -> tuple[bool, str]:
        """Returns (eligible, reason)."""
        if not verdict.auto_apply_eligible:
            return False, f"BookVerdict.auto_apply_eligible=False (action={verdict.action.value})"
        if not self.allow_high_risk and verdict.risk_flags and any(rf in HIGH_RISK_FLAGS for rf in verdict.risk_flags):
            return (
                False,
                f"High-risk flag present: {[f for f in verdict.risk_flags if f in HIGH_RISK_FLAGS]}",
            )
        if verdict.overall_confidence < 80:
            return False, f"Overall confidence {verdict.overall_confidence} < 80"
        if not verdict.proposed_patch:
            return False, "No proposed patch"
        return True, "ok"

    def filter_eligible(self, verdicts: list[BookVerdict]) -> list[BookVerdict]:
        return [v for v in verdicts if self.is_eligible(v)[0]]


__all__ = [
    "RestorePoint",
    "RestorePointStore",
    "RestoreDeletionCandidate",
    "ConservativeAutoApply",
]

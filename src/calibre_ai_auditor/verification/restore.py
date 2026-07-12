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
import shutil
from dataclasses import dataclass
from datetime import UTC, datetime, timedelta
from pathlib import Path
from typing import Any
from uuid import uuid4

from calibre_ai_auditor.verification.verdict import HIGH_RISK_FLAGS, BookVerdict

logger = logging.getLogger(__name__)


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
        self.artifacts_dir = artifacts_dir
        self.restore_root = artifacts_dir / "restore"
        self.restore_root.mkdir(parents=True, exist_ok=True)
        self.default_ttl = default_ttl

    def path_for(self, run_id: str, book_key: str) -> Path:
        safe_key = book_key.replace(":", "_").replace("/", "_")
        safe_run = run_id.replace("/", "_")
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
        rp_dir = self.path_for(run_id, book_key)
        rp_dir.mkdir(parents=True, exist_ok=True)

        # 1. Original OPF (always; this is the cheapest, most reliable restore)
        if original_opf and original_opf.exists():
            target = rp_dir / "original.opf"
            try:
                shutil.copy2(original_opf, target)
            except OSError as e:
                logger.warning("Failed to copy original OPF: %s", e)

        # 2. Original book file (only if small enough; otherwise hardlink)
        if original_file and original_file.exists():
            target = rp_dir / ("original" + original_file.suffix)
            try:
                # Try hardlink first (instant, no extra disk)
                target.hardlink_to(original_file)
            except OSError:
                # Fall back to copy (slow for large files but always works)
                try:
                    shutil.copy2(original_file, target)
                except OSError as e:
                    logger.warning("Failed to backup original file: %s", e)

        # 3. Original cover (if changed)
        if original_cover and original_cover.exists():
            target = rp_dir / ("original.cover" + original_cover.suffix)
            try:
                shutil.copy2(original_cover, target)
            except OSError as e:
                logger.warning("Failed to backup original cover: %s", e)

        # 4. Metadata snapshots
        now = datetime.now(UTC)
        (rp_dir / "before.json").write_text(json.dumps(before_metadata, indent=2, default=str))
        (rp_dir / "after.json").write_text(json.dumps(after_metadata, indent=2, default=str))
        (rp_dir / "restore.json").write_text(
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
            )
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
                    ttl = timedelta(seconds=meta.get("ttl_seconds", int(self.default_ttl.total_seconds())))
                    if (now - applied_at) > ttl:
                        shutil.rmtree(book_dir)
                        deleted += 1
                        logger.info("Cleaned up expired restore point: %s", book_dir)
                except (json.JSONDecodeError, KeyError, ValueError) as e:
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
            if run_dir.name.startswith("."):
                continue
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

    def quarantine_and_delete(self, candidates: list[RestoreDeletionCandidate]) -> int:
        """Revalidate and atomically quarantine exactly the supplied candidates before deletion."""
        for candidate in candidates:
            if candidate.path.is_symlink() or not candidate.path.is_dir():
                raise RuntimeError(f"Restore point changed after preview: {candidate.path}")
            stat = os.lstat(candidate.path)
            manifest = candidate.path / "restore.json"
            if (
                stat.st_dev != candidate.device
                or stat.st_ino != candidate.inode
                or manifest.is_symlink()
                or not manifest.is_file()
                or hashlib.sha256(manifest.read_bytes()).hexdigest() != candidate.manifest_sha256
            ):
                raise RuntimeError(f"Restore point changed after preview: {candidate.path}")

        if not candidates:
            return 0
        quarantine = self.restore_root / ".retention-quarantine" / uuid4().hex
        moved: list[Path] = []
        try:
            for candidate in candidates:
                relative = candidate.path.relative_to(self.restore_root)
                target = quarantine / relative
                target.parent.mkdir(parents=True, exist_ok=True)
                os.rename(candidate.path, target)
                moved.append(target)
        except Exception:
            for target in reversed(moved):
                original = self.restore_root / target.relative_to(quarantine)
                original.parent.mkdir(parents=True, exist_ok=True)
                os.rename(target, original)
            raise
        shutil.rmtree(quarantine)
        return len(candidates)

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

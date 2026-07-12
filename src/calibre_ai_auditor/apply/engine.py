import logging
from datetime import UTC, datetime
from pathlib import Path
from typing import Any
from uuid import uuid4

from sqlmodel import Session

from calibre_ai_auditor.calibre.cli import CalibreCLI
from calibre_ai_auditor.storage.models import BookRecord, Change
from calibre_ai_auditor.verification.restore import RestorePointStore

logger = logging.getLogger(__name__)


class ApplyEngine:
    def __init__(self, cli: CalibreCLI, artifacts_dir: Path):
        self.cli = cli
        self.artifacts_dir = artifacts_dir

    def apply_patch(self, _session: Session, book: BookRecord, patch: dict[str, Any]) -> Change:
        """
        Applies a metadata patch and records the change.
        """
        if book.source != "calibre" or not book.calibre_book_id:
            raise ValueError(f"Cannot apply patch to non-Calibre book: {book.book_key}")

        backup_dir = self.artifacts_dir / "backups" / str(book.calibre_book_id)
        backup_dir.mkdir(parents=True, exist_ok=True)

        timestamp = datetime.now(UTC).strftime("%Y%m%d_%H%M%S_%f")
        backup_opf = backup_dir / f"before_{timestamp}_{uuid4().hex}.opf"

        # 1. Backup
        logger.info(f"Backing up metadata for book {book.calibre_book_id} to {backup_opf}")
        self.cli.export_opf(book.calibre_book_id, backup_opf)

        restore_point = RestorePointStore(self.artifacts_dir).create(
            run_id=book.run_id,
            book_key=book.book_key,
            calibre_book_id=book.calibre_book_id,
            before_metadata=book.current_metadata,
            after_metadata=patch,
            fields_changed=list(patch),
            original_opf=backup_opf,
        )
        if not (restore_point.path / "original.opf").is_file():
            raise RuntimeError(f"Restore point could not be created for {book.book_key}")

        # 2. Record the change
        change = Change(
            book_key=book.book_key,
            run_id=book.run_id,
            before_metadata=book.current_metadata,
            after_metadata=patch,
            backup_opf_path=str(backup_opf),
            status="pending_apply",
        )
        _session.add(change)
        _session.commit()
        _session.refresh(change)

        # 3. Apply changes via Calibre CLI
        logger.info(f"Applying metadata patch to book {book.calibre_book_id}...")
        try:
            self._apply_metadata_fields(book.calibre_book_id, patch)
        except Exception:
            try:
                self.cli.set_metadata(book.calibre_book_id, backup_opf)
                change.status = "failed_rolled_back"
            except Exception:
                change.status = "failed_rollback_failed"
                logger.exception("Rollback failed for book %s", book.book_key)
            _session.add(change)
            _session.commit()
            raise

        change.status = "applied"
        _session.add(change)
        _session.commit()
        return change

    def _apply_metadata_fields(self, book_id: int, patch: dict[str, Any]) -> None:
        """
        Calls calibredb set_metadata for each field in the patch.
        """
        # Mapping our internal Metadata fields to calibredb field names
        field_map = {
            "title": "title",
            "authors": "authors",
            "publisher": "publisher",
            "published_date": "pubdate",
            "language": "languages",
            "series": "series",
            "series_index": "series_index",
        }

        for key, value in patch.items():
            if key == "identifiers" and isinstance(value, dict):
                ident_str = ",".join([f"{k}:{v}" for k, v in value.items()])
                cmd = [
                    "calibredb",
                    "set_metadata",
                    str(book_id),
                    "--identifiers",
                    ident_str,
                ]
                if self.cli.library_path:
                    cmd.extend(["--with-library", str(self.cli.library_path)])
                self.cli._run_command(cmd)
                continue

            calibre_field = field_map.get(key)
            if calibre_field and value:
                val_str = ",".join(value) if isinstance(value, list) else str(value)
                cmd = [
                    "calibredb",
                    "set_metadata",
                    str(book_id),
                    "--field",
                    f"{calibre_field}:{val_str}",
                ]
                if self.cli.library_path:
                    cmd.extend(["--with-library", str(self.cli.library_path)])
                self.cli._run_command(cmd)

    def undo_change(self, session: Session, change: Change) -> None:
        """
        Reverts a change using the backup OPF.
        """
        if change.status == "undone":
            logger.warning(f"Change {change.id} is already undone.")
            return

        book_id_str = change.book_key.split(":")[-1]
        book_id = int(book_id_str)

        logger.info(f"Restoring metadata for book {book_id} from {change.backup_opf_path}")
        self.cli.set_metadata(book_id, Path(change.backup_opf_path))

        change.status = "undone"
        session.add(change)

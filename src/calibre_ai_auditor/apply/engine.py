import hashlib
import logging
import shutil
import xml.etree.ElementTree as ET
from datetime import UTC, datetime
from pathlib import Path
from typing import Any
from uuid import uuid4

from sqlmodel import Session

from calibre_ai_auditor.calibre.cli import CalibreCLI
from calibre_ai_auditor.storage.models import BookRecord, Change
from calibre_ai_auditor.verification.restore import RestorePointStore

logger = logging.getLogger(__name__)

DC = "http://purl.org/dc/elements/1.1/"
OPF = "http://www.idpf.org/2007/opf"


class ApplyEngine:
    def __init__(self, cli: CalibreCLI, artifacts_dir: Path):
        self.cli = cli
        self.artifacts_dir = artifacts_dir

    def apply_patch(
        self,
        _session: Session,
        book: BookRecord,
        patch: dict[str, Any],
        *,
        operation_id: str | None = None,
        live_before: dict[str, Any] | None = None,
    ) -> Change:
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
        backup_sha256 = hashlib.sha256(backup_opf.read_bytes()).hexdigest()

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
            operation_id=operation_id,
            book_key=book.book_key,
            run_id=book.run_id,
            before_metadata=live_before if live_before is not None else book.current_metadata,
            after_metadata=patch,
            backup_opf_path=str(backup_opf),
            backup_opf_sha256=backup_sha256,
            status="pending_apply",
        )
        _session.add(change)
        _session.commit()
        _session.refresh(change)

        # 3. Apply changes via Calibre CLI
        logger.info(f"Applying metadata patch to book {book.calibre_book_id}...")
        target_opf = backup_opf.with_name(backup_opf.name.replace("before_", "target_"))
        self._build_target_opf(backup_opf, target_opf, patch)
        try:
            self.cli.set_metadata(book.calibre_book_id, target_opf)
        except Exception:
            try:
                self.cli.set_metadata(book.calibre_book_id, backup_opf)
                restored = self.cli.show_metadata(book.calibre_book_id)
                expected = live_before if live_before is not None else book.current_metadata
                change.status = (
                    "failed_rolled_back"
                    if all(restored.get(field) == value for field, value in expected.items())
                    else "failed_rollback_failed"
                )
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

    def _build_target_opf(self, source: Path, target: Path, patch: dict[str, Any]) -> None:
        """Create one complete target OPF so Calibre receives one atomic metadata command."""
        shutil.copyfile(source, target)
        tree = ET.parse(target)
        root = tree.getroot()
        metadata = root.find(f"{{{OPF}}}metadata")
        if metadata is None:
            raise ValueError("backup OPF has no metadata element")

        scalar_fields = {
            "title": "title",
            "publisher": "publisher",
            "published_date": "date",
            "language": "language",
        }
        for field, tag in scalar_fields.items():
            if field in patch:
                self._replace_dc(metadata, tag, [str(patch[field])])
        if "authors" in patch:
            authors = patch["authors"] if isinstance(patch["authors"], list) else [patch["authors"]]
            self._replace_dc(metadata, "creator", [str(author) for author in authors])
        if "identifiers" in patch and isinstance(patch["identifiers"], dict):
            for element in list(metadata.findall(f"{{{DC}}}identifier")):
                metadata.remove(element)
            for scheme, value in sorted(patch["identifiers"].items()):
                element = ET.SubElement(metadata, f"{{{DC}}}identifier")
                element.set(f"{{{OPF}}}scheme", str(scheme).upper())
                element.text = str(value)
        for field, name in (("series", "calibre:series"), ("series_index", "calibre:series_index")):
            if field in patch:
                for element in list(metadata.findall(f"{{{OPF}}}meta")):
                    if element.get("name") == name:
                        metadata.remove(element)
                element = ET.SubElement(metadata, f"{{{OPF}}}meta")
                element.set("name", name)
                element.set("content", str(patch[field]))
        tree.write(target, encoding="utf-8", xml_declaration=True)

    @staticmethod
    def _replace_dc(metadata: ET.Element, tag: str, values: list[str]) -> None:
        for element in list(metadata.findall(f"{{{DC}}}{tag}")):
            metadata.remove(element)
        for value in values:
            element = ET.SubElement(metadata, f"{{{DC}}}{tag}")
            element.text = value

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

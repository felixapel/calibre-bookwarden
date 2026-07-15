import hashlib
import io
import logging
import os
import xml.etree.ElementTree as ET
from datetime import UTC, datetime
from pathlib import Path
from typing import Any
from uuid import uuid4

from PIL import Image
from sqlmodel import Session

from calibre_ai_auditor.apply.artifacts import (
    MAX_OPF_BYTES,
    bind_exported_artifact,
    set_cover_from_artifact,
    set_metadata_from_artifact,
    verify_artifact,
)
from calibre_ai_auditor.calibre.cli import VOLATILE_METADATA_FIELDS, CalibreCLI
from calibre_ai_auditor.security.files import (
    copy_file_beneath,
    ensure_secure_directory,
    read_file_beneath,
    sha256_file_beneath,
    write_bytes_beneath,
)
from calibre_ai_auditor.storage.models import BookRecord, Change
from calibre_ai_auditor.verification.identity_v2 import CanonicalPatch, validate_isbn
from calibre_ai_auditor.verification.restore import RestorePointStore

logger = logging.getLogger(__name__)

DC = "http://purl.org/dc/elements/1.1/"
OPF = "http://www.idpf.org/2007/opf"
MAX_COVER_BYTES = 20 * 1024 * 1024
MAX_COVER_PIXELS = 25_000_000


class ApplyEngine:
    def __init__(self, cli: CalibreCLI, artifacts_dir: Path):
        self.cli = cli
        self.artifacts_dir = Path(os.path.abspath(artifacts_dir))

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
        patch = CanonicalPatch.model_validate(patch).model_dump(mode="json", exclude_none=True)
        if not patch:
            raise ValueError("Cannot apply an empty canonical patch")
        ensure_secure_directory(self.artifacts_dir)
        backup_dir = ensure_secure_directory(self.artifacts_dir / "backups" / str(book.calibre_book_id))
        cover_path = self._stage_cover_patch(
            patch.get("cover"),
            live_before if live_before is not None else book.current_metadata,
            patch.get("identifiers"),
            backup_dir,
        )

        timestamp = datetime.now(UTC).strftime("%Y%m%d_%H%M%S_%f")
        backup_opf = backup_dir / f"before_{timestamp}_{uuid4().hex}.opf"

        # 1. Backup
        logger.info(f"Backing up metadata for book {book.calibre_book_id} to {backup_opf}")
        exported_opf_sha256 = self.cli.export_opf(book.calibre_book_id, backup_opf)
        backup_sha256 = bind_exported_artifact(
            self.artifacts_dir,
            backup_opf,
            exported_opf_sha256,
            max_bytes=MAX_OPF_BYTES,
        )
        backup_cover: Path | None = None
        backup_cover_sha256: str | None = None
        if cover_path is not None:
            candidate = backup_opf.with_suffix(".cover")
            exported_cover_sha256 = self.cli.export_cover(book.calibre_book_id, candidate)
            if not exported_cover_sha256:
                raise ValueError("cover update is not reversible because the current cover could not be backed up")
            backup_cover = candidate
            backup_cover_sha256 = bind_exported_artifact(
                self.artifacts_dir,
                candidate,
                exported_cover_sha256,
                max_bytes=MAX_COVER_BYTES,
            )

        before_source = live_before if live_before is not None else book.current_metadata
        before_custom = (
            {"#edition": str(before_source.get("#edition") or before_source.get("edition_statement") or "")}
            if "edition_statement" in patch
            else {}
        )

        restore_point = RestorePointStore(self.artifacts_dir).create(
            run_id=book.run_id,
            book_key=book.book_key,
            calibre_book_id=book.calibre_book_id,
            before_metadata=book.current_metadata,
            after_metadata=patch,
            fields_changed=list(patch),
            original_opf=backup_opf,
        )
        if (
            sha256_file_beneath(
                self.artifacts_dir,
                restore_point.path / "original.opf",
                max_bytes=MAX_OPF_BYTES,
            )
            != backup_sha256
        ):
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
            backup_cover_path=str(backup_cover) if backup_cover else None,
            backup_cover_sha256=backup_cover_sha256,
            before_custom=before_custom,
            status="pending_apply",
        )
        _session.add(change)
        _session.commit()
        _session.refresh(change)

        # 3. Apply changes via Calibre CLI
        logger.info(f"Applying metadata patch to book {book.calibre_book_id}...")
        target_opf = backup_opf.with_name(backup_opf.name.replace("before_", "target_"))
        target_sha256 = self._build_target_opf(
            backup_opf,
            target_opf,
            patch,
            source_sha256=backup_sha256,
        )
        try:
            set_metadata_from_artifact(
                self.cli,
                book.calibre_book_id,
                self.artifacts_dir,
                target_opf,
                target_sha256,
            )
            if "edition_statement" in patch:
                self.cli.set_custom(book.calibre_book_id, "#edition", str(patch["edition_statement"]))
            if cover_path is not None:
                set_cover_from_artifact(
                    self.cli,
                    book.calibre_book_id,
                    self.artifacts_dir,
                    cover_path,
                    str(patch["cover"]["artifact_sha256"]),
                )
        except Exception:
            try:
                self._restore_components(book.calibre_book_id, change)
                restored = self.cli.show_metadata(book.calibre_book_id)
                expected = live_before if live_before is not None else book.current_metadata
                change.status = (
                    "failed_rolled_back"
                    if self._metadata_matches_restore(restored, expected, change)
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

    def _stage_cover_patch(
        self,
        raw_cover: Any,
        current_metadata: dict[str, Any],
        patch_identifiers: Any,
        backup_dir: Path,
    ) -> Path | None:
        if raw_cover is None:
            return None
        artifact = Path(str(raw_cover["artifact_path"]))
        identifiers = (
            patch_identifiers if isinstance(patch_identifiers, dict) else current_metadata.get("identifiers", {})
        )
        current_isbn = validate_isbn(str(identifiers.get("isbn", ""))) if isinstance(identifiers, dict) else None
        if current_isbn != raw_cover["manifestation_isbn"]:
            raise ValueError("cover artifact is not bound to the exact patch manifestation")

        staged = backup_dir / f"cover_input_{uuid4().hex}{artifact.suffix.lower()}"
        copy_file_beneath(
            self.artifacts_dir,
            artifact,
            staged,
            max_bytes=MAX_COVER_BYTES,
            expected_sha256=str(raw_cover["artifact_sha256"]),
            target_root=self.artifacts_dir,
        )
        payload = read_file_beneath(
            self.artifacts_dir,
            staged,
            max_bytes=MAX_COVER_BYTES,
            expected_sha256=str(raw_cover["artifact_sha256"]),
        )
        with Image.open(io.BytesIO(payload)) as image:
            if image.width * image.height > MAX_COVER_PIXELS:
                raise ValueError("cover artifact exceeds the 25 megapixel limit")
            image.verify()
        return staged

    def _require_artifact(self, path: Path, expected_sha256: str | None) -> Path:
        verify_artifact(self.artifacts_dir, path, expected_sha256, max_bytes=MAX_OPF_BYTES)
        return path

    def _restore_components(self, book_id: int, change: Change) -> None:
        opf_path = Path(change.backup_opf_path)
        set_metadata_from_artifact(
            self.cli,
            book_id,
            self.artifacts_dir,
            opf_path,
            change.backup_opf_sha256,
        )
        for column, value in (change.before_custom or {}).items():
            self.cli.set_custom(book_id, column, str(value))
        if change.backup_cover_path:
            cover_path = Path(change.backup_cover_path)
            set_cover_from_artifact(
                self.cli,
                book_id,
                self.artifacts_dir,
                cover_path,
                change.backup_cover_sha256,
            )

    def _metadata_matches_restore(
        self,
        observed: dict[str, Any],
        expected: dict[str, Any],
        change: Change,
    ) -> bool:
        special = {"cover", "#edition", "edition_statement"}
        ignored = special | VOLATILE_METADATA_FIELDS
        if not all(observed.get(field) == value for field, value in expected.items() if field not in ignored):
            return False
        for column, value in (change.before_custom or {}).items():
            observed_value = observed.get(column, observed.get("edition_statement", ""))
            if str(observed_value or "") != str(value):
                return False
        if change.backup_cover_path:
            observed_cover = observed.get("cover")
            if not isinstance(observed_cover, str) or change.backup_cover_sha256 is None:
                return False
            path = Path(observed_cover)
            raw_library = getattr(self.cli, "library_path", None)
            root = Path(raw_library) if isinstance(raw_library, (str, Path)) else path.parent
            if sha256_file_beneath(root, path, max_bytes=MAX_COVER_BYTES) != change.backup_cover_sha256:
                return False
        return True

    def _build_target_opf(
        self,
        source: Path,
        target: Path,
        patch: dict[str, Any],
        *,
        source_sha256: str | None = None,
    ) -> str:
        """Create one complete target OPF so Calibre receives one atomic metadata command."""
        payload = read_file_beneath(
            self.artifacts_dir,
            source,
            max_bytes=MAX_OPF_BYTES,
            expected_sha256=source_sha256,
        )
        root = ET.fromstring(payload)
        metadata = root.find(f"{{{OPF}}}metadata")
        if metadata is None:
            raise ValueError("backup OPF has no metadata element")

        scalar_fields = {
            "title": "title",
            "publisher": "publisher",
            "pubdate": "date",
        }
        for field, tag in scalar_fields.items():
            if field in patch:
                self._replace_dc(metadata, tag, [str(patch[field])])
        if "authors" in patch:
            authors = patch["authors"] if isinstance(patch["authors"], list) else [patch["authors"]]
            self._replace_dc(metadata, "creator", [str(author) for author in authors])
        if "languages" in patch:
            languages = patch["languages"] if isinstance(patch["languages"], list) else [patch["languages"]]
            self._replace_dc(metadata, "language", [str(language) for language in languages])
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
        rendered = ET.tostring(root, encoding="utf-8", xml_declaration=True)
        write_bytes_beneath(self.artifacts_dir, target, rendered)
        return hashlib.sha256(rendered).hexdigest()

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
        self._restore_components(book_id, change)

        change.status = "undone"
        session.add(change)

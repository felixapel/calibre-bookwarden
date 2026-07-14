import hashlib
from pathlib import Path
from typing import Any
from unittest.mock import MagicMock

import pytest
from PIL import Image
from sqlmodel import Session, SQLModel, create_engine, select

from calibre_ai_auditor.apply.engine import ApplyEngine
from calibre_ai_auditor.security.files import SecurePathError
from calibre_ai_auditor.storage.models import BookRecord, Change

SAMPLE_OPF = """<?xml version="1.0" encoding="utf-8"?>
<package xmlns="http://www.idpf.org/2007/opf" xmlns:dc="http://purl.org/dc/elements/1.1/">
  <metadata><dc:title>Old Title</dc:title><dc:publisher>Old Publisher</dc:publisher></metadata>
</package>
"""
ISBN = "9780306406157"


@pytest.fixture
def session() -> Any:
    engine = create_engine("sqlite:///:memory:")
    SQLModel.metadata.create_all(engine)
    with Session(engine) as session:
        yield session


def test_apply_patch_backup(session: Any, tmp_path: Any) -> None:
    mock_cli = MagicMock()
    mock_cli.export_opf.side_effect = lambda _book_id, destination: destination.write_text(SAMPLE_OPF)
    engine = ApplyEngine(mock_cli, tmp_path)

    book = BookRecord(
        book_key="calibre:1",
        run_id="test_run",
        calibre_book_id=1,
        source="calibre",
        current_metadata={"title": "Old Title"},
        files=[],
    )

    patch_data = {"title": "New Title"}

    change = engine.apply_patch(session, book, patch_data)

    assert change.book_key == "calibre:1"
    assert "before_" in change.backup_opf_path
    mock_cli.export_opf.assert_called_once()
    mock_cli.set_metadata.assert_called_once()


def test_undo_change(session: Any, tmp_path: Any) -> None:
    mock_cli = MagicMock()
    engine = ApplyEngine(mock_cli, tmp_path)

    backup_file = tmp_path / "backup.opf"
    backup_file.write_text("fake opf")

    change = Change(
        id=1,
        book_key="calibre:1",
        run_id="test_run",
        before_metadata={},
        after_metadata={},
        backup_opf_path=str(backup_file),
        backup_opf_sha256=hashlib.sha256(backup_file.read_bytes()).hexdigest(),
        status="applied",
    )
    session.add(change)
    session.commit()

    engine.undo_change(session, change)

    assert change.status == "undone"
    mock_cli.set_metadata.assert_called_once_with(1, Path(str(backup_file)))


def test_apply_patch_restores_backup_and_records_failed_attempt(
    tmp_path: Any,
) -> None:
    db_engine = create_engine(f"sqlite:///{tmp_path / 'changes.db'}")
    SQLModel.metadata.create_all(db_engine)
    mock_cli = MagicMock()

    def export_opf(_book_id: int, destination: Path) -> None:
        destination.write_text(SAMPLE_OPF)

    mock_cli.export_opf.side_effect = export_opf
    mock_cli.set_metadata.side_effect = [RuntimeError("metadata write failed"), None]
    mock_cli.show_metadata.return_value = {"title": "Old Title", "publisher": "Old Publisher"}
    engine = ApplyEngine(mock_cli, tmp_path)
    book = BookRecord(
        book_key="calibre:1",
        run_id="test_run",
        calibre_book_id=1,
        source="calibre",
        current_metadata={"title": "Old Title", "publisher": "Old Publisher"},
    )

    with Session(db_engine) as write_session:
        with pytest.raises(RuntimeError, match="metadata write failed"):
            engine.apply_patch(
                write_session,
                book,
                {"title": "New Title", "publisher": "New Publisher"},
            )
        write_session.rollback()

    with Session(db_engine) as read_session:
        failed_change = read_session.exec(select(Change)).one()
        assert failed_change.status == "failed_rolled_back"
        assert mock_cli.set_metadata.call_count == 2
        assert mock_cli.set_metadata.call_args_list[-1].args == (1, Path(failed_change.backup_opf_path))


def test_apply_engine_rejects_legacy_and_unknown_patch_fields_before_backup(session: Any, tmp_path: Path) -> None:
    mock_cli = MagicMock()
    engine = ApplyEngine(mock_cli, tmp_path)
    book = BookRecord(
        book_key="calibre:1",
        run_id="test_run",
        calibre_book_id=1,
        source="calibre",
        current_metadata={"title": "Old Title"},
    )

    with pytest.raises(ValueError, match="Extra inputs"):
        engine.apply_patch(session, book, {"published_date": "2024-01-01"})
    with pytest.raises(ValueError, match="Extra inputs"):
        engine.apply_patch(session, book, {"tags": ["unsafe"]})

    mock_cli.export_opf.assert_not_called()


def test_apply_engine_rejects_symlinked_artifact_parent_before_calibre_write(
    session: Any,
    tmp_path: Path,
) -> None:
    artifacts = tmp_path / "artifacts"
    outside = tmp_path / "outside"
    artifacts.mkdir()
    outside.mkdir()
    (artifacts / "backups").symlink_to(outside, target_is_directory=True)
    mock_cli = MagicMock()
    book = BookRecord(
        book_key="calibre:1",
        run_id="test_run",
        calibre_book_id=1,
        source="calibre",
        current_metadata={"title": "Old Title"},
    )

    with pytest.raises(SecurePathError, match="directory component"):
        ApplyEngine(mock_cli, artifacts).apply_patch(session, book, {"title": "New Title"})

    mock_cli.export_opf.assert_not_called()
    mock_cli.set_metadata.assert_not_called()


def test_apply_engine_rejects_backup_replaced_between_export_and_seal(session: Any, tmp_path: Path) -> None:
    mock_cli = MagicMock()
    original = SAMPLE_OPF.encode()

    def export_then_replace(_book_id: int, destination: Path) -> str:
        destination.write_bytes(original)
        digest = hashlib.sha256(original).hexdigest()
        destination.write_text(SAMPLE_OPF.replace("Old Title", "Substituted Title"))
        return digest

    mock_cli.export_opf.side_effect = export_then_replace
    book = BookRecord(
        book_key="calibre:1",
        run_id="test_run",
        calibre_book_id=1,
        source="calibre",
        current_metadata={"title": "Old Title"},
    )

    with pytest.raises(RuntimeError, match="replaced"):
        ApplyEngine(mock_cli, tmp_path).apply_patch(session, book, {"title": "New Title"})

    mock_cli.set_metadata.assert_not_called()


def test_target_opf_uses_canonical_pubdate_and_languages(tmp_path: Path) -> None:
    source = tmp_path / "source.opf"
    target = tmp_path / "target.opf"
    source.write_text(SAMPLE_OPF)

    ApplyEngine(MagicMock(), tmp_path)._build_target_opf(
        source,
        target,
        {"pubdate": "2024-01-02", "languages": ["eng", "spa"]},
    )

    rendered = target.read_text()
    assert "2024-01-02" in rendered
    assert "eng" in rendered
    assert "spa" in rendered


def test_apply_patch_writes_edition_and_manifestation_bound_cover_with_backups(
    session: Any,
    tmp_path: Path,
) -> None:
    mock_cli = MagicMock()
    mock_cli.export_opf.side_effect = lambda _book_id, destination: destination.write_text(SAMPLE_OPF)
    mock_cli.export_cover.side_effect = lambda _book_id, destination: destination.write_bytes(b"old-cover") or True
    cover = tmp_path / "covers" / "new.jpg"
    cover.parent.mkdir()
    Image.new("RGB", (10, 10), "red").save(cover)
    cover_sha = hashlib.sha256(cover.read_bytes()).hexdigest()
    book = BookRecord(
        book_key="calibre:1",
        run_id="test_run",
        calibre_book_id=1,
        source="calibre",
        current_metadata={"title": "Old Title", "identifiers": {"isbn": ISBN}, "#edition": "Old edition"},
    )

    change = ApplyEngine(mock_cli, tmp_path).apply_patch(
        session,
        book,
        {
            "edition_statement": "First edition",
            "cover": {
                "artifact_path": str(cover),
                "artifact_sha256": cover_sha,
                "manifestation_isbn": ISBN,
            },
        },
    )

    mock_cli.set_custom.assert_called_once_with(1, "#edition", "First edition")
    staged_cover = mock_cli.set_cover.call_args.args[1]
    assert staged_cover != cover
    assert hashlib.sha256(staged_cover.read_bytes()).hexdigest() == cover_sha
    assert change.backup_cover_path is not None
    assert Path(change.backup_cover_path).read_bytes() == b"old-cover"
    assert change.backup_cover_sha256 == hashlib.sha256(b"old-cover").hexdigest()
    assert change.before_custom == {"#edition": "Old edition"}


def test_cover_failure_restores_opf_custom_column_and_previous_cover(tmp_path: Path) -> None:
    db_engine = create_engine(f"sqlite:///{tmp_path / 'cover-failure.db'}")
    SQLModel.metadata.create_all(db_engine)
    mock_cli = MagicMock()
    mock_cli.export_opf.side_effect = lambda _book_id, destination: destination.write_text(SAMPLE_OPF)
    mock_cli.export_cover.side_effect = lambda _book_id, destination: destination.write_bytes(b"old-cover") or True
    mock_cli.set_cover.side_effect = [RuntimeError("cover write failed"), None]

    def restored_metadata(_book_id: int) -> dict[str, Any]:
        backup_cover = next((tmp_path / "backups" / "1").glob("before_*.cover"))
        return {
            "title": "Old Title",
            "identifiers": {"isbn": ISBN},
            "#edition": "Old edition",
            "cover": str(backup_cover),
        }

    mock_cli.show_metadata.side_effect = restored_metadata
    cover = tmp_path / "covers" / "new.jpg"
    cover.parent.mkdir()
    Image.new("RGB", (10, 10), "red").save(cover)
    book = BookRecord(
        book_key="calibre:1",
        run_id="test_run",
        calibre_book_id=1,
        source="calibre",
        current_metadata={"title": "Old Title", "identifiers": {"isbn": ISBN}, "#edition": "Old edition"},
    )

    with Session(db_engine) as write_session, pytest.raises(RuntimeError, match="cover write failed"):
        ApplyEngine(mock_cli, tmp_path).apply_patch(
            write_session,
            book,
            {
                "edition_statement": "First edition",
                "cover": {
                    "artifact_path": str(cover),
                    "artifact_sha256": hashlib.sha256(cover.read_bytes()).hexdigest(),
                    "manifestation_isbn": ISBN,
                },
            },
        )

    assert mock_cli.set_metadata.call_count == 2
    assert mock_cli.set_custom.call_args_list[-1].args == (1, "#edition", "Old edition")
    assert mock_cli.set_cover.call_count == 2
    assert "before_" in mock_cli.set_cover.call_args_list[-1].args[1].name
    with Session(db_engine) as read_session:
        assert read_session.exec(select(Change)).one().status == "failed_rolled_back"


def test_undo_rejects_tampered_opf_before_any_restore_write(session: Any, tmp_path: Path) -> None:
    mock_cli = MagicMock()
    engine = ApplyEngine(mock_cli, tmp_path)
    backup = tmp_path / "backup.opf"
    backup.write_text("original backup")
    change = Change(
        book_key="calibre:1",
        run_id="test_run",
        before_metadata={},
        after_metadata={"title": "Changed"},
        backup_opf_path=str(backup),
        backup_opf_sha256=hashlib.sha256(backup.read_bytes()).hexdigest(),
        status="applied",
    )
    session.add(change)
    session.commit()
    backup.write_text("tampered")

    with pytest.raises(RuntimeError, match="hash mismatch"):
        engine.undo_change(session, change)

    mock_cli.set_metadata.assert_not_called()
    assert change.status == "applied"

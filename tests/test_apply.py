from pathlib import Path
from typing import Any
from unittest.mock import MagicMock

import pytest
from sqlmodel import Session, SQLModel, create_engine, select

from calibre_ai_auditor.apply.engine import ApplyEngine
from calibre_ai_auditor.storage.models import BookRecord, Change


@pytest.fixture
def session() -> Any:
    engine = create_engine("sqlite:///:memory:")
    SQLModel.metadata.create_all(engine)
    with Session(engine) as session:
        yield session


def test_apply_patch_backup(session: Any, tmp_path: Any) -> None:
    mock_cli = MagicMock()
    mock_cli.export_opf.side_effect = lambda _book_id, destination: destination.write_text("original metadata")
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
        destination.write_text("original metadata")

    mock_cli.export_opf.side_effect = export_opf
    mock_cli._run_command.side_effect = [None, RuntimeError("second field failed")]
    engine = ApplyEngine(mock_cli, tmp_path)
    book = BookRecord(
        book_key="calibre:1",
        run_id="test_run",
        calibre_book_id=1,
        source="calibre",
        current_metadata={"title": "Old Title", "publisher": "Old Publisher"},
    )

    with Session(db_engine) as write_session:
        with pytest.raises(RuntimeError, match="second field failed"):
            engine.apply_patch(
                write_session,
                book,
                {"title": "New Title", "publisher": "New Publisher"},
            )
        write_session.rollback()

    with Session(db_engine) as read_session:
        failed_change = read_session.exec(select(Change)).one()
        assert failed_change.status == "failed_rolled_back"
        mock_cli.set_metadata.assert_called_once_with(1, Path(failed_change.backup_opf_path))

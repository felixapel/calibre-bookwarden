"""Scan must not re-query calibredb per book.

list_books --fields all already returns full per-book metadata; calling
show_metadata per book re-scans the whole library N times (N subprocesses).
"""

from pathlib import Path
from typing import Any
from unittest.mock import patch

from sqlmodel import Session, SQLModel
from typer.testing import CliRunner

from calibre_ai_auditor.cli.main import app
from calibre_ai_auditor.config.settings import Settings
from calibre_ai_auditor.storage.db import get_engine
from calibre_ai_auditor.storage.models import BookRecord

runner = CliRunner()


class _CountingFakeCLI:
    show_metadata_calls = 0

    def __init__(self, *_args: Any, **_kwargs: Any) -> None:
        pass

    def list_books(self, *_args: Any, **_kwargs: Any) -> list[dict[str, Any]]:
        return [
            {
                "id": i,
                "title": f"Book {i}",
                "authors": ["Author"],
                "formats": [f"/books/{i}/book.epub"],
                "publisher": "Press",
            }
            for i in (1, 2, 3)
        ]

    def show_metadata(self, book_id: int) -> dict[str, Any]:
        type(self).show_metadata_calls += 1
        return {"id": book_id, "title": f"Book {book_id}"}


def test_scan_reuses_list_rows_without_per_book_subprocess(tmp_path: Path) -> None:
    _CountingFakeCLI.show_metadata_calls = 0
    settings = Settings(
        library={"path": tmp_path},
        storage={"sqlite_path": tmp_path / "test.db"},
    )
    engine = get_engine(settings)
    SQLModel.metadata.create_all(engine)

    # The app callback rebuilds settings via load_settings, so patch it too.
    with (
        patch("calibre_ai_auditor.cli.main.init_db", return_value=None),
        patch("calibre_ai_auditor.cli.main.CalibreCLI", _CountingFakeCLI),
        patch("calibre_ai_auditor.cli.main.load_settings", return_value=settings),
    ):
        result = runner.invoke(app, ["scan"])

    assert result.exit_code == 0, result.stdout
    assert _CountingFakeCLI.show_metadata_calls == 0
    with Session(engine) as session:
        from sqlmodel import select

        records = session.exec(select(BookRecord)).all()
        assert len(records) == 3
        titles = sorted(r.current_metadata["title"] for r in records)
        assert titles == ["Book 1", "Book 2", "Book 3"]
        assert all(r.status == "scanned" for r in records)

import shutil
import subprocess
from pathlib import Path

import pytest
from sqlmodel import Session, SQLModel, create_engine

from calibre_ai_auditor.apply.engine import ApplyEngine
from calibre_ai_auditor.calibre.cli import CalibreCLI
from calibre_ai_auditor.storage.models import BookRecord


@pytest.mark.skipif(shutil.which("calibredb") is None, reason="calibredb is not installed")
def test_real_calibre_apply_and_undo_round_trip(tmp_path: Path) -> None:
    library = tmp_path / "library"
    library.mkdir()
    subprocess.run(
        [
            "calibredb",
            "add",
            "--empty",
            "--title",
            "Original Title",
            "--authors",
            "Original Author",
            "--with-library",
            str(library),
        ],
        check=True,
        capture_output=True,
        text=True,
    )

    cli = CalibreCLI(library)
    before = cli.show_metadata(1)
    db_engine = create_engine(f"sqlite:///{tmp_path / 'changes.db'}")
    SQLModel.metadata.create_all(db_engine)
    book = BookRecord(
        book_key="calibre:1",
        run_id="calibre-integration",
        calibre_book_id=1,
        source="calibre",
        current_metadata={"title": before["title"]},
    )

    with Session(db_engine) as session:
        change = ApplyEngine(cli, tmp_path / "artifacts").apply_patch(
            session,
            book,
            {"title": "Verified Title", "publisher": "Verified Publisher"},
        )
        after = cli.show_metadata(1)
        assert after["title"] == "Verified Title"
        assert after["publisher"] == "Verified Publisher"
        assert Path(change.backup_opf_path).is_file()

        ApplyEngine(cli, tmp_path / "artifacts").undo_change(session, change)
        restored = cli.show_metadata(1)
        assert restored["title"] == "Original Title"
        assert restored.get("publisher", "") in ("", None)

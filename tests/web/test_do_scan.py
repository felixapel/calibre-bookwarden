"""Web do_scan must reuse listed rows: no per-book show_metadata, no N+1 selects."""

from pathlib import Path
from typing import Any
from unittest.mock import patch

import pytest
from sqlmodel import Session, SQLModel, select

from calibre_ai_auditor.config.settings import Settings
from calibre_ai_auditor.storage.db import get_engine
from calibre_ai_auditor.storage.models import BookRecord
from calibre_ai_auditor.web.api.runs import ScanRequest, do_scan


class _CountingFakeCLI:
    show_metadata_calls = 0

    def __init__(self, *_args: Any, **_kwargs: Any) -> None:
        pass

    def list_books(self, *_args: Any, **_kwargs: Any) -> list[dict[str, Any]]:
        return [
            {
                "id": i,
                "title": f"Book {i}",
                "authors": "Author",
                "formats": [f"/books/{i}/book.epub"],
                "identifiers": {},
                "publisher": "Press",
                "pubdate": "2020-01-01",
                "languages": ["en"],
                "series": "Saga",
                "series_index": float(i),
                "tags": ["fiction"],
            }
            for i in (1, 2, 3)
        ]

    def show_metadata(self, book_id: int) -> dict[str, Any]:
        type(self).show_metadata_calls += 1
        return {"id": book_id}


@pytest.mark.asyncio
async def test_do_scan_enriches_from_listed_rows(tmp_path: Path) -> None:
    _CountingFakeCLI.show_metadata_calls = 0
    settings = Settings(
        library={"path": tmp_path},
        storage={"sqlite_path": tmp_path / "test.db"},
    )
    engine = get_engine(settings)
    SQLModel.metadata.create_all(engine)

    with patch("calibre_ai_auditor.web.api.runs.CalibreCLI", _CountingFakeCLI):
        result = await do_scan(settings, ScanRequest())

    assert result["books_found"] == 3
    assert _CountingFakeCLI.show_metadata_calls == 0
    with Session(engine) as session:
        records = session.exec(select(BookRecord)).all()
        assert len(records) == 3
        first = session.exec(select(BookRecord).where(BookRecord.book_key == "calibre:1")).one()
        assert first.current_metadata["publisher"] == "Press"
        assert first.current_metadata["language"] == "en"
        assert first.current_metadata["series"] == "Saga"
        assert first.current_metadata["tags"] == ["fiction"]

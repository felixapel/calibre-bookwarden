from pathlib import Path
from unittest.mock import AsyncMock, patch

import pytest

from calibre_ai_auditor.config.settings import Settings
from calibre_ai_auditor.ingest.single_file import audit_ingested_file


@pytest.mark.asyncio
async def test_audit_ingested_file(tmp_path: Path) -> None:
    import calibre_ai_auditor.storage.db as db_module

    epub = tmp_path / "sample.epub"
    epub.write_bytes(b"not a real epub")

    settings = Settings()
    settings.database.backend = "sqlite"
    settings.storage.sqlite_path = tmp_path / "test.db"
    settings.storage.artifacts_dir = tmp_path / "artifacts"

    db_module._engine = None
    from calibre_ai_auditor.storage.db import init_db

    init_db(settings)

    with patch(
        "calibre_ai_auditor.ingest.single_file.run_audit",
        new_callable=AsyncMock,
    ) as mock_audit:
        result = await audit_ingested_file(settings, epub)

    assert "run_ingest_" in result["run_id"]
    assert result["book_key"] == f"path:{epub.resolve()}"
    mock_audit.assert_called_once()
    db_module._engine = None

from pathlib import Path
from unittest.mock import MagicMock, patch

import pytest

from calibre_ai_auditor.extractors.tika_client import TikaClient


@pytest.mark.asyncio
async def test_tika_disabled() -> None:
    client = TikaClient(enabled=False)
    assert await client.extract_text(Path("dummy.pdf")) is None
    assert await client.extract_metadata(Path("dummy.pdf")) is None
    assert await client.test_connection() is False


@pytest.mark.asyncio
async def test_tika_extract_text(tmp_path: Path) -> None:
    client = TikaClient(enabled=True)
    test_file = tmp_path / "test.pdf"
    test_file.write_text("dummy")

    with patch("httpx.AsyncClient.put") as mock_put:
        mock_response = MagicMock()
        mock_response.text = "extracted text"
        mock_put.return_value = mock_response

        text = await client.extract_text(test_file)
        assert text == "extracted text"
        mock_put.assert_called_once()

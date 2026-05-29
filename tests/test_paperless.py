import os
import tempfile
from pathlib import Path
from unittest.mock import AsyncMock, MagicMock, patch

import pytest

from calibre_ai_auditor.config.settings import Settings
from calibre_ai_auditor.integrations.paperless import PaperlessBridge


@pytest.mark.asyncio
async def test_paperless_bridge_disabled() -> None:
    settings = Settings()
    settings.paperless.enabled = False

    bridge = PaperlessBridge(settings)
    assert await bridge.test_connection() is False
    assert await bridge.get_document_types() == {}
    assert await bridge.fetch_candidate_documents() == []
    assert await bridge.download_document_file(123, Path("/tmp")) is None


@pytest.mark.asyncio
async def test_paperless_bridge_test_connection_success() -> None:
    settings = Settings()
    settings.paperless.enabled = True
    settings.paperless.base_url = "http://fake-paperless"

    with patch.dict(os.environ, {"PAPERLESS_TOKEN": "secret-token"}):
        bridge = PaperlessBridge(settings)

        # Mock httpx.AsyncClient GET request
        mock_response = MagicMock()
        mock_response.raise_for_status = MagicMock()
        mock_response.json = MagicMock(return_value={"results": []})

        mock_client = MagicMock()
        mock_client.get = AsyncMock(return_value=mock_response)
        mock_client.__aenter__ = AsyncMock(return_value=mock_client)
        mock_client.__aexit__ = AsyncMock(return_value=None)

        with patch("httpx.AsyncClient", return_value=mock_client):
            res = await bridge.test_connection()
            assert res is True
            mock_client.get.assert_called_once_with(
                "http://fake-paperless/api/documents/",
                headers={"Authorization": "Token secret-token"},
                params={"page_size": 1},
            )


@pytest.mark.asyncio
async def test_paperless_bridge_fetch_candidates() -> None:
    settings = Settings()
    settings.paperless.enabled = True
    settings.paperless.base_url = "http://fake-paperless"
    settings.paperless.import_document_types = ["book_scan"]

    with patch.dict(os.environ, {"PAPERLESS_TOKEN": "secret-token"}):
        bridge = PaperlessBridge(settings)

        # Mock document types request
        mock_types_response = MagicMock()
        mock_types_response.raise_for_status = MagicMock()
        mock_types_response.json = MagicMock(
            return_value={"results": [{"id": 42, "name": "book_scan"}]}
        )

        # Mock documents list request
        mock_docs_response = MagicMock()
        mock_docs_response.raise_for_status = MagicMock()
        mock_docs_response.json = MagicMock(
            return_value={"results": [{"id": 101, "title": "Scanned Book 1"}]}
        )

        # A custom client that routes calls to different mocks
        mock_client = MagicMock()
        mock_client.__aenter__ = AsyncMock(return_value=mock_client)
        mock_client.__aexit__ = AsyncMock(return_value=None)

        async def get_mock(url, **kwargs):
            if "document_types" in url:
                return mock_types_response
            elif "documents" in url:
                return mock_docs_response
            raise ValueError(f"Unexpected url: {url}")

        mock_client.get = AsyncMock(side_effect=get_mock)

        with patch("httpx.AsyncClient", return_value=mock_client):
            docs = await bridge.fetch_candidate_documents()
            assert len(docs) == 1
            assert docs[0]["id"] == 101
            assert docs[0]["title"] == "Scanned Book 1"


@pytest.mark.asyncio
async def test_paperless_bridge_download() -> None:
    settings = Settings()
    settings.paperless.enabled = True
    settings.paperless.base_url = "http://fake-paperless"

    with patch.dict(os.environ, {"PAPERLESS_TOKEN": "secret-token"}):
        bridge = PaperlessBridge(settings)

        mock_stream_response = MagicMock()
        mock_stream_response.raise_for_status = MagicMock()
        mock_stream_response.headers = {
            "content-disposition": 'attachment; filename="my_scanned_book.pdf"'
        }

        # Mock stream chunks
        async def aiter_bytes():
            yield b"pdf_data_bytes"

        mock_stream_response.aiter_bytes = aiter_bytes

        mock_client = MagicMock()
        mock_client.stream = MagicMock()
        mock_client.stream.return_value.__aenter__ = AsyncMock(return_value=mock_stream_response)
        mock_client.stream.return_value.__aexit__ = AsyncMock(return_value=None)
        mock_client.__aenter__ = AsyncMock(return_value=mock_client)
        mock_client.__aexit__ = AsyncMock(return_value=None)

        with (
            patch("httpx.AsyncClient", return_value=mock_client),
            tempfile.TemporaryDirectory() as tmp_dir,
        ):
            file_path = await bridge.download_document_file(101, Path(tmp_dir))
            assert file_path is not None
            assert file_path.name == "my_scanned_book.pdf"
            assert file_path.read_bytes() == b"pdf_data_bytes"

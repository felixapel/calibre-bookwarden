from unittest.mock import AsyncMock, MagicMock, patch

import pytest

from calibre_ai_auditor.config.settings import Settings
from calibre_ai_auditor.providers.testing import probe_provider_connectivity


@pytest.mark.asyncio
async def test_openlibrary_probe() -> None:
    settings = Settings()
    mock_response = MagicMock()
    mock_response.raise_for_status = MagicMock()
    mock_response.json = MagicMock(return_value={"numFound": 3})

    mock_client = MagicMock()
    mock_client.get = AsyncMock(return_value=mock_response)
    mock_client.__aenter__ = AsyncMock(return_value=mock_client)
    mock_client.__aexit__ = AsyncMock(return_value=None)

    with patch("calibre_ai_auditor.providers.testing.httpx.AsyncClient", return_value=mock_client):
        result = await probe_provider_connectivity(settings, "openlibrary")

    assert result["ok"] is True
    assert "openlibrary" in result["provider"]

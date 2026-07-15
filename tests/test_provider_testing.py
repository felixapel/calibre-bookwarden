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


@pytest.mark.asyncio
async def test_komf_probe_fails_when_provider_returns_no_candidates() -> None:
    settings = Settings()
    provider = MagicMock()
    provider.fetch_candidates = AsyncMock(return_value=[])
    registry = MagicMock()
    registry.providers = {"komf": provider}

    with patch("calibre_ai_auditor.providers.testing.ProviderRegistry", return_value=registry) as registry_cls:
        result = await probe_provider_connectivity(settings, "komf")

    assert result == {
        "provider": "komf",
        "ok": False,
        "message": "Provider returned no candidates for probe query",
    }
    registry_cls.assert_called_once()
    assert registry_cls.call_args.kwargs == {"enable_komf": True}

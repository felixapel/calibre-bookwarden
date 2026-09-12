"""Provider response cache: identical GETs collapse to one network call."""

from unittest.mock import AsyncMock, MagicMock, patch

import pytest

from calibre_ai_auditor.providers import cache as cache_module
from calibre_ai_auditor.providers.cache import cached_get_json, clear_provider_cache


def _client_with(payload):
    resp = MagicMock()
    resp.json.return_value = payload
    resp.raise_for_status.return_value = None
    client = MagicMock()
    client.get = AsyncMock(return_value=resp)
    client.__aenter__ = AsyncMock(return_value=client)
    client.__aexit__ = AsyncMock(return_value=None)
    return client


@pytest.mark.asyncio
async def test_identical_gets_share_one_network_call():
    clear_provider_cache()
    client = _client_with({"docs": []})
    with patch("httpx.AsyncClient", return_value=client):
        first = await cached_get_json("https://example.test/s", params={"q": "x"})
        second = await cached_get_json("https://example.test/s", params={"q": "x"})
    assert first == {"docs": []}
    assert second == {"docs": []}
    assert client.get.await_count == 1
    clear_provider_cache()


@pytest.mark.asyncio
async def test_cache_evicts_oldest_when_full(monkeypatch):
    monkeypatch.setattr(cache_module, "_MAX_ENTRIES", 3)
    clear_provider_cache()
    client = _client_with({"docs": []})
    with patch("httpx.AsyncClient", return_value=client):
        for i in range(3):
            await cached_get_json("https://example.test/s", params={"q": str(i)})
        assert len(cache_module._CACHE) == 3
        await cached_get_json("https://example.test/s", params={"q": "new"})
        assert len(cache_module._CACHE) == 3
        keys = list(cache_module._CACHE)
        assert not any("q=0" in k for k in keys)
        assert any("q=new" in k for k in keys)
    clear_provider_cache()

"""Rate-limit pools are keyed by (url, timeout) and released on shutdown."""

from unittest.mock import AsyncMock, MagicMock, patch

import pytest

from calibre_ai_auditor.web import rate_limit
from calibre_ai_auditor.web.rate_limit import _pool_for, aclose_rate_limit_pools


def _fake_client() -> MagicMock:
    client = MagicMock()
    client.aclose = AsyncMock()
    return client


def test_pools_keyed_by_url_and_timeout() -> None:
    rate_limit._POOLS.clear()
    try:
        with patch("redis.asyncio.from_url", side_effect=[_fake_client(), _fake_client(), _fake_client()]):
            a = _pool_for("redis://x:6379", 1.0)
            b = _pool_for("redis://x:6379", 1.0)
            c = _pool_for("redis://x:6379", 2.0)
        assert a is b
        assert c is not a
        assert len(rate_limit._POOLS) == 2
    finally:
        rate_limit._POOLS.clear()


@pytest.mark.asyncio
async def test_aclose_drains_pools() -> None:
    clients = [_fake_client(), _fake_client()]
    rate_limit._POOLS.clear()
    rate_limit._POOLS[("redis://x:6379", 1.0)] = clients[0]
    rate_limit._POOLS[("redis://y:6379", 1.0)] = clients[1]
    await aclose_rate_limit_pools()
    assert rate_limit._POOLS == {}
    for client in clients:
        client.aclose.assert_awaited_once()

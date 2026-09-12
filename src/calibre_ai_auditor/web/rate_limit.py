"""Fail-closed production HTTP rate limiting backed by Valkey."""

import contextlib
from typing import Any

RATE_LIMIT_SCRIPT = """
local current = redis.call('INCR', KEYS[1])
if current == 1 then
  redis.call('EXPIRE', KEYS[1], ARGV[2])
end
if current > tonumber(ARGV[1]) then
  local ttl = redis.call('TTL', KEYS[1])
  if ttl < 1 then return 1 end
  return ttl
end
return 0
"""


_POOLS: dict[tuple[str, float], Any] = {}


def _pool_for(url: str, timeout: float) -> Any:
    """Shared connection pool per (URL, timeout): avoids per-request socket churn."""
    import redis.asyncio as aioredis

    key = (url, timeout)
    pool = _POOLS.get(key)
    if pool is None:
        pool = aioredis.from_url(
            url,
            decode_responses=True,
            socket_connect_timeout=timeout,
            socket_timeout=timeout,
            max_connections=20,
        )
        _POOLS[key] = pool
    return pool


async def aclose_rate_limit_pools() -> None:
    """Release pooled Redis connections (call from ASGI lifespan shutdown)."""
    while _POOLS:
        _, pool = _POOLS.popitem()
        with contextlib.suppress(Exception):
            await pool.aclose()


async def consume_rate_limit(url: str, identity: str, limit: int, window: int, timeout: float) -> int:
    """Consume one request and return retry seconds, or zero when allowed."""
    client = _pool_for(url, timeout)
    result = await client.eval(RATE_LIMIT_SCRIPT, 1, f"http-rate:{identity}", limit, window)
    return max(0, int(result))

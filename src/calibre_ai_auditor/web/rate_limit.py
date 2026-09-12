"""Fail-closed production HTTP rate limiting backed by Valkey."""

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


_POOLS: dict[str, Any] = {}


def _pool_for(url: str, timeout: float) -> Any:
    """Shared connection pool per URL: avoids per-request socket churn."""
    import redis.asyncio as aioredis

    pool = _POOLS.get(url)
    if pool is None:
        pool = aioredis.from_url(
            url,
            decode_responses=True,
            socket_connect_timeout=timeout,
            socket_timeout=timeout,
            max_connections=20,
        )
        _POOLS[url] = pool
    return pool


async def consume_rate_limit(url: str, identity: str, limit: int, window: int, timeout: float) -> int:
    """Consume one request and return retry seconds, or zero when allowed."""
    client = _pool_for(url, timeout)
    result = await client.eval(RATE_LIMIT_SCRIPT, 1, f"http-rate:{identity}", limit, window)
    return max(0, int(result))

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


async def consume_rate_limit(url: str, identity: str, limit: int, window: int, timeout: float) -> int:
    """Consume one request and return retry seconds, or zero when allowed."""
    import redis.asyncio as aioredis

    client: Any = aioredis.from_url(
        url,
        decode_responses=True,
        socket_connect_timeout=timeout,
        socket_timeout=timeout,
    )
    try:
        result = await client.eval(RATE_LIMIT_SCRIPT, 1, f"http-rate:{identity}", limit, window)
        return max(0, int(result))
    finally:
        await client.aclose()

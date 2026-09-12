"""Shared HTTP response cache for read-only provider GETs.

Why: OpenLibrary/GoogleBooks were creating a fresh AsyncClient per fetch with
zero caching, so auditing N books by the same ISBN/title paid N network
round-trips. This TTL cache keeps behavior identical (same URL+params ->
same parse) while collapsing repeats to memory.
"""

from __future__ import annotations

import asyncio
import logging
import time
from typing import Any

import httpx

logger = logging.getLogger(__name__)

_CACHE: dict[str, tuple[float, Any]] = {}
_LOCK = asyncio.Lock()
_DEFAULT_TTL_SECONDS = 300.0


def _key(url: str, params: dict[str, Any] | None) -> str:
    if not params:
        return url
    parts = "&".join(f"{k}={params[k]}" for k in sorted(params))
    return f"{url}?{parts}"


def clear_provider_cache() -> None:
    """Test/isolation hook: drop all cached provider responses."""
    _CACHE.clear()


async def cached_get_json(
    url: str,
    params: dict[str, Any] | None = None,
    *,
    timeout: float = 15.0,
    ttl: float = _DEFAULT_TTL_SECONDS,
) -> Any:
    """GET JSON with TTL memoization. Raises on transport/HTTP errors."""
    key = _key(url, params)
    now = time.monotonic()
    async with _LOCK:
        hit = _CACHE.get(key)
        if hit is not None and now - hit[0] < ttl:
            return hit[1]
    limits = httpx.Limits(max_connections=20, max_keepalive_connections=10)
    async with httpx.AsyncClient(timeout=timeout, limits=limits) as client:
        response = await client.get(url, params=params)
        response.raise_for_status()
        data = response.json()
    async with _LOCK:
        _CACHE[key] = (time.monotonic(), data)
    return data

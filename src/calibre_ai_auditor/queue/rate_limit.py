import logging
import time

from calibre_ai_auditor.queue.base import CacheBackend

logger = logging.getLogger(__name__)


class RateLimiter:
    def __init__(self, cache: CacheBackend, enabled: bool = True):
        self.cache = cache
        self.enabled = enabled

    async def record_cooldown(self, provider_name: str, seconds: int = 300) -> None:
        if not self.enabled:
            return
        logger.warning(f"Recording cooldown for provider {provider_name} ({seconds}s)")
        # Store the timestamp when the cooldown expires
        expire_at = time.time() + seconds
        await self.cache.set(f"cooldown_{provider_name}", expire_at, ttl=seconds)

    async def is_on_cooldown(self, provider_name: str) -> bool:
        if not self.enabled:
            return False

        expire_at = await self.cache.get(f"cooldown_{provider_name}")
        if not expire_at:
            return False

        return time.time() < float(expire_at)

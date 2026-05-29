class LockBackend:
    """Base class for distributed locks."""

    async def acquire(self, key: str, timeout: int = 60) -> bool:
        raise NotImplementedError

    async def release(self, key: str) -> None:
        raise NotImplementedError


class MemoryLock(LockBackend):
    """In-memory lock implementation for single-process setups."""

    def __init__(self) -> None:
        self.locks: set[str] = set()

    async def acquire(self, key: str, _timeout: int = 60) -> bool:
        if key in self.locks:
            return False
        self.locks.add(key)
        return True

    async def release(self, key: str) -> None:
        self.locks.discard(key)


def get_lock_manager(_backend: str = "memory") -> LockBackend:
    # In a full implementation, we'd check the settings and instantiate ValkeyLock
    return MemoryLock()

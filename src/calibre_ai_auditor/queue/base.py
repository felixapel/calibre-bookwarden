from abc import ABC, abstractmethod
from typing import Any


class JobQueue(ABC):
    @abstractmethod
    async def enqueue(self, task: str, payload: dict[str, Any]) -> str:
        """Add a job to the queue and return the job_id."""
        pass

    @abstractmethod
    async def get_status(self, job_id: str) -> dict[str, Any] | None:
        """Retrieve the status and result of a job."""
        pass

    @abstractmethod
    async def dequeue(self, timeout: int = 1) -> dict[str, Any] | None:
        """Pop a job from the queue."""
        pass

    @abstractmethod
    async def update_job(self, job_id: str, updates: dict[str, Any]) -> None:
        """Update job data."""
        pass



class CacheBackend(ABC):
    @abstractmethod
    async def get(self, key: str) -> Any | None:
        pass

    @abstractmethod
    async def set(self, key: str, value: Any, ttl: int | None = None) -> None:
        pass

    @abstractmethod
    async def delete(self, key: str) -> None:
        pass

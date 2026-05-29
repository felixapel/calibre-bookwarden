import uuid
from datetime import datetime, timezone
from typing import Any

from calibre_ai_auditor.queue.base import CacheBackend, JobQueue


class MemoryQueue(JobQueue):
    def __init__(self) -> None:
        self.jobs: dict[str, dict[str, Any]] = {}

    async def enqueue(self, task: str, payload: dict[str, Any]) -> str:
        job_id = str(uuid.uuid4())
        self.jobs[job_id] = {
            "job_id": job_id,
            "task": task,
            "status": "pending",
            "payload": payload,
            "created_at": datetime.now(timezone.utc).isoformat(),
        }
        # In a real async worker environment, we would trigger the task here.
        # For memory fallback, we just record it.
        return job_id

    async def get_status(self, job_id: str) -> dict[str, Any] | None:
        return self.jobs.get(job_id)


class MemoryCache(CacheBackend):
    def __init__(self) -> None:
        self.data: dict[str, Any] = {}

    async def get(self, key: str) -> Any | None:
        return self.data.get(key)

    async def set(self, key: str, value: Any, _ttl: int | None = None) -> None:
        self.data[key] = value

    async def delete(self, key: str) -> None:
        if key in self.data:
            del self.data[key]

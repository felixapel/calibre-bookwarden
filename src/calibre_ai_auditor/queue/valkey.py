import json
import logging
import uuid
from datetime import datetime
from typing import Any

from calibre_ai_auditor.queue.base import CacheBackend, JobQueue

logger = logging.getLogger(__name__)


class ValkeyQueue(JobQueue):
    """
    Queue implementation backed by Valkey/Redis.
    Falls back to in-memory dictionary if the 'redis' package is not installed or backend is memory.
    """

    def __init__(self, url: str, backend: str = "valkey") -> None:
        self.url = url
        self._client = None
        self._fallback_db: dict[str, dict[str, Any]] = {}
        self._fallback_queue: list[str] = []
        if backend == "valkey":
            try:
                import redis.asyncio as aioredis  # type: ignore
                self._client = aioredis.from_url(url, decode_responses=True)
                logger.info(f"ValkeyQueue initialized with URL: {url}")
            except ImportError:
                logger.warning(
                    "ValkeyQueue is falling back to in-memory behavior because the 'redis' package is not installed."
                )
            except Exception as e:
                logger.warning(
                    f"ValkeyQueue failed to initialize with URL {url}: {e}. Falling back to memory."
                )

    def _cleanup_memory_jobs(self) -> None:
        now = datetime.now()
        # 1. TTL eviction: completed/failed jobs older than 1 hour (3600 seconds)
        keys_to_remove = []
        for job_id, job in list(self._fallback_db.items()):
            if job.get("status") in ("completed", "failed"):
                updated_at_str = job.get("updated_at")
                if updated_at_str:
                    try:
                        updated_at = datetime.fromisoformat(updated_at_str)
                        age = (now - updated_at).total_seconds()
                        if age > 3600:
                            keys_to_remove.append(job_id)
                    except Exception:
                        pass
        for job_id in keys_to_remove:
            self._fallback_db.pop(job_id, None)

        # 2. Size-bounding: limit to 100 finished jobs
        if len(self._fallback_db) > 100:
            finished = [
                (jid, j) for jid, j in self._fallback_db.items()
                if j.get("status") in ("completed", "failed")
            ]
            finished.sort(key=lambda x: x[1].get("updated_at", ""))
            to_remove = len(self._fallback_db) - 100
            for i in range(min(to_remove, len(finished))):
                self._fallback_db.pop(finished[i][0], None)

    async def enqueue(self, task: str, payload: dict[str, Any]) -> str:
        self._cleanup_memory_jobs()
        job_id = str(uuid.uuid4())
        now_str = datetime.now().isoformat()
        job_data = {
            "job_id": job_id,
            "status": "pending",
            "task": task,
            "payload": payload,
            "progress": 0,
            "total": 0,
            "result": None,
            "error": None,
            "created_at": now_str,
            "updated_at": now_str,
        }
        
        if self._client:
            try:
                await self._client.set(f"job:{job_id}", json.dumps(job_data))
                await self._client.rpush("queue:jobs", job_id)
                return job_id
            except Exception as e:
                logger.error(f"Valkey enqueue failed: {e}. Falling back to memory.")
        
        self._fallback_db[job_id] = job_data
        self._fallback_queue.append(job_id)
        return job_id

    async def get_status(self, job_id: str) -> dict[str, Any] | None:
        self._cleanup_memory_jobs()
        if self._client:
            try:
                data = await self._client.get(f"job:{job_id}")
                if data:
                    return json.loads(data)
            except Exception as e:
                logger.error(f"Valkey get_status failed: {e}.")
        
        return self._fallback_db.get(job_id)

    async def dequeue(self, timeout: int = 1) -> dict[str, Any] | None:
        self._cleanup_memory_jobs()
        if self._client:
            try:
                res = await self._client.blpop("queue:jobs", timeout=timeout)
                if res:
                    job_id = res[1]
                    return await self.get_status(job_id)
            except Exception as e:
                logger.error(f"Valkey dequeue failed: {e}. Falling back to memory queue.")
        
        if self._fallback_queue:
            job_id = self._fallback_queue.pop(0)
            return self._fallback_db.get(job_id)
        return None

    async def update_job(self, job_id: str, updates: dict[str, Any]) -> None:
        self._cleanup_memory_jobs()
        job_data = await self.get_status(job_id)
        if not job_data:
            return
        job_data.update(updates)
        job_data["updated_at"] = datetime.now().isoformat()
        
        if self._client:
            try:
                # Set TTL of 3600 seconds if job is finished
                if job_data["status"] in ("completed", "failed"):
                    await self._client.set(f"job:{job_id}", json.dumps(job_data), ex=3600)
                else:
                    await self._client.set(f"job:{job_id}", json.dumps(job_data))
                return
            except Exception as e:
                logger.error(f"Valkey update_job failed: {e}.")
        
        self._fallback_db[job_id] = job_data


class ValkeyCache(CacheBackend):
    """
    Cache implementation backed by Valkey/Redis.
    Falls back to in-memory dictionary if the 'redis' package is not installed.
    """

    def __init__(self, url: str) -> None:
        self.url = url
        self._client = None
        self._fallback_db: dict[str, Any] = {}
        try:
            import redis.asyncio as aioredis  # type: ignore
            self._client = aioredis.from_url(url, decode_responses=True)
        except ImportError:
            pass

    async def get(self, key: str) -> Any | None:
        if self._client:
            try:
                val = await self._client.get(key)
                if val:
                    return json.loads(val)
            except Exception as e:
                logger.error(f"ValkeyCache get failed: {e}")
        return self._fallback_db.get(key)

    async def set(self, key: str, value: Any, ttl: int | None = None) -> None:
        if self._client:
            try:
                val_str = json.dumps(value)
                if ttl:
                    await self._client.set(key, val_str, ex=ttl)
                else:
                    await self._client.set(key, val_str)
                return
            except Exception as e:
                logger.error(f"ValkeyCache set failed: {e}")
        
        self._fallback_db[key] = value

    async def delete(self, key: str) -> None:
        if self._client:
            try:
                await self._client.delete(key)
                return
            except Exception as e:
                logger.error(f"ValkeyCache delete failed: {e}")
        self._fallback_db.pop(key, None)

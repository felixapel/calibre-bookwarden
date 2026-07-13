import asyncio
import contextlib
import logging
from collections.abc import Callable
from datetime import datetime
from pathlib import Path
from typing import Any

from pydantic import BaseModel

from calibre_ai_auditor.config.settings import Settings, load_settings
from calibre_ai_auditor.queue.valkey import ValkeyQueue

logger = logging.getLogger(__name__)


class JobStatus(BaseModel):
    job_id: str
    status: str  # pending, running, completed, failed
    task: str
    progress: int = 0
    total: int = 0
    result: Any | None = None
    error: str | None = None
    created_at: datetime
    updated_at: datetime


_queue: ValkeyQueue | None = None
_active_tasks: set[asyncio.Task[Any]] = set()
_worker_task: asyncio.Task[Any] | None = None


def get_queue() -> ValkeyQueue:
    global _queue
    if _queue is None:
        settings = load_settings()
        _queue = ValkeyQueue(settings.queue.valkey_url, backend=settings.queue.backend)
    return _queue


def serialize_arg(arg: Any) -> Any:
    if isinstance(arg, Settings):
        return {"__type__": "Settings"}
    elif isinstance(arg, Path):
        return {"__type__": "Path", "data": str(arg)}
    elif isinstance(arg, BaseModel):
        return {
            "__type__": "BaseModel",
            "model_name": arg.__class__.__name__,
            "data": arg.model_dump(),
        }
    else:
        return {"__type__": "raw", "data": arg}


def deserialize_arg(arg_data: dict[str, Any], settings: Settings) -> Any:
    t = arg_data.get("__type__")
    if t == "Settings":
        return settings
    elif t == "Path":
        return Path(arg_data["data"])
    elif t == "BaseModel":
        model_name = arg_data["model_name"]
        data = arg_data["data"]
        if model_name == "ScanRequest":
            from calibre_ai_auditor.web.api.runs import ScanRequest

            return ScanRequest(**data)
        raise ValueError(f"Unknown BaseModel model_name: {model_name}")
    elif t == "raw":
        return arg_data["data"]
    else:
        return arg_data


def serialize_kwargs(kwargs: dict[str, Any]) -> dict[str, Any]:
    return {k: serialize_arg(v) for k, v in kwargs.items()}


def deserialize_kwargs(kwargs_data: dict[str, Any], settings: Settings) -> dict[str, Any]:
    return {k: deserialize_arg(v, settings) for k, v in kwargs_data.items()}


def make_progress_callback(job_id: str, queue: ValkeyQueue) -> Callable[[int, int], None]:
    def cb(current: int, total: int) -> None:
        asyncio.create_task(queue.update_job(job_id, {"progress": current, "total": total}))

    return cb


async def run_job_from_payload(job_id: str, job_data: dict[str, Any], settings: Settings, queue: ValkeyQueue) -> None:
    from calibre_ai_auditor.audit.engine import run_audit
    from calibre_ai_auditor.ingest.single_file import audit_ingested_file
    from calibre_ai_auditor.web.api.bridges import do_paperless_webhook_audit
    from calibre_ai_auditor.web.api.runs import do_scan

    payload = job_data.get("payload", {})
    func_name = payload.get("func_name")
    args_data = payload.get("args", [])
    kwargs_data = payload.get("kwargs", {})

    # 1. Update status to running
    await queue.update_job(job_id, {"status": "running"})

    # 2. Resolve function. The dispatch is dynamic (string → callable), so we
    # use a dict + .get() and cast the result to Any. The runtime contract
    # (func is one of these four) is enforced by the elif chain below.
    func: Any = {
        "do_scan": do_scan,
        "run_audit": run_audit,
        "do_paperless_webhook_audit": do_paperless_webhook_audit,
        "audit_ingested_file": audit_ingested_file,
    }.get(func_name)
    if func is None:
        err_msg = f"Unknown function name in payload: {func_name}"
        logger.error(err_msg)
        await queue.update_job(job_id, {"status": "failed", "error": err_msg})
        return

    # 3. Deserialize args and kwargs
    try:
        args = [deserialize_arg(arg, settings) for arg in args_data]
        kwargs = deserialize_kwargs(kwargs_data, settings)
    except Exception as e:
        err_msg = f"Deserialization failed: {e}"
        logger.error(err_msg)
        await queue.update_job(job_id, {"status": "failed", "error": err_msg})
        return

    # Pass progress callback if it's run_audit
    if func is run_audit:
        kwargs["progress_callback"] = make_progress_callback(job_id, queue)

    # 4. Execute the function
    try:
        if asyncio.iscoroutinefunction(func):
            res = await func(*args, **kwargs)
        else:
            res = func(*args, **kwargs)

        await queue.update_job(
            job_id,
            {
                "status": "completed",
                "result": res,
            },
        )
    except Exception as e:
        logger.error(f"Job {job_id} failed: {e}", exc_info=True)
        await queue.update_job(
            job_id,
            {
                "status": "failed",
                "error": str(e),
            },
        )


async def start_job(task: str, func: Callable[..., Any], *args: Any, **kwargs: Any) -> str:
    # 1. Serialize arguments
    serialized_args = [serialize_arg(arg) for arg in args]
    serialized_kwargs = serialize_kwargs(kwargs)

    payload = {
        "func_name": func.__name__,
        "args": serialized_args,
        "kwargs": serialized_kwargs,
    }

    settings = load_settings()
    queue = get_queue()
    job_id = await queue.enqueue(task, payload)

    # If backend is memory, run immediately in a background task
    if settings.queue.backend != "valkey":
        job_data = await queue.get_status(job_id)
        if job_data:
            t = asyncio.create_task(run_job_from_payload(job_id, job_data, settings, queue))
            _active_tasks.add(t)
            t.add_done_callback(_active_tasks.discard)

    return job_id


async def get_job_status(job_id: str) -> JobStatus | None:
    queue = get_queue()
    data = await queue.get_status(job_id)
    if not data:
        return None

    created_at = data.get("created_at")
    if isinstance(created_at, str):
        try:
            created_at = datetime.fromisoformat(created_at)
        except ValueError:
            created_at = datetime.now()
    updated_at = data.get("updated_at")
    if isinstance(updated_at, str):
        try:
            updated_at = datetime.fromisoformat(updated_at)
        except ValueError:
            updated_at = datetime.now()

    return JobStatus(
        job_id=data["job_id"],
        status=data["status"],
        task=data["task"],
        progress=data.get("progress", 0),
        total=data.get("total", 0),
        result=data.get("result"),
        error=data.get("error"),
        created_at=created_at or datetime.now(),
        updated_at=updated_at or datetime.now(),
    )


async def worker_loop(settings: Settings) -> None:
    queue = get_queue()
    logger.info("Valkey background worker loop started.")
    while True:
        try:
            job_data = await queue.dequeue(timeout=1)
            if job_data:
                job_id = job_data["job_id"]
                # Only process if status is pending
                if job_data["status"] == "pending":
                    logger.info(f"Valkey worker picked up job {job_id}")
                    await run_job_from_payload(job_id, job_data, settings, queue)
        except asyncio.CancelledError:
            logger.info("Valkey background worker loop cancelled.")
            break
        except Exception as e:
            logger.error(f"Error in Valkey worker loop: {e}")
            await asyncio.sleep(1)


async def start_worker_task(settings: Settings) -> None:
    global _worker_task
    if _worker_task is not None:
        return
    # Only run workers if we are using the valkey backend
    if settings.queue.backend == "valkey":
        _worker_task = asyncio.create_task(worker_loop(settings))
        logger.info("Valkey background worker task started.")


async def stop_worker_task() -> None:
    global _worker_task
    if _worker_task:
        _worker_task.cancel()
        with contextlib.suppress(asyncio.CancelledError):
            await _worker_task
        _worker_task = None
        logger.info("Valkey background worker task stopped.")

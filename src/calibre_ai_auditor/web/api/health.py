import inspect
import shutil
from collections.abc import Awaitable
from typing import Any, cast

from fastapi import APIRouter, Depends, HTTPException, Response
from sqlalchemy import text

from calibre_ai_auditor.config.settings import Settings, load_settings
from calibre_ai_auditor.extractors.tika_client import TikaClient
from calibre_ai_auditor.llm.router import LLMRouter
from calibre_ai_auditor.storage.db import expected_schema_revision, get_engine
from calibre_ai_auditor.vectors.client import VectorClient
from calibre_ai_auditor.verification.metrics import get_metrics
from calibre_ai_auditor.web.schemas import APIResponse

router = APIRouter()


def get_settings() -> Settings:
    return load_settings()


@router.get("/health")
async def health_check() -> dict[str, str]:
    return {"status": "ok"}


@router.get("/health/live")
async def liveness_check() -> dict[str, str]:
    """Process liveness only; intentionally does not expose dependency state."""
    return {"status": "alive"}


@router.get("/health/ready")
async def readiness_check(settings: Settings = Depends(get_settings)) -> dict[str, Any]:
    """Check required production dependencies without probing optional sidecars."""
    checks: dict[str, dict[str, Any]] = {}

    production_contract_ok = (
        settings.database.backend == "postgres"
        and settings.queue.backend == "valkey"
        and settings.rate_limits.enabled
        and settings.rate_limits.backend == "valkey"
        and settings.library.read_only
        and not settings.allow_remote_file_upload
        and not settings.privacy.allow_remote_text
        and not settings.privacy.allow_remote_images
    )
    checks["production_contract"] = {"ok": production_contract_ok}

    library_ok = bool(settings.library.path and settings.library.path.is_dir())
    checks["library"] = {"ok": library_ok}

    try:
        with get_engine(settings).connect() as connection:
            connection.execute(text("SELECT 1"))
            revision = connection.execute(text("SELECT version_num FROM alembic_version")).scalar_one()
        schema_ok = revision == expected_schema_revision()
        checks["database"] = {"ok": schema_ok, "schema_revision": revision}
    except Exception as exc:
        checks["database"] = {"ok": False, "error": type(exc).__name__}

    try:
        import redis.asyncio as aioredis

        client = aioredis.from_url(
            settings.queue.valkey_url,
            decode_responses=True,
            socket_connect_timeout=settings.queue.connect_timeout_seconds,
            socket_timeout=settings.queue.connect_timeout_seconds,
        )
        try:
            ping_result = client.ping()
            if inspect.isawaitable(ping_result):
                await ping_result
            elif not ping_result:
                raise ConnectionError("Valkey ping returned false")
        finally:
            close_result = client.aclose()
            if inspect.isawaitable(close_result):
                await cast(Awaitable[bool], close_result)
        checks["valkey"] = {"ok": True}
    except Exception as exc:
        checks["valkey"] = {"ok": False, "error": type(exc).__name__}

    try:
        import redis.asyncio as aioredis

        rate_client = aioredis.from_url(
            settings.rate_limits.valkey_url,
            decode_responses=True,
            socket_connect_timeout=settings.queue.connect_timeout_seconds,
            socket_timeout=settings.queue.connect_timeout_seconds,
        )
        try:
            rate_ping = rate_client.ping()
            if inspect.isawaitable(rate_ping):
                await rate_ping
            elif not rate_ping:
                raise ConnectionError("Rate-limit Valkey ping returned false")
        finally:
            rate_close = rate_client.aclose()
            if inspect.isawaitable(rate_close):
                await cast(Awaitable[bool], rate_close)
        checks["rate_limit_valkey"] = {"ok": True}
    except Exception as exc:
        checks["rate_limit_valkey"] = {"ok": False, "error": type(exc).__name__}

    if settings.require_writer_ready:
        try:
            from calibre_ai_auditor.apply.heartbeat import heartbeat_is_fresh, read_writer_heartbeat

            heartbeat = read_writer_heartbeat(
                settings.queue.valkey_url,
                timeout=settings.queue.connect_timeout_seconds,
            )
            writer_ok = heartbeat_is_fresh(
                heartbeat,
                max_age_seconds=settings.writer_heartbeat_max_age_seconds,
            )
            checks["writer"] = {"ok": writer_ok}
        except Exception as exc:
            checks["writer"] = {"ok": False, "error": type(exc).__name__}

    if not all(check["ok"] for check in checks.values()):
        raise HTTPException(status_code=503, detail={"status": "not_ready", "checks": checks})
    return {"status": "ready", "checks": checks}


@router.get("/metrics")
async def prometheus_metrics(settings: Settings = Depends(get_settings)) -> Response:
    """Prometheus metrics, including durable queue and writer liveness state."""
    metrics = get_metrics()
    collection_ok = True

    try:
        from calibre_ai_auditor.apply.heartbeat import heartbeat_is_fresh, read_writer_heartbeat

        heartbeat = read_writer_heartbeat(
            settings.queue.valkey_url,
            timeout=settings.queue.connect_timeout_seconds,
        )
        metrics.set_writer_health(
            fresh=heartbeat_is_fresh(
                heartbeat,
                max_age_seconds=settings.writer_heartbeat_max_age_seconds,
            )
        )
    except Exception:
        collection_ok = False
        metrics.set_writer_health(fresh=False)

    try:
        outbox_depths = {"pending": 0, "processing": 0, "failed": 0, "published": 0}
        operation_depths = {"unknown": 0, "restore_failed": 0}
        change_depths = {"failed_rollback_failed": 0}
        with get_engine(settings).connect() as connection:
            for status, count in connection.execute(text("SELECT status, count(*) FROM outboxevent GROUP BY status")):
                outbox_depths[str(status)] = int(count)
            for state, count in connection.execute(text("SELECT state, count(*) FROM operationledger GROUP BY state")):
                operation_depths[str(state)] = int(count)
            for status, count in connection.execute(text('SELECT status, count(*) FROM "change" GROUP BY status')):
                change_depths[str(status)] = int(count)
        for status, count in outbox_depths.items():
            metrics.set_outbox_depth(status, count)
        for state, count in operation_depths.items():
            metrics.set_operation_depth(state, count)
        for status, count in change_depths.items():
            metrics.set_change_depth(status, count)
    except Exception:
        collection_ok = False

    metrics.gauge("bookaudit_operational_metrics_collection_success", float(collection_ok))
    body = metrics.render()
    return Response(content=body, media_type="text/plain; version=0.0.4")


@router.get("/doctor", response_model=APIResponse)
async def doctor_check(settings: Settings = Depends(get_settings)) -> dict[str, Any]:
    results: dict[str, Any] = {
        "dependencies": {},
        "connectivity": {},
    }

    # 1. System Tools
    tools = ["calibredb", "ebook-meta", "fetch-ebook-metadata", "ocrmypdf"]
    for tool in tools:
        path = shutil.which(tool)
        results["dependencies"][tool] = {"found": path is not None, "path": path}

    # 2. Sidecars

    # Tika
    tika = TikaClient(
        enabled=settings.extractors.tika.enabled,
        base_url=settings.extractors.tika.base_url,
    )
    results["connectivity"]["tika"] = {
        "enabled": settings.extractors.tika.enabled,
        "ok": await tika.test_connection(),
        "url": settings.extractors.tika.base_url,
    }

    # Qdrant
    qdrant = VectorClient(
        url=settings.vectors.qdrant_url,
        collection=settings.vectors.collection,
        enabled=settings.vectors.enabled,
    )
    results["connectivity"]["qdrant"] = {
        "enabled": settings.vectors.enabled,
        "ok": await qdrant.test_connection() if settings.vectors.enabled else False,
        "url": settings.vectors.qdrant_url,
    }

    # LLMs (via Router)
    router_llm = LLMRouter(settings)
    results["connectivity"]["llms"] = {}
    for name, provider in router_llm.providers.items():
        results["connectivity"]["llms"][name] = {
            "ok": await provider.test_connection(),
            "name": provider.name,
        }

    # v1.0: discover homelab inference hosts
    from calibre_ai_auditor.verification.host_registry import (
        HostRegistry,
        HostRegistryConfig,
        default_felix_homelab,
    )

    host_cfg = HostRegistryConfig(hosts=default_felix_homelab())
    host_reg = HostRegistry(host_cfg)
    try:
        await host_reg.health_check_all()
        results["connectivity"]["inference_hosts"] = host_reg.summary()
    finally:
        await host_reg.__aexit__(None, None, None)

    return {"status": "success", "data": results}

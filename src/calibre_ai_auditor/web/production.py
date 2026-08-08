"""Certificate A production application.

This module intentionally does not import :mod:`calibre_ai_auditor.web.app`.
The generic application retains development and historical compatibility
surfaces; Certificate A is a separate, fail-closed product boundary.
"""

from __future__ import annotations

import hmac
import logging
import os
import re
import time
from collections.abc import AsyncIterator, Callable
from contextlib import asynccontextmanager
from datetime import UTC, datetime
from pathlib import Path
from typing import Any, cast
from uuid import uuid4

from fastapi import FastAPI, HTTPException, Request, Response
from fastapi.responses import FileResponse, JSONResponse
from fastapi.staticfiles import StaticFiles
from sqlalchemy.engine import Engine
from starlette.middleware.base import RequestResponseEndpoint
from starlette.middleware.gzip import GZipMiddleware

from calibre_ai_auditor.config.settings import Settings, load_settings
from calibre_ai_auditor.storage.db import expected_schema_revision, get_engine
from calibre_ai_auditor.verification.metrics import get_metrics
from calibre_ai_auditor.web.api.production_review import router as production_review_router
from calibre_ai_auditor.web.api.production_verify import (
    _canonical_root,
    _configuration_errors,
)
from calibre_ai_auditor.web.api.production_verify import (
    router as production_verify_router,
)
from calibre_ai_auditor.web.observability import request_id_context

logger = logging.getLogger(__name__)

SettingsProvider = Callable[[], Settings]
EngineProvider = Callable[[Settings], Engine]

REQUEST_ID_PATTERN = re.compile(r"^[A-Za-z0-9._:-]{1,128}$")
REJECTED_API_KEYS = {
    "replace-with-at-least-32-random-characters",
    "change-me",
}
CONTENT_SECURITY_POLICY = (
    "default-src 'self'; base-uri 'self'; frame-ancestors 'none'; "
    "form-action 'self'; object-src 'none'; img-src 'self' data:; "
    "style-src 'self'; script-src 'self'; connect-src 'self'"
)
CERTIFICATE_A_RUN_STATUSES = (
    "pending",
    "inventorying",
    "running",
    "cancelling",
    "cancelled",
    "completed",
    "completed_with_errors",
    "failed",
    "source_changed",
    "blocked_recovery",
)
CERTIFICATE_A_ACTIVE_STATUSES = ("pending", "inventorying", "running", "cancelling")


def _api_key_is_strong(value: str) -> bool:
    return len(value) >= 32 and len(set(value)) >= 8 and value.lower() not in REJECTED_API_KEYS


def _default_static_dir() -> Path:
    configured = os.getenv("BOOKAUDIT_STATIC_DIR")
    if configured:
        return Path(configured)
    return Path(__file__).with_name("static")


def _settings(request: Request) -> Settings:
    provider = cast(SettingsProvider, request.app.state.settings_provider)
    return provider()


def _as_utc_datetime(value: Any) -> datetime | None:
    if value is None:
        return None
    if isinstance(value, str):
        value = datetime.fromisoformat(value.replace("Z", "+00:00"))
    if not isinstance(value, datetime):
        return None
    if value.tzinfo is None:
        return value.replace(tzinfo=UTC)
    return value.astimezone(UTC)


def create_production_app(
    *,
    settings_provider: SettingsProvider = load_settings,
    engine_provider: EngineProvider = get_engine,
    static_dir: Path | None = None,
) -> FastAPI:
    """Build the isolated Certificate A ASGI application."""

    @asynccontextmanager
    async def production_lifespan(runtime_app: FastAPI) -> AsyncIterator[None]:
        settings = settings_provider()
        expected_key = settings.api_key.get_secret_value() if settings.api_key else ""
        if _configuration_errors(settings) or not _api_key_is_strong(expected_key):
            raise RuntimeError("Certificate A production configuration is invalid")
        checker = runtime_app.state.database_readiness_checker
        checker(settings)
        yield

    application = FastAPI(
        title="Calibre AI Auditor Certificate A",
        version="1.2.1",
        docs_url=None,
        redoc_url=None,
        openapi_url=None,
        lifespan=production_lifespan,
    )
    application.state.settings_provider = settings_provider
    application.state.engine_provider = engine_provider
    initial_settings = settings_provider()
    expected_revision = expected_schema_revision()
    from calibre_ai_auditor.apply.heartbeat import library_root_sha256
    from calibre_ai_auditor.verification.heartbeat import read_verifier_heartbeat

    application.state.expected_schema_revision = expected_revision
    application.state.library_root_sha256 = (
        library_root_sha256(_canonical_root(initial_settings)) if initial_settings.library.path is not None else None
    )

    def check_database(settings: Settings) -> None:
        engine = engine_provider(settings)
        if engine.dialect.name != "postgresql":
            raise RuntimeError("Certificate A requires PostgreSQL")
        with engine.connect() as connection:
            role, revision = connection.exec_driver_sql(
                "SELECT current_user, (SELECT version_num FROM alembic_version)"
            ).one()
        if role != "bookaudit_app" or revision != expected_revision:
            raise RuntimeError("Certificate A app database binding is invalid")

    application.state.database_readiness_checker = check_database
    application.state.verifier_heartbeat_reader = lambda settings: read_verifier_heartbeat(
        settings.queue.valkey_url,
        timeout=settings.queue.connect_timeout_seconds,
    )
    metrics_registry = get_metrics()
    metrics_registry.set_certificate_a_ready(ready=False)
    metrics_registry.set_verifier_health(fresh=False)
    metrics_registry.set_certificate_a_metrics_collection(success=False)
    metrics_registry.set_certificate_a_oldest_active_heartbeat_age(0.0)
    for run_status in CERTIFICATE_A_RUN_STATUSES:
        metrics_registry.set_certificate_a_run_depth(run_status, 0)
    application.add_middleware(GZipMiddleware, minimum_size=1_000)

    @application.middleware("http")
    async def measure_http_requests(request: Request, call_next: RequestResponseEndpoint) -> Response:
        if request.url.path == "/api/metrics":
            return await call_next(request)
        started = time.perf_counter()
        status = 500
        try:
            response: Response = await call_next(request)
            status = response.status_code
            return response
        finally:
            route = request.scope.get("route")
            route_path = getattr(route, "path", "unmatched")
            get_metrics().record_http_request(
                request.method,
                route_path,
                status,
                time.perf_counter() - started,
            )

    @application.middleware("http")
    async def add_security_headers(request: Request, call_next: RequestResponseEndpoint) -> Response:
        response: Response = await call_next(request)
        response.headers["Content-Security-Policy"] = CONTENT_SECURITY_POLICY
        response.headers["X-Content-Type-Options"] = "nosniff"
        response.headers["X-Frame-Options"] = "DENY"
        response.headers["Referrer-Policy"] = "no-referrer"
        response.headers["Permissions-Policy"] = "camera=(), microphone=(), geolocation=()"
        response.headers["Strict-Transport-Security"] = "max-age=31536000; includeSubDomains"
        return response

    @application.middleware("http")
    async def correlate_request(request: Request, call_next: RequestResponseEndpoint) -> Response:
        supplied = request.headers.get("X-Request-ID", "")
        request_id = supplied if REQUEST_ID_PATTERN.fullmatch(supplied) else str(uuid4())
        token = request_id_context.set(request_id)
        try:
            response: Response = await call_next(request)
            response.headers["X-Request-ID"] = request_id
            return response
        finally:
            request_id_context.reset(token)

    @application.middleware("http")
    async def add_cache_control(request: Request, call_next: RequestResponseEndpoint) -> Response:
        response: Response = await call_next(request)
        if request.url.path.startswith("/assets/"):
            response.headers["Cache-Control"] = "public, max-age=31536000, immutable"
        elif request.url.path.startswith("/api/"):
            response.headers["Cache-Control"] = "no-store, no-cache, must-revalidate, max-age=0"
        else:
            response.headers["Cache-Control"] = "no-cache, must-revalidate"
        return response

    @application.middleware("http")
    async def enforce_api_key(request: Request, call_next: RequestResponseEndpoint) -> Response:
        is_api = request.url.path == "/api" or request.url.path.startswith("/api/")
        if not is_api or request.url.path == "/api/health/live":
            return await call_next(request)

        settings = _settings(request)
        expected = settings.api_key.get_secret_value() if settings.api_key else ""
        if not _api_key_is_strong(expected):
            detail = (
                "API authentication is not configured"
                if not expected
                else "API authentication is not securely configured"
            )
            return JSONResponse(status_code=503, content={"detail": detail})
        provided = request.headers.get("X-API-Key", "")
        if not hmac.compare_digest(provided, expected):
            return JSONResponse(status_code=401, content={"detail": "Invalid or missing API Key"})
        return await call_next(request)

    @application.middleware("http")
    async def enforce_trusted_host(request: Request, call_next: RequestResponseEndpoint) -> Response:
        settings = _settings(request)
        allowed = {host.strip().lower() for host in settings.trusted_hosts.split(",") if host.strip()}
        hostname = (request.url.hostname or "").lower()
        if settings.profile == "production" and hostname not in allowed:
            return JSONResponse(status_code=400, content={"detail": "Invalid host header"})
        return await call_next(request)

    @application.middleware("http")
    async def enforce_rate_limit(request: Request, call_next: RequestResponseEndpoint) -> Response:
        is_api = request.url.path == "/api" or request.url.path.startswith("/api/")
        if not is_api or request.url.path == "/api/health/live":
            return await call_next(request)
        settings = _settings(request)
        if settings.profile != "production":
            return await call_next(request)
        if not settings.rate_limits.enabled or settings.rate_limits.backend != "valkey":
            return JSONResponse(status_code=503, content={"detail": "API rate limiting is not configured"})

        from calibre_ai_auditor.web.rate_limit import consume_rate_limit

        consumer = getattr(request.app.state, "rate_limit_consumer", consume_rate_limit)
        identity = request.client.host if request.client is not None else "unknown"
        try:
            retry_after = await consumer(
                settings.rate_limits.valkey_url,
                identity,
                settings.rate_limits.requests_per_window,
                settings.rate_limits.window_seconds,
                settings.queue.connect_timeout_seconds,
            )
        except Exception:
            logger.exception("certificate_a_rate_limiter_unavailable")
            return JSONResponse(status_code=503, content={"detail": "API rate limiting is unavailable"})
        if retry_after:
            return JSONResponse(
                status_code=429,
                headers={"Retry-After": str(retry_after)},
                content={"detail": "API request rate exceeded"},
            )
        return await call_next(request)

    @application.get("/api/health/live")
    async def liveness() -> dict[str, str]:
        return {"status": "alive"}

    @application.get("/api/health/ready")
    async def readiness(request: Request) -> dict[str, Any]:
        settings = _settings(request)
        runtime_metrics = get_metrics()
        contract_errors = _configuration_errors(settings)
        if (
            contract_errors
            or not settings.rate_limits.enabled
            or settings.rate_limits.backend != "valkey"
            or not _api_key_is_strong(settings.api_key.get_secret_value() if settings.api_key else "")
        ):
            runtime_metrics.set_certificate_a_ready(ready=False)
            raise HTTPException(
                status_code=503,
                detail={"status": "not_ready", "code": "configuration_invalid"},
            )
        try:
            checker = request.app.state.database_readiness_checker
            checker(settings)
        except Exception:
            logger.exception("certificate_a_database_not_ready")
            runtime_metrics.set_certificate_a_ready(ready=False)
            raise HTTPException(
                status_code=503,
                detail={"status": "not_ready", "code": "database_unavailable"},
            ) from None

        from calibre_ai_auditor.verification.heartbeat import verifier_heartbeat_is_fresh

        try:
            reader = request.app.state.verifier_heartbeat_reader
            heartbeat = reader(settings)
            root_sha256 = library_root_sha256(_canonical_root(settings))
            verifier_ready = verifier_heartbeat_is_fresh(
                heartbeat,
                release_digest=settings.release_digest or "",
                alembic_revision=expected_revision,
                library_root_sha256=root_sha256,
                max_age_seconds=settings.verifier.heartbeat_max_age_seconds,
            )
        except Exception:
            logger.exception("certificate_a_verifier_readiness_failed")
            verifier_ready = False
        runtime_metrics.set_verifier_health(fresh=verifier_ready)
        if not verifier_ready:
            runtime_metrics.set_certificate_a_ready(ready=False)
            raise HTTPException(
                status_code=503,
                detail={"status": "not_ready", "code": "verifier_unavailable"},
            )
        runtime_metrics.set_certificate_a_ready(ready=True)
        return {"status": "ready", "certificate": "A"}

    @application.get("/api/metrics")
    async def metrics(request: Request) -> Response:
        settings = _settings(request)
        runtime_metrics = get_metrics()
        collection_ok = True
        configuration_ready = not (
            _configuration_errors(settings)
            or not settings.rate_limits.enabled
            or settings.rate_limits.backend != "valkey"
            or not _api_key_is_strong(settings.api_key.get_secret_value() if settings.api_key else "")
        )

        try:
            checker = request.app.state.database_readiness_checker
            checker(settings)
            database_ready = True
        except Exception:
            logger.exception("certificate_a_database_readiness_metrics_failed")
            database_ready = False
            collection_ok = False

        try:
            reader = request.app.state.verifier_heartbeat_reader
            heartbeat = reader(settings)
            root_sha256 = library_root_sha256(_canonical_root(settings))
            from calibre_ai_auditor.verification.heartbeat import verifier_heartbeat_is_fresh

            verifier_fresh = verifier_heartbeat_is_fresh(
                heartbeat,
                release_digest=settings.release_digest or "",
                alembic_revision=expected_revision,
                library_root_sha256=root_sha256,
                max_age_seconds=settings.verifier.heartbeat_max_age_seconds,
            )
        except Exception:
            logger.exception("certificate_a_verifier_metrics_failed")
            verifier_fresh = False
            collection_ok = False
        runtime_metrics.set_verifier_health(fresh=verifier_fresh)
        runtime_metrics.set_certificate_a_ready(ready=configuration_ready and database_ready and verifier_fresh)

        for run_status in CERTIFICATE_A_RUN_STATUSES:
            runtime_metrics.set_certificate_a_run_depth(run_status, 0)
        runtime_metrics.set_certificate_a_oldest_active_heartbeat_age(0.0)
        try:
            engine = request.app.state.engine_provider(settings)
            with engine.connect() as connection:
                rows = connection.exec_driver_sql(
                    """
                    SELECT status, COUNT(*)
                    FROM verificationrun
                    WHERE contract_version = 'certificate-a-v1'
                      AND pipeline_version = 'manifestation-v2'
                    GROUP BY status
                    """
                ).all()
                oldest_active = connection.exec_driver_sql(
                    """
                    SELECT MIN(COALESCE(heartbeat_at, started_at))
                    FROM verificationrun
                    WHERE contract_version = 'certificate-a-v1'
                      AND pipeline_version = 'manifestation-v2'
                      AND status IN ('pending', 'inventorying', 'running', 'cancelling')
                    """
                ).scalar_one_or_none()
            for run_status, count in rows:
                if run_status in CERTIFICATE_A_RUN_STATUSES:
                    runtime_metrics.set_certificate_a_run_depth(str(run_status), int(count))
            oldest_active_at = _as_utc_datetime(oldest_active)
            if oldest_active_at is not None:
                age_seconds = (datetime.now(UTC) - oldest_active_at).total_seconds()
                runtime_metrics.set_certificate_a_oldest_active_heartbeat_age(age_seconds)
        except Exception:
            logger.exception("certificate_a_database_metrics_failed")
            collection_ok = False

        runtime_metrics.set_certificate_a_metrics_collection(success=collection_ok)
        return Response(
            content=runtime_metrics.render(),
            media_type="text/plain; version=0.0.4",
        )

    @application.get("/api/capabilities")
    async def capabilities(request: Request) -> dict[str, Any]:
        settings = _settings(request)
        ocr = settings.recognition_v2.ocr
        tesseract_enabled = ocr.enabled and ocr.backends == ["tesseract"]
        providers: list[str] = []
        if settings.providers.google_books:
            providers.append("google_books_isbn")
        if settings.providers.openlibrary:
            providers.append("openlibrary_isbn")
        return {
            "certificate": "A",
            "mode": "shadow",
            "pipeline": "manifestation-v2",
            "library_source": "offline-folder",
            "providers": providers,
            "ocr": {
                "enabled": tesseract_enabled,
                "backend": "tesseract",
                "max_pages": ocr.max_pages,
            },
            "writes_enabled": False,
        }

    application.include_router(production_verify_router, prefix="/api")
    application.include_router(production_review_router, prefix="/api")

    @application.post("/api/apply")
    async def retired_apply() -> JSONResponse:
        return JSONResponse(
            status_code=410,
            content={"detail": "Legacy apply is retired; Certificate A is shadow-only"},
        )

    resolved_static = (static_dir or _default_static_dir()).resolve()
    index_file = resolved_static / "index.html"
    assets_dir = resolved_static / "assets"
    favicon = resolved_static / "favicon.svg"

    if assets_dir.is_dir():
        application.mount("/assets", StaticFiles(directory=assets_dir), name="assets")

    @application.get("/favicon.svg", response_model=None)
    async def favicon_file() -> FileResponse:
        if not favicon.is_file():
            raise HTTPException(status_code=404, detail="Not found")
        return FileResponse(favicon)

    def spa_index() -> FileResponse:
        if not index_file.is_file():
            raise HTTPException(status_code=404, detail="Not found")
        return FileResponse(index_file)

    application.add_api_route("/", spa_index, methods=["GET"], include_in_schema=False)
    application.add_api_route("/verify", spa_index, methods=["GET"], include_in_schema=False)
    application.add_api_route("/verify/{run_id}", spa_index, methods=["GET"], include_in_schema=False)
    application.add_api_route("/review", spa_index, methods=["GET"], include_in_schema=False)
    application.add_api_route(
        "/review/{evidence_id}",
        spa_index,
        methods=["GET"],
        include_in_schema=False,
    )

    return application


app = create_production_app()

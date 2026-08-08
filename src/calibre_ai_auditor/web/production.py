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
from collections.abc import Callable
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
from calibre_ai_auditor.storage.db import get_engine
from calibre_ai_auditor.verification.metrics import get_metrics
from calibre_ai_auditor.web.api.production_verify import router as production_verify_router
from calibre_ai_auditor.web.observability import request_id_context

logger = logging.getLogger(__name__)

SettingsProvider = Callable[[], Settings]
EngineProvider = Callable[[Settings], Engine]

REQUEST_ID_PATTERN = re.compile(r"^[A-Za-z0-9._:-]{1,128}$")
COVER_KEY_PATTERN = re.compile(r"^[A-Za-z0-9._-]{1,160}$")
REJECTED_API_KEYS = {
    "replace-with-at-least-32-random-characters",
    "change-me",
}
CONTENT_SECURITY_POLICY = (
    "default-src 'self'; base-uri 'self'; frame-ancestors 'none'; "
    "form-action 'self'; object-src 'none'; img-src 'self' data:; "
    "style-src 'self' 'unsafe-inline'; script-src 'self'; connect-src 'self'"
)


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


def _unavailable() -> JSONResponse:
    """Fail closed until the durable Certificate A repository is attached."""
    return JSONResponse(
        status_code=503,
        content={"detail": "Certificate A verification storage is not ready"},
    )


def create_production_app(
    *,
    settings_provider: SettingsProvider = load_settings,
    engine_provider: EngineProvider = get_engine,
    static_dir: Path | None = None,
) -> FastAPI:
    """Build the isolated Certificate A ASGI application."""
    application = FastAPI(
        title="Calibre AI Auditor Certificate A",
        version="1.2.1",
        docs_url=None,
        redoc_url=None,
        openapi_url=None,
    )
    application.state.settings_provider = settings_provider
    application.state.engine_provider = engine_provider
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
        contract_ok = bool(
            settings.profile == "production"
            and settings.database.backend == "postgres"
            and settings.queue.backend == "valkey"
            and settings.library.read_only
            and settings.library.path
            and settings.library.path.is_dir()
            and not settings.allow_remote_file_upload
            and not settings.privacy.allow_remote_text
            and not settings.privacy.allow_remote_images
            and not settings.manifestation_v2.auto_apply.enabled
            and not settings.manifestation_v2.supervised_pilot.enabled
        )
        if not contract_ok:
            raise HTTPException(status_code=503, detail={"status": "not_ready"})
        return {"status": "ready", "certificate": "A"}

    @application.get("/api/metrics")
    async def metrics() -> Response:
        return Response(
            content=get_metrics().render(),
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
    application.add_api_route("/api/review/v2", _unavailable, methods=["GET"])
    application.add_api_route("/api/review/v2/{evidence_id}", _unavailable, methods=["GET"])

    @application.get("/api/covers/{book_key}.jpg")
    async def cover(book_key: str, request: Request) -> FileResponse:
        if not COVER_KEY_PATTERN.fullmatch(book_key):
            raise HTTPException(status_code=404, detail="Cover not found")
        covers_root = _settings(request).storage.artifacts_dir / "covers"
        candidate = covers_root / f"{book_key}.jpg"
        if not candidate.is_file():
            raise HTTPException(status_code=404, detail="Cover not found")
        return FileResponse(candidate)

    @application.post("/api/apply")
    async def retired_apply() -> JSONResponse:
        return JSONResponse(
            status_code=410,
            content={"detail": "Legacy apply is retired; Certificate A is shadow-only"},
        )

    resolved_static = (static_dir or _default_static_dir()).resolve()
    index_file = resolved_static / "index.html"
    assets_dir = resolved_static / "assets"
    favicon = resolved_static / "favicon.ico"

    if assets_dir.is_dir():
        application.mount("/assets", StaticFiles(directory=assets_dir), name="assets")

    @application.get("/favicon.ico", response_model=None)
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

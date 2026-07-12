import contextlib
import hmac
import logging
import os
import re
from collections.abc import AsyncGenerator
from contextlib import asynccontextmanager
from typing import Any
from uuid import uuid4

from fastapi import FastAPI, HTTPException, Request, Response
from fastapi.responses import FileResponse, JSONResponse, RedirectResponse
from fastapi.staticfiles import StaticFiles

from calibre_ai_auditor.web.api import (
    apply,
    audit,
    books,
    bridges,
    config,
    health,
    inspect,
    providers,
    reports,
    runs,
    verify,
)

logger = logging.getLogger(__name__)


@asynccontextmanager
async def lifespan(_app: FastAPI) -> AsyncGenerator[None, None]:
    logger.info("Starting Calibre AI Auditor API...")

    import asyncio
    from pathlib import Path

    from calibre_ai_auditor.config.settings import load_settings
    from calibre_ai_auditor.ingest.watcher import IngestWatcher
    from calibre_ai_auditor.web.covers import resolve_covers_dir
    from calibre_ai_auditor.web.jobs import start_worker_task, stop_worker_task
    from calibre_ai_auditor.web.observability import configure_logging

    settings = load_settings()
    configure_logging(settings.log_level, json_enabled=settings.log_json)

    if not getattr(_app.state, "covers_mounted", False):
        covers_dir = resolve_covers_dir(settings.storage.artifacts_dir)
        _app.mount("/api/covers", StaticFiles(directory=str(covers_dir)), name="covers")
        _app.state.covers_mounted = True

    # Start Valkey background workers
    await start_worker_task(settings)

    watcher_task = None

    if settings.library.path and settings.library.path.exists():
        watcher = IngestWatcher(folders=[settings.library.path], interval=10)

        async def on_file_created(path: Path) -> None:
            logger.info(f"IngestWatcher detected new file: {path}")
            from calibre_ai_auditor.ingest.single_file import audit_ingested_file
            from calibre_ai_auditor.web.jobs import start_job

            await start_job("ingest_file", audit_ingested_file, settings, path)

        async def run_watcher() -> None:
            try:
                await watcher.start(on_file_created)
            except asyncio.CancelledError:
                watcher.stop()
                logger.info("IngestWatcher task cancelled and stopped.")

        watcher_task = asyncio.create_task(run_watcher())
        logger.info(f"IngestWatcher background task started on {settings.library.path}")

    yield

    logger.info("Shutting down Calibre AI Auditor API...")

    # Stop Valkey background workers
    await stop_worker_task()

    if watcher_task:
        watcher_task.cancel()
        with contextlib.suppress(asyncio.CancelledError):
            await watcher_task  # noqa: SIM105


app = FastAPI(
    title="Calibre AI Auditor API",
    description="Backend API for Calibre AI Auditor WebUI",
    version="0.1.0",
    lifespan=lifespan,
)

REQUEST_ID_PATTERN = re.compile(r"^[A-Za-z0-9._:-]{1,128}$")


@app.middleware("http")
async def correlate_request(request: Request, call_next: Any) -> Response:
    from calibre_ai_auditor.web.observability import request_id_context

    supplied = request.headers.get("X-Request-ID", "")
    request_id = supplied if REQUEST_ID_PATTERN.fullmatch(supplied) else str(uuid4())
    token = request_id_context.set(request_id)
    try:
        response: Response = await call_next(request)
        response.headers["X-Request-ID"] = request_id
        return response
    finally:
        request_id_context.reset(token)


# Trailing slash redirection middleware for nested frontend routes
@app.middleware("http")
async def redirect_trailing_slash(request: Request, call_next: Any) -> Response:
    path = request.url.path
    if path != "/" and path.endswith("/") and not path.startswith("/api") and not path.startswith("/assets"):
        return RedirectResponse(
            url=str(request.url.replace(path=path[:-1])),
            status_code=301,
        )
    response: Response = await call_next(request)
    return response


# Cache control headers middleware
@app.middleware("http")
async def add_cache_control_headers(request: Request, call_next: Any) -> Response:
    response: Response = await call_next(request)
    path = request.url.path
    if path.startswith("/assets/"):
        response.headers["Cache-Control"] = "public, max-age=31536000, immutable"
    elif path.startswith("/api/"):
        response.headers["Cache-Control"] = "no-store, no-cache, must-revalidate, max-age=0"
    else:
        response.headers["Cache-Control"] = "no-cache, must-revalidate"
    return response


@app.middleware("http")
async def enforce_api_key(request: Request, call_next: Any) -> Response:
    """Require an API key when configured, and always in production."""
    is_api = request.url.path == "/api" or request.url.path.startswith("/api/")
    if not is_api or request.url.path == "/api/health/live":
        response: Response = await call_next(request)
        return response

    from calibre_ai_auditor.config.settings import load_settings

    settings = load_settings()
    expected = settings.api_key.get_secret_value() if settings.api_key else ""
    if settings.profile == "production" and not expected:
        return JSONResponse(status_code=503, content={"detail": "API authentication is not configured"})
    if expected:
        provided = request.headers.get("X-API-Key", "")
        if not hmac.compare_digest(provided, expected):
            return JSONResponse(status_code=401, content={"detail": "Invalid or missing API Key"})

    response = await call_next(request)
    return response


app.include_router(health.router, prefix="/api")
app.include_router(config.router, prefix="/api")
app.include_router(inspect.router, prefix="/api")
app.include_router(runs.router, prefix="/api")
app.include_router(books.router, prefix="/api")
app.include_router(apply.router, prefix="/api")
app.include_router(providers.router, prefix="/api")
app.include_router(reports.router, prefix="/api")
app.include_router(audit.router, prefix="/api")
app.include_router(bridges.router, prefix="/api")
app.include_router(verify.router, prefix="/api")

# Static files for the frontend
static_dir_str = os.getenv("BOOKAUDIT_STATIC_DIR")
if not static_dir_str:
    static_dir_str = os.path.join(os.path.dirname(__file__), "static")
static_dir: str = static_dir_str

if os.path.exists(static_dir):
    # Mount the assets directory explicitly
    assets_dir = os.path.join(static_dir, "assets")
    if os.path.exists(assets_dir):
        app.mount("/assets", StaticFiles(directory=assets_dir), name="assets")

    # Mount other top-level static files if needed, but we'll just serve index.html as fallback
    @app.get("/{full_path:path}", response_model=None)
    async def serve_spa(full_path: str) -> FileResponse:
        if full_path.startswith("api/"):
            raise HTTPException(status_code=404, detail="Not Found")

        # Check if the requested file exists in the static_dir
        file_path = os.path.join(static_dir, full_path)
        if os.path.isfile(file_path):
            return FileResponse(file_path)

        # Fallback to SPA index.html
        return FileResponse(os.path.join(static_dir, "index.html"))
else:
    logger.warning(f"Static directory not found at {static_dir}. Frontend will not be served.")

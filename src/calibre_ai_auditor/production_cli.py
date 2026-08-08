"""Minimal command surface shipped by the Certificate A production image."""

from __future__ import annotations

import asyncio
from pathlib import Path
from typing import Annotated, Any

import typer
from sqlalchemy import text

from calibre_ai_auditor.config.settings import Settings, load_settings
from calibre_ai_auditor.security.files import SecurePathError, ensure_secure_directory
from calibre_ai_auditor.storage.db import expected_schema_revision, get_engine, init_db

app = typer.Typer(
    name="bookaudit-certificate-a",
    help="Fail-closed Certificate A web and verifier runtime.",
    pretty_exceptions_show_locals=False,
)


def _require_current_schema(engine: Any) -> None:
    try:
        with engine.connect() as connection:
            revision = connection.execute(text("SELECT version_num FROM alembic_version")).scalar_one()
    except Exception as exc:
        raise ValueError("database schema is unavailable; run the one-shot migration first") from exc
    if revision != expected_schema_revision():
        raise ValueError("database schema is not current; run the one-shot migration first")


def _require_database_role(engine: Any, expected_role: str) -> None:
    if engine.dialect.name != "postgresql":
        raise ValueError(f"{expected_role} requires PostgreSQL")
    try:
        with engine.connect() as connection:
            current_user = connection.execute(text("SELECT current_user")).scalar_one()
    except Exception as exc:
        raise ValueError("database role could not be verified") from exc
    if current_user != expected_role:
        raise ValueError(f"database role must be {expected_role}")


def _settings() -> Settings:
    settings = load_settings()
    if settings.profile != "production":
        raise ValueError("Certificate A commands require BOOKAUDIT_PROFILE=production")
    return settings


@app.command()
def web(
    host: Annotated[str, typer.Option("--host", help="Bind socket to this host.")] = "0.0.0.0",
    port: Annotated[int, typer.Option("--port", min=1, max=65535, help="Bind socket to this port.")] = 8080,
) -> None:
    """Start only the Certificate A production application."""
    import uvicorn

    try:
        settings = _settings()
        engine = get_engine(settings)
        _require_current_schema(engine)
        _require_database_role(engine, "bookaudit_app")
    except ValueError as exc:
        typer.secho(str(exc), fg=typer.colors.RED, err=True)
        raise typer.Exit(1) from None
    uvicorn.run("calibre_ai_auditor.web.production:app", host=host, port=port, reload=False)


@app.command()
def migrate() -> None:
    """Run the explicit one-shot Alembic upgrade as the migrator role."""
    try:
        settings = _settings()
        if settings.database.backend != "postgres":
            raise ValueError("Certificate A migration requires PostgreSQL")
        engine = get_engine(settings)
        _require_database_role(engine, "bookaudit_migrator")
        init_db(settings)
    except ValueError as exc:
        typer.secho(str(exc), fg=typer.colors.RED, err=True)
        raise typer.Exit(1) from None
    typer.echo("Database schema upgraded to the Certificate A head")


@app.command()
def verifier(
    once: Annotated[bool, typer.Option("--once", help="Process at most one request and exit.")] = False,
) -> None:
    """Run the dedicated PostgreSQL-fenced Certificate A verifier."""
    from calibre_ai_auditor.verification.certificate_a_worker import (
        CertificateAContractError,
        run_certificate_a_worker,
    )

    try:
        settings = _settings()
        if settings.database.backend != "postgres" or settings.queue.backend != "valkey":
            raise ValueError("Certificate A verifier requires PostgreSQL and Valkey")
        if settings.release_digest is None:
            raise ValueError("Certificate A verifier requires BOOKAUDIT_RELEASE_DIGEST")
        if not settings.library.read_only or settings.library.path is None or not settings.library.path.is_dir():
            raise ValueError("Certificate A verifier requires a readable, read-only library directory")
        ensure_secure_directory(settings.verifier.scratch_dir)
        engine = get_engine(settings)
        _require_current_schema(engine)
        _require_database_role(engine, "bookaudit_verifier")
        processed = asyncio.run(run_certificate_a_worker(settings, once=once))
    except (CertificateAContractError, OSError, SecurePathError, ValueError) as exc:
        typer.secho(str(exc), fg=typer.colors.RED, err=True)
        raise typer.Exit(1) from None
    if once:
        typer.echo("processed one Certificate A request" if processed else "no Certificate A request pending")


@app.command("verifier-health")
def verifier_health() -> None:
    """Exit successfully only for a fresh, exactly bound verifier heartbeat."""
    from calibre_ai_auditor.apply.heartbeat import library_root_sha256
    from calibre_ai_auditor.verification.heartbeat import (
        read_verifier_heartbeat,
        verifier_heartbeat_is_fresh,
    )

    try:
        settings = _settings()
        if settings.release_digest is None or settings.library.path is None or settings.queue.backend != "valkey":
            raise ValueError("Certificate A verifier binding is incomplete")
        heartbeat = read_verifier_heartbeat(
            settings.queue.valkey_url,
            timeout=settings.queue.connect_timeout_seconds,
        )
        fresh = verifier_heartbeat_is_fresh(
            heartbeat,
            release_digest=settings.release_digest,
            alembic_revision=expected_schema_revision(),
            library_root_sha256=library_root_sha256(Path(settings.library.path)),
            max_age_seconds=settings.verifier.heartbeat_max_age_seconds,
        )
    except Exception:
        raise typer.Exit(1) from None
    if not fresh:
        raise typer.Exit(1)
    typer.echo("Certificate A verifier heartbeat is fresh")


if __name__ == "__main__":
    app()

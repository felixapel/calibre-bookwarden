import asyncio
import getpass
import hashlib
import json as json_lib
import logging
import os
import shutil
import sys
from datetime import datetime
from pathlib import Path
from typing import Annotated, Any
from uuid import uuid4

import typer
from sqlalchemy import text
from sqlmodel import Session, col, desc, select

from calibre_ai_auditor.apply.coordinator import PilotGuard, acknowledge_v2_failure
from calibre_ai_auditor.apply.engine import ApplyEngine
from calibre_ai_auditor.audit.engine import run_audit
from calibre_ai_auditor.calibre.cli import CalibreCLI
from calibre_ai_auditor.calibre.content_server import ContentServerError, ContentServerSource, aggregate_inventory
from calibre_ai_auditor.config.settings import Settings, load_settings
from calibre_ai_auditor.extractors.heuristics import extract_heuristics
from calibre_ai_auditor.extractors.text import extract_snippets
from calibre_ai_auditor.security.files import SecurePathError, ensure_secure_directory, write_bytes_beneath
from calibre_ai_auditor.storage.db import expected_schema_revision, get_engine, init_db
from calibre_ai_auditor.storage.models import (
    BookRecord,
    Change,
    PilotSession,
    Run,
    utc_now,
)

app = typer.Typer(pretty_exceptions_show_locals=False)
logger = logging.getLogger(__name__)


def _require_current_schema(engine: Any) -> None:
    """Fail closed when runtime state has not been migrated explicitly."""
    try:
        with engine.connect() as connection:
            revision = connection.execute(text("SELECT version_num FROM alembic_version")).scalar_one()
    except Exception as exc:
        raise ValueError("database schema is unavailable; run `bookaudit migrate` explicitly") from exc
    if revision != expected_schema_revision():
        raise ValueError("database schema is not current; run `bookaudit migrate` explicitly")


async def _audit_run(
    settings: Settings,
    run: str = "latest",
    judge: bool = True,
    save_evidence: bool = True,
) -> None:
    engine = get_engine(settings)
    with Session(engine) as session:
        final_run_id = run
        if run == "latest":
            run_stmt = select(Run).order_by(desc(Run.created_at)).limit(1)
            r = session.exec(run_stmt).first()
            if not r:
                typer.secho("Error: No runs found.", fg=typer.colors.RED)
                raise typer.Exit(1)
            final_run_id = r.run_id

        typer.echo(f"Auditing run: {final_run_id}")
        await run_audit(
            settings,
            final_run_id,
            judge=judge,
            save_evidence=save_evidence,
            progress_callback=lambda current, total: typer.echo(f"  Auditing book {current}/{total}"),
        )
    typer.secho("Audit complete.", fg=typer.colors.GREEN)


@app.callback()
def main(
    ctx: typer.Context,
    config: Annotated[Path | None, typer.Option("--config", "-c", help="Path to config.yml")] = None,
) -> None:
    """
    Calibre AI Auditor - Evidence-first metadata auditing.
    """
    settings = load_settings(config)
    ctx.obj = settings


@app.command()
def doctor(ctx: typer.Context) -> None:
    """
    Check system dependencies and connectivity.
    """
    settings: Settings = ctx.obj
    typer.echo("Doctor check:")
    tools = [
        "calibredb",
        "ebook-meta",
        "ebook-convert",
        "fetch-ebook-metadata",
        "tesseract",
        "ocrmypdf",
    ]
    for tool in tools:
        path = shutil.which(tool)
        status = typer.style("FOUND", fg=typer.colors.GREEN) if path else typer.style("MISSING", fg=typer.colors.RED)
        typer.echo(f"  {tool:25} : {status} ({path or 'N/A'})")

    typer.echo(f"\nLibrary path: {settings.library.path}")
    typer.echo(f"DB path:      {settings.storage.sqlite_path}")


@app.command()
def inventory(
    content_server: Annotated[
        str,
        typer.Option("--content-server", help="Loopback URL for an SSH-tunneled Calibre Content Server"),
    ],
    library_id: Annotated[str, typer.Option("--library-id", help="Exact Content Server library id")],
    username: Annotated[str, typer.Option("--username", help="Read-only Content Server user")],
    source_identity: Annotated[
        str,
        typer.Option("--source-identity", help="Stable verified SSH host fingerprint or equivalent identity"),
    ],
    password_stdin: Annotated[
        bool,
        typer.Option("--password-stdin", help="Read exactly one password line from standard input"),
    ] = False,
    output: Annotated[
        Path | None,
        typer.Option("--output", help="Optional private JSON path beneath the current project directory"),
    ] = None,
) -> None:
    """Inventory a remote Calibre library without DB, provider, OCR, or LLM state."""
    if password_stdin:
        password = sys.stdin.readline().rstrip("\r\n")
    else:
        password = getpass.getpass("Calibre Content Server password: ")
    if not password:
        typer.secho("Inventory failed: an empty password is not allowed", fg=typer.colors.RED, err=True)
        raise typer.Exit(2)
    try:
        source = ContentServerSource(
            content_server,
            library_id=library_id,
            username=username,
            source_identity=source_identity,
            password=password,
        )
        report = aggregate_inventory(source.list_books(), source_fingerprint=source.fingerprint)
        payload = (json_lib.dumps(report.model_dump(mode="json"), indent=2, sort_keys=True) + "\n").encode()
        if output is None:
            typer.echo(payload.decode(), nl=False)
            return
        root = Path(os.path.abspath(Path.cwd()))
        target = Path(os.path.abspath(output))
        if target == root or not target.is_relative_to(root):
            raise ValueError("output must be beneath the current project directory")
        parent = ensure_secure_directory(target.parent)
        os.chmod(parent, 0o700, follow_symlinks=False)
        write_bytes_beneath(root, target, payload, mode=0o600)
        typer.echo(f"Private inventory written: {target}")
    except (ContentServerError, OSError, SecurePathError, ValueError) as exc:
        typer.secho(f"Inventory failed: {exc}", fg=typer.colors.RED, err=True)
        raise typer.Exit(1) from exc


@app.command("verify-content-server")
def verify_content_server(
    ctx: typer.Context,
    content_server: Annotated[
        str,
        typer.Option("--content-server", help="Loopback URL for an SSH-tunneled Calibre Content Server"),
    ],
    library_id: Annotated[str, typer.Option("--library-id", help="Exact Content Server library id")],
    username: Annotated[str, typer.Option("--username", help="Read-only Content Server user")],
    source_identity: Annotated[
        str,
        typer.Option("--source-identity", help="Stable verified SSH host fingerprint or equivalent identity"),
    ],
    scratch_root: Annotated[
        Path,
        typer.Option("--scratch-root", help="Private directory for short-lived exported formats"),
    ],
    password_stdin: Annotated[
        bool,
        typer.Option("--password-stdin", help="Read exactly one password line from standard input"),
    ] = False,
    limit: Annotated[int, typer.Option("--limit", min=0, help="Max books to verify (0 = unlimited)")] = 1,
    run_id: Annotated[str | None, typer.Option("--run-id", help="Resume an existing remote V2 run id")] = None,
    use_llm: Annotated[bool, typer.Option("--use-llm/--no-llm")] = False,
    use_ocr: Annotated[bool, typer.Option("--use-ocr/--no-ocr")] = True,
    use_vision: Annotated[bool, typer.Option("--use-vision/--no-vision")] = False,
    allow_remote_text: Annotated[bool, typer.Option("--allow-remote-text/--deny-remote-text")] = False,
    allow_remote_images: Annotated[bool, typer.Option("--allow-remote-images/--deny-remote-images")] = False,
    allow_public_providers: Annotated[
        bool,
        typer.Option(
            "--allow-public-providers/--deny-public-providers",
            help="Permit exact identifier lookups at allowlisted public metadata providers",
        ),
    ] = False,
    format: Annotated[str, typer.Option("--format", help="Report format: text|json")] = "text",
) -> None:
    """Run a persisted, shadow-only V2 audit through a read-only Content Server."""
    from calibre_ai_auditor.verification.pipeline_v2 import AuditMode
    from calibre_ai_auditor.verification.service_v2 import build_v2_enricher, run_persisted_library_audit

    if format not in {"text", "json"}:
        typer.secho("Error: --format must be text or json.", fg=typer.colors.RED, err=True)
        raise typer.Exit(2)
    if use_llm or use_vision or allow_remote_text or allow_remote_images:
        typer.secho(
            "Remote verification currently permits local extraction/OCR and optional exact public providers only.",
            fg=typer.colors.RED,
            err=True,
        )
        raise typer.Exit(2)
    password = (
        sys.stdin.readline().rstrip("\r\n") if password_stdin else getpass.getpass("Calibre Content Server password: ")
    )
    if not password:
        typer.secho("Remote verification failed: an empty password is not allowed", fg=typer.colors.RED, err=True)
        raise typer.Exit(2)
    settings: Settings = ctx.obj
    final_run_id = run_id or f"remote_verify_{datetime.now().strftime('%Y%m%d_%H%M%S')}_{uuid4().hex[:8]}"
    try:
        source = ContentServerSource(
            content_server,
            library_id=library_id,
            username=username,
            source_identity=source_identity,
            password=password,
        )
        private_scratch = ensure_secure_directory(scratch_root)
        engine = get_engine(settings)
        _require_current_schema(engine)
        enricher = build_v2_enricher(
            settings,
            use_llm=use_llm,
            use_ocr=use_ocr,
            use_vision=use_vision,
            run_allows_remote_text=allow_remote_text,
            run_allows_remote_images=allow_remote_images,
            use_public_providers=allow_public_providers,
        )
        result = asyncio.run(
            run_persisted_library_audit(
                cli=source,
                database_engine=engine,
                run_id=final_run_id,
                limit=limit,
                mode=AuditMode.shadow,
                evidence_enricher=enricher,
                use_llm=use_llm,
                settings=settings,
                scratch_root=private_scratch,
            )
        )
    except (ContentServerError, OSError, SecurePathError, ValueError) as exc:
        typer.secho(f"Remote verification failed: {exc}", fg=typer.colors.RED, err=True)
        raise typer.Exit(1) from exc

    verdicts = [
        {
            "book_key": package.book_key,
            "state": package.state.value,
            "tier": package.identity.tier.value,
            "risk_flags": package.identity.risk_flags,
        }
        for package in result.packages
    ]
    if format == "json":
        typer.echo(
            json_lib.dumps(
                {"run_id": final_run_id, "status": result.status.value, "verdicts": verdicts},
                indent=2,
            )
        )
    else:
        typer.echo(
            f"Remote shadow audit processed {len(verdicts)} books (run_id={final_run_id}, status={result.status.value})"
        )
        for verdict in verdicts:
            typer.echo(
                f"  {verdict['book_key']} state={verdict['state']} tier={verdict['tier']} "
                f"flags={','.join(verdict['risk_flags']) or '-'}"
            )


@app.command()
def migrate(ctx: typer.Context) -> None:
    """Upgrade the configured database to the repository's Alembic head and exit."""
    settings: Settings = ctx.obj
    init_db(settings)
    typer.secho("Database schema upgraded to head.", fg=typer.colors.GREEN)


@app.command("pilot-stop")
def pilot_stop(
    ctx: typer.Context,
    pilot_id: Annotated[str, typer.Argument(help="Exact supervised pilot ID to close")],
    yes: Annotated[bool, typer.Option("--yes", help="Confirm closing the exact pilot session")] = False,
) -> None:
    """Close one persisted supervised V2 pilot so queued work fails closed."""
    if not yes:
        typer.secho("Stopping a supervised pilot requires --yes.", fg=typer.colors.RED)
        raise typer.Exit(1)
    settings: Settings = ctx.obj
    engine = get_engine(settings)
    with Session(engine) as session:
        pilot = session.exec(select(PilotSession).where(PilotSession.pilot_id == pilot_id).with_for_update()).first()
        if pilot is None:
            typer.secho(f"Pilot session not found: {pilot_id}", fg=typer.colors.RED)
            raise typer.Exit(1)
        if pilot.state != "stopped":
            pilot.state = "stopped"
            pilot.updated_at = utc_now()
            session.add(pilot)
            session.commit()
    typer.secho(f"Supervised V2 pilot {pilot_id} stopped.", fg=typer.colors.GREEN)


@app.command("incident-ack")
def incident_ack(
    ctx: typer.Context,
    operation_id: Annotated[str, typer.Argument(help="Exact failed V2 operation ID")],
    actor: Annotated[str, typer.Option("--actor", help="Operator identity recorded in the audit trail")],
    reason: Annotated[str, typer.Option("--reason", help="Evidence that the terminal failure is safe to clear")],
    yes: Annotated[bool, typer.Option("--yes", help="Confirm the exact incident acknowledgement")] = False,
) -> None:
    """Acknowledge one reviewed, quiescent V2 failure without deleting its evidence."""
    if not yes:
        typer.secho("Acknowledging a supervised V2 incident requires --yes.", fg=typer.colors.RED)
        raise typer.Exit(1)
    settings: Settings = ctx.obj
    engine = get_engine(settings)
    with Session(engine) as session:
        try:
            acknowledge_v2_failure(
                session,
                operation_id=operation_id,
                actor=actor,
                reason=reason,
            )
            session.commit()
        except ValueError as exc:
            session.rollback()
            typer.secho(str(exc), fg=typer.colors.RED)
            raise typer.Exit(1) from None
    typer.secho(f"Supervised V2 incident {operation_id} acknowledged.", fg=typer.colors.GREEN)


@app.command()
def scan(
    ctx: typer.Context,
    limit: Annotated[int, typer.Option("--limit", help="Max books to scan")] = 100,
) -> None:
    """
    Scan Calibre library for books and metadata.
    """
    settings: Settings = ctx.obj
    if not settings.library.path:
        typer.secho("Error: Library path not set.", fg=typer.colors.RED)
        raise typer.Exit(1)

    init_db(settings)
    cli = CalibreCLI(settings.library.path)

    run_id = f"run_{datetime.now().strftime('%Y%m%d_%H%M%S')}"
    typer.echo(f"Starting scan: {run_id}")

    books = cli.list_books()
    if limit > 0:
        books = books[:limit]

    engine = get_engine(settings)
    with Session(engine) as session:
        run = Run(run_id=run_id, status="completed")
        session.add(run)

        for b in books:
            book_id = b["id"]
            typer.echo(f"  Processing book {book_id}: {b.get('title')}")
            full_meta = cli.show_metadata(book_id)

            book_key = f"calibre:{book_id}"
            existing = session.exec(select(BookRecord).where(BookRecord.book_key == book_key)).first()
            if existing:
                existing.run_id = run_id
                existing.current_metadata = full_meta
                existing.files = full_meta.get("formats", [])
                existing.status = "scanned"
                session.add(existing)
            else:
                record = BookRecord(
                    book_key=book_key,
                    run_id=run_id,
                    calibre_book_id=book_id,
                    source="calibre",
                    current_metadata=full_meta,
                    files=full_meta.get("formats", []),
                )
                session.add(record)

        session.commit()

        if settings.vectors.enabled:
            from calibre_ai_auditor.vectors.client import VectorClient
            from calibre_ai_auditor.vectors.embeddings import get_embedding_client
            from calibre_ai_auditor.vectors.indexer import VectorIndexer

            vclient = VectorClient(
                settings.vectors.qdrant_url,
                settings.vectors.collection,
                settings.vectors.enabled,
            )
            eclient = get_embedding_client(settings)
            indexer = VectorIndexer(vclient, eclient)
            for b in books:
                book_key = f"calibre:{b['id']}"
                existing_book: BookRecord | None = session.exec(
                    select(BookRecord).where(BookRecord.book_key == book_key)
                ).first()
                if existing_book:
                    asyncio.run(indexer.index_book(existing_book))

    typer.secho(f"Scan complete. Found {len(books)} books.", fg=typer.colors.GREEN)


@app.command()
def inspect(
    ctx: typer.Context,
    path: Annotated[Path, typer.Option("--path", help="Path to ebook file")],
    no_providers: Annotated[bool, typer.Option("--no-providers", help="Skip external fetch")] = False,
) -> None:
    """
    standalone inspection of a single file.
    """
    if not path.exists():
        typer.secho(f"Error: File not found: {path}", fg=typer.colors.RED)
        raise typer.Exit(1)

    typer.echo(f"Inspecting: {path.name}")
    typer.echo("  Extracting snippets...")
    snippets = extract_snippets(path)

    typer.echo("  Applying heuristics...")
    extracted = extract_heuristics([s.model_dump() for s in snippets])

    typer.echo("\nExtracted Info:")
    typer.echo(f"  Title:   {extracted.get('title')}")
    typer.echo(f"  Authors: {', '.join(extracted.get('authors', []))}")
    typer.echo(f"  ISBN:    {extracted.get('identifiers', {}).get('isbn')}")


@app.command()
def audit(
    ctx: typer.Context,
    run: Annotated[str, typer.Option("--run", help="Run ID or 'latest'")] = "latest",
    judge: Annotated[bool, typer.Option("--judge/--no-judge", help="Enable or skip LLM judgment")] = True,
    save_evidence: Annotated[bool, typer.Option("--save-evidence", help="Persist evidence JSON")] = True,
) -> None:
    """
    Build evidence packages and optionally call the judge model.
    """
    settings: Settings = ctx.obj
    asyncio.run(_audit_run(settings, run, judge, save_evidence))


@app.command()
def apply(
    ctx: typer.Context,
    run: Annotated[str, typer.Option("--run", help="Run ID or 'latest'")] = "latest",
    safe_only: Annotated[
        bool, typer.Option("--safe-only", help="Only apply high-confidence 'safe' suggestions")
    ] = True,
    yes: Annotated[bool, typer.Option("--yes", "-y", help="Skip confirmation")] = False,
) -> None:
    """
    Report the retired legacy apply path.
    """
    _ = (ctx, run, safe_only, yes)
    typer.secho(
        "Legacy direct apply is disabled. Use the authenticated Manifestation V2 authorize/apply API.",
        fg=typer.colors.RED,
    )
    raise typer.Exit(1)


@app.command()
def undo(
    ctx: typer.Context,
    change_id: Annotated[int, typer.Argument(help="ID of the change to revert")],
) -> None:
    """
    Revert one applied change.
    """
    settings: Settings = ctx.obj
    if settings.profile == "production":
        typer.secho(
            "Direct CLI undo is disabled in production; queue through the authenticated API",
            fg=typer.colors.RED,
        )
        raise typer.Exit(1)
    engine = get_engine(settings)
    cli = CalibreCLI(settings.library.path)
    apply_engine = ApplyEngine(cli, settings.storage.artifacts_dir)

    with Session(engine) as session:
        statement = select(Change).where(Change.id == change_id)
        change = session.exec(statement).first()
        if not change:
            typer.secho(f"Error: Change with ID {change_id} not found.", fg=typer.colors.RED)
            raise typer.Exit(1)

        typer.echo(f"Undoing change {change_id} for {change.book_key}...")
        try:
            apply_engine.undo_change(session, change)
            session.commit()
            typer.secho("Undo successful.", fg=typer.colors.GREEN)
        except Exception as e:
            typer.secho(f"Error: {e}", fg=typer.colors.RED)


def _verify_backup_manifest(manifest_path: Path) -> str:
    """Verify the paired database/artifact files named by a retention backup manifest."""
    try:
        absolute_manifest = manifest_path.absolute()
        checked_component = Path(absolute_manifest.anchor)
        for component in absolute_manifest.parts[1:]:
            checked_component /= component
            if checked_component.is_symlink():
                raise ValueError("backup manifest path contains a symlink")
        if not absolute_manifest.is_file():
            raise ValueError("backup manifest must be a regular non-symlink file")
        manifest_bytes = absolute_manifest.read_bytes()
        manifest = json_lib.loads(manifest_bytes)
        datetime.fromisoformat(manifest["created_at"])
        root = absolute_manifest.parent
        for file_key, digest_key in (
            ("database_dump", "database_sha256"),
            ("artifacts_archive", "artifacts_sha256"),
        ):
            relative = Path(manifest[file_key])
            if relative.is_absolute() or ".." in relative.parts:
                raise ValueError(f"{file_key} must be relative to the backup manifest")
            backup_file = root
            for component in relative.parts:
                backup_file /= component
                if backup_file.is_symlink():
                    raise ValueError(f"unsafe or missing {file_key}")
            backup_file = backup_file.resolve()
            if not backup_file.is_relative_to(root) or not backup_file.is_file():
                raise ValueError(f"unsafe or missing {file_key}")
            digest = hashlib.sha256()
            with backup_file.open("rb") as backup_stream:
                for chunk in iter(lambda: backup_stream.read(1024 * 1024), b""):
                    digest.update(chunk)
            actual = digest.hexdigest()
            if actual != manifest[digest_key]:
                raise ValueError(f"checksum mismatch for {file_key}")
        return hashlib.sha256(manifest_bytes).hexdigest()
    except (OSError, KeyError, TypeError, ValueError, json_lib.JSONDecodeError) as exc:
        typer.secho(f"Backup manifest verification failed: {exc}", fg=typer.colors.RED)
        raise typer.Exit(1) from exc


@app.command("retention")
def retention(
    ctx: typer.Context,
    execute: Annotated[bool, typer.Option("--execute", help="Delete expired restore points")] = False,
    recover_quarantine: Annotated[
        str | None,
        typer.Option("--recover-quarantine", help="Resume exactly one journaled transaction ID"),
    ] = None,
    backup_reference: Annotated[
        Path | None,
        typer.Option("--backup-reference", help="JSON manifest for the paired DB/artifact backup"),
    ] = None,
) -> None:
    """Preview or explicitly delete restore points past their recorded retention target."""
    settings: Settings = ctx.obj
    from calibre_ai_auditor.verification.restore import RestorePointStore

    store = RestorePointStore(settings.storage.artifacts_dir)
    manifest = []
    if recover_quarantine is not None:
        if not execute:
            typer.secho("--recover-quarantine requires --execute", fg=typer.colors.RED)
            raise typer.Exit(1)
        typer.echo(f"Recovering retention quarantine: {recover_quarantine}")
    else:
        try:
            pending_quarantines = store.list_pending_quarantines()
        except RuntimeError as exc:
            typer.secho(str(exc), fg=typer.colors.RED)
            raise typer.Exit(1) from exc
        if pending_quarantines:
            transaction_ids = ", ".join(transaction.name for transaction in pending_quarantines)
            typer.secho(
                "Pending retention quarantine requires explicit recovery with "
                f"--recover-quarantine TRANSACTION_ID: {transaction_ids}",
                fg=typer.colors.RED,
            )
            raise typer.Exit(1)
        try:
            manifest = store.build_deletion_manifest()
        except RuntimeError as exc:
            typer.secho(str(exc), fg=typer.colors.RED)
            raise typer.Exit(1) from exc
        typer.echo(f"Expired restore points: {len(manifest)}")
        for candidate in manifest:
            typer.echo(f"  {candidate.path.relative_to(store.restore_root)}")
        if not execute:
            typer.echo("Dry run only; no restore points were deleted.")
            return
    if backup_reference is None:
        typer.secho("--backup-reference is required with --execute", fg=typer.colors.RED)
        raise typer.Exit(1)
    backup_manifest_sha256 = _verify_backup_manifest(backup_reference)

    writer_guard = None
    writer_guard_acquired = False
    try:
        if settings.profile == "production":
            from sqlalchemy import text

            from calibre_ai_auditor.apply.guard import acquire_writer_guard
            from calibre_ai_auditor.apply.heartbeat import heartbeat_is_fresh, read_writer_heartbeat

            engine = get_engine(settings)
            if engine.dialect.name != "postgresql":
                typer.secho("Production retention requires PostgreSQL", fg=typer.colors.RED)
                raise typer.Exit(1)
            writer_guard = engine.connect()
            writer_guard_acquired = acquire_writer_guard(writer_guard)
            if not writer_guard_acquired:
                typer.secho("Metadata writer is active; retention refused", fg=typer.colors.RED)
                raise typer.Exit(1)
            nonterminal = writer_guard.execute(
                text(
                    "SELECT count(*) FROM operationledger "
                    "WHERE state IN ('requested','claimed','writing','verifying','restoring')"
                )
            ).scalar_one()
            heartbeat = read_writer_heartbeat(
                settings.queue.valkey_url,
                timeout=settings.queue.connect_timeout_seconds,
            )
            if nonterminal or heartbeat_is_fresh(
                heartbeat,
                max_age_seconds=settings.writer_heartbeat_max_age_seconds,
            ):
                typer.secho(
                    "Writer heartbeat or non-terminal operations remain; retention refused",
                    fg=typer.colors.RED,
                )
                raise typer.Exit(1)
        deleted = (
            store.recover_quarantine(
                recover_quarantine,
                backup_manifest_sha256=backup_manifest_sha256,
            )
            if recover_quarantine is not None
            else store.quarantine_and_delete(
                manifest,
                backup_manifest_sha256=backup_manifest_sha256,
            )
        )
    except (OSError, RuntimeError) as exc:
        typer.secho(str(exc), fg=typer.colors.RED)
        raise typer.Exit(1) from exc
    finally:
        if writer_guard is not None:
            from calibre_ai_auditor.apply.guard import release_writer_guard

            try:
                if writer_guard_acquired:
                    release_writer_guard(writer_guard)
            finally:
                writer_guard.close()
    if recover_quarantine is not None:
        typer.secho(
            f"Recovered deletion of {deleted} quarantined restore points after backup {backup_reference}.",
            fg=typer.colors.GREEN,
        )
    else:
        typer.secho(
            f"Deleted {deleted} expired restore points after backup {backup_reference}.",
            fg=typer.colors.GREEN,
        )


@app.command()
def ingest_paperless(
    ctx: typer.Context,
    limit: Annotated[int, typer.Option("--limit", help="Max documents to import")] = 50,
) -> None:
    """
    Ingest scanned books from Paperless-ngx matching configured document types.
    """
    settings: Settings = ctx.obj
    if not settings.paperless.enabled:
        typer.secho("Error: Paperless integration is disabled in settings.", fg=typer.colors.RED)
        raise typer.Exit(1)

    from calibre_ai_auditor.integrations.paperless import PaperlessBridge

    bridge = PaperlessBridge(settings)

    async def run_ingest() -> None:
        engine = get_engine(settings)
        init_db(settings)

        # Test connection first
        if not await bridge.test_connection():
            typer.secho("Error: Could not connect to Paperless-ngx. Check logs.", fg=typer.colors.RED)
            raise typer.Exit(1)

        typer.echo("Fetching candidate documents from Paperless-ngx...")
        docs = await bridge.fetch_candidate_documents()
        if not docs:
            typer.echo("No documents matching target document types found.")
            return

        if limit > 0:
            docs = docs[:limit]

        typer.echo(f"Found {len(docs)} documents to ingest.")
        run_id = f"run_paperless_{datetime.now().strftime('%Y%m%d_%H%M%S')}"

        with Session(engine) as session:
            run = Run(run_id=run_id, status="completed")
            session.add(run)

            # Destination directory for downloaded files
            import_dir = Path(settings.storage.artifacts_dir) / "paperless_imports"

            for doc in docs:
                doc_id = doc["id"]
                title = doc.get("title", f"Paperless Document {doc_id}")
                typer.echo(f"  Ingesting document {doc_id}: {title}...")

                # Download file
                file_path = await bridge.download_document_file(doc_id, import_dir)
                if not file_path:
                    typer.secho(
                        f"    Failed to download file for document {doc_id}",
                        fg=typer.colors.YELLOW,
                    )
                    continue

                book_key = f"paperless:{doc_id}"

                # Check if already exists
                existing = session.exec(select(BookRecord).where(BookRecord.book_key == book_key)).first()

                # Construct file and metadata info
                file_info = {
                    "path": str(file_path),
                    "format": file_path.suffix.lstrip(".").lower(),
                    "size_bytes": file_path.stat().st_size if file_path.exists() else None,
                }

                current_meta = {
                    "title": title,
                    "authors": [],
                    "publisher": None,
                    "published_date": doc.get("created"),
                    "tags": doc.get("tags", []),
                }

                if existing:
                    existing.run_id = run_id
                    existing.current_metadata = current_meta
                    existing.files = [file_info]
                    existing.status = "scanned"
                    existing.paperless_document_id = doc_id
                    session.add(existing)
                else:
                    record = BookRecord(
                        book_key=book_key,
                        run_id=run_id,
                        paperless_document_id=doc_id,
                        source="paperless",
                        current_metadata=current_meta,
                        files=[file_info],
                    )
                    session.add(record)

            session.commit()
        typer.secho("Ingestion complete.", fg=typer.colors.GREEN)

    asyncio.run(run_ingest())


@app.command()
def web(
    _ctx: typer.Context,
    host: Annotated[str, typer.Option("--host", help="Bind socket to this host.")] = "0.0.0.0",
    port: Annotated[int, typer.Option("--port", help="Bind socket to this port.")] = 8080,
    reload: Annotated[bool, typer.Option("--reload", help="Enable auto-reload.")] = False,
) -> None:
    """
    Start the WebUI backend server.
    """
    import uvicorn

    settings: Settings = _ctx.obj
    init_db(settings)

    typer.echo(f"Starting Web API on {host}:{port}...")
    uvicorn.run("calibre_ai_auditor.web.app:app", host=host, port=port, reload=reload)


@app.command()
def config(
    ctx: typer.Context,
) -> None:
    """
    Show effective configuration.
    """
    settings: Settings = ctx.obj
    out = settings.model_dump(exclude={"open_ai_api_key"})
    typer.echo(json_lib.dumps(out, indent=2, default=str))


@app.command("calibrate-v2")
def calibrate_v2(
    corpus: Annotated[
        Path,
        typer.Option("--corpus", help="Reviewed manifestation-v2 calibration corpus JSON"),
    ],
    output: Annotated[
        Path,
        typer.Option("--output", help="Destination for the advisory checksummed calibration report"),
    ],
    valid_days: Annotated[
        int,
        typer.Option("--valid-days", min=1, max=365, help="Report validity window"),
    ] = 30,
) -> None:
    """Create an advisory V2 calibration report from reviewed labels."""
    from calibre_ai_auditor.verification.calibration_v2 import (
        create_report_from_labeled_corpus,
        write_calibration_report,
    )

    try:
        report = create_report_from_labeled_corpus(corpus, valid_days=valid_days)
        write_calibration_report(report, output)
    except (OSError, ValueError) as exc:
        typer.secho(f"Calibration failed: {exc}", fg=typer.colors.RED)
        raise typer.Exit(1) from exc
    typer.echo(
        f"Advisory checksummed calibration report: {output} "
        f"(sample={report.sample_size}, Tier A={report.tier_a_decisions}, "
        f"false_auto_apply={report.false_auto_apply_count})"
    )


@app.command()
def verify(
    ctx: typer.Context,
    limit: Annotated[int, typer.Option("--limit", help="Max books to verify (0 = unlimited)")] = 50,
    library: Annotated[str | None, typer.Option("--library", help="Override library path")] = None,
    use_llm: Annotated[bool, typer.Option("--use-llm/--no-llm", help="Call LLM for ambiguous fields")] = False,
    use_ocr: Annotated[
        bool,
        typer.Option("--use-ocr/--no-ocr", help="OCR bounded PDF front matter before exact provider lookup"),
    ] = True,
    use_vision: Annotated[
        bool,
        typer.Option("--use-vision/--no-vision", help="Use cover vision as non-authoritative review evidence"),
    ] = False,
    format: Annotated[str, typer.Option("--format", help="Report format: text|json")] = "text",
    pipeline: Annotated[str, typer.Option("--pipeline", help="Verification contract: v2|v1")] = "v2",
    run_id: Annotated[str | None, typer.Option("--run-id", help="Resume an existing V2 run id")] = None,
    allow_remote_text: Annotated[
        bool,
        typer.Option(
            "--allow-remote-text/--deny-remote-text",
            help="Per-run consent for bounded text egress to a remote LLM",
        ),
    ] = False,
    allow_remote_images: Annotated[
        bool,
        typer.Option(
            "--allow-remote-images/--deny-remote-images",
            help="Per-run consent for bounded cover egress to a remote vision model",
        ),
    ] = False,
) -> None:
    """
    Verify Calibre metadata against exact book content.

    Manifestation V2 is the default: all formats, exact-ID providers, sealed
    evidence, and shadow-only decisions. Use --pipeline v1 for the legacy
    ContentVerificationEngine.
    """
    import time as _t

    if pipeline == "v2":
        from calibre_ai_auditor.providers.evidence_v2 import CompositeEvidenceEnricher
        from calibre_ai_auditor.storage.models import VerificationResult
        from calibre_ai_auditor.verification.pipeline_v2 import AuditMode
        from calibre_ai_auditor.verification.service_v2 import (
            build_v2_enricher,
            run_persisted_library_audit,
        )

        v2_settings: Settings = ctx.obj
        v2_library_path = Path(library) if library else v2_settings.library.path
        if not v2_library_path:
            typer.secho("Error: No library path configured.", fg=typer.colors.RED)
            raise typer.Exit(1)
        if format not in {"text", "json"}:
            typer.secho("Error: --format must be text or json.", fg=typer.colors.RED)
            raise typer.Exit(2)
        init_db(v2_settings)
        database_engine = get_engine(v2_settings)
        final_run_id = run_id or f"verify_{datetime.now().strftime('%Y%m%d_%H%M%S')}_{uuid4().hex[:8]}"
        try:
            enricher = build_v2_enricher(
                v2_settings,
                use_llm=use_llm,
                use_ocr=use_ocr,
                use_vision=use_vision,
                run_allows_remote_text=allow_remote_text,
                run_allows_remote_images=allow_remote_images,
            )
        except ValueError as exc:
            typer.secho(f"Error: {exc}", fg=typer.colors.RED)
            raise typer.Exit(2) from exc
        # Keep the protocol dependency explicit for type checkers and callers
        # that replace the configured enrichers in tests.
        assert isinstance(enricher, CompositeEvidenceEnricher)
        started = _t.monotonic()
        v2_result = asyncio.run(
            run_persisted_library_audit(
                cli=CalibreCLI(v2_library_path),
                database_engine=database_engine,
                run_id=final_run_id,
                limit=limit,
                mode=AuditMode.shadow,
                evidence_enricher=enricher,
                use_llm=use_llm,
                settings=v2_settings,
            )
        )
        elapsed = _t.monotonic() - started
        with Session(database_engine) as result_session:
            stored_v2 = result_session.exec(
                select(VerificationResult)
                .where(VerificationResult.run_id == final_run_id)
                .order_by(col(VerificationResult.id))
            ).all()
        v2_verdicts = [item.verdict for item in stored_v2 if item.evidence_id]
        if format == "json":
            typer.echo(
                json_lib.dumps(
                    {"run_id": final_run_id, "elapsed_seconds": elapsed, "verdicts": v2_verdicts},
                    indent=2,
                    default=str,
                )
            )
        else:
            typer.echo(f"Verified {len(v2_verdicts)} books (run_id={final_run_id}, status={v2_result.status.value})")
            for v2_verdict in v2_verdicts:
                identity = v2_verdict.get("identity", {})
                typer.echo(
                    f"  {v2_verdict.get('book_key', '?'):20s} "
                    f"state={v2_verdict.get('state', '?'):10s} tier={identity.get('tier', '?')} "
                    f"flags={','.join(identity.get('risk_flags', [])) or '-'}"
                )
            typer.echo(f"Done in {elapsed:.1f}s")
        return
    if pipeline != "v1":
        typer.secho("Error: --pipeline must be v2 or v1.", fg=typer.colors.RED)
        raise typer.Exit(2)

    from calibre_ai_auditor.verification import (
        ContentVerificationEngine,
        DeclaredMetadata,
        LLMWitness,
        ObservationSet,
        WitnessConfig,
    )
    from calibre_ai_auditor.verification.metrics import get_metrics

    settings: Settings = ctx.obj
    lib_path = Path(library) if library else settings.library.path
    if not lib_path:
        typer.secho("Error: No library path configured.", fg=typer.colors.RED)
        raise typer.Exit(1)

    cli = CalibreCLI(lib_path)
    books = cli.list_books()
    if limit:
        books = books[:limit]
    if not books:
        typer.secho("No books found.", fg=typer.colors.YELLOW)
        raise typer.Exit(0)

    engine = ContentVerificationEngine()
    metrics = get_metrics()
    run_id = f"verify_{datetime.now().strftime('%Y%m%d_%H%M%S')}"
    metrics.set_run_progress(run_id, total=len(books), completed=0)

    # Optional LLM witness
    witness = None
    if use_llm:
        from calibre_ai_auditor.llm.router import LLMRouter

        router = LLMRouter(settings)
        witness = LLMWitness(router, WitnessConfig(cache_responses=True))

    typer.echo(f"Verifying {len(books)} books in {lib_path} (run_id={run_id})")
    started = _t.monotonic()
    verdicts: list[dict[str, Any]] = []
    counts: dict[str, int] = {"no_change": 0, "suggest_fix": 0, "needs_review": 0, "defer": 0}

    for i, book in enumerate(books, start=1):
        book_key = f"calibre:{book['id']}"
        title = book.get("title", "")
        authors_str = book.get("authors", "")
        authors_list = [a.strip() for a in authors_str.split("&") if a.strip()] if isinstance(authors_str, str) else []  # noqa: SIM108

        # Extract real content from the first available format
        formats = book.get("formats", [])
        snippet_text = ""
        for fmt in formats[:1]:
            file_path = Path(fmt)
            if file_path.exists():
                snippets = extract_snippets(file_path, max_pages=3)
                snippet_text = "\n".join(s.text for s in snippets)
                break

        heuristics = extract_heuristics([{"text": snippet_text, "source": "first_pages"}] if snippet_text else [])

        declared = DeclaredMetadata(
            title=title,
            authors=authors_list,
            publisher=book.get("publisher"),
            published_date=book.get("pubdate"),
            language=book.get("languages"),
            series=book.get("series"),
            isbn=(book.get("identifiers") or {}).get("isbn") if book.get("identifiers") else None,
        )
        observed = ObservationSet(
            title_page_text=snippet_text[:5000] if snippet_text else None,
            body_sample=snippet_text[:10000] if snippet_text else None,
            title_extracted=heuristics.get("title"),
            authors_extracted=heuristics.get("authors") or [],
            isbn_extracted=(heuristics.get("identifiers") or {}).get("isbn"),
            evidence_quality="high" if len(snippet_text) > 500 else "low",
        )

        verdict = engine.verify(book_key=book_key, run_id=run_id, declared=declared, observed=observed)

        # Optionally call LLM witness for ambiguous fields
        if witness:
            book_title_observed = verdict.field_verdicts.get("title")
            book_title_str = book_title_observed.observed_value if book_title_observed else None
            book_authors_observed = verdict.field_verdicts.get("authors")
            book_authors_list = book_authors_observed.observed_value if book_authors_observed else None

            for fname, fv in list(verdict.field_verdicts.items()):
                if fv.verdict.value == "ambiguous":
                    result = asyncio.run(
                        witness.witness_field(
                            field_name=fname,
                            current_fv=fv,
                            book_title=book_title_str,
                            book_authors=book_authors_list,
                        )
                    )
                    if result.success and result.refined_verdict:
                        verdict.field_verdicts[fname] = result.refined_verdict
                        if result.judge_call:
                            metrics.record_llm_call(
                                provider=result.judge_call.provider,
                                model=result.judge_call.model,
                                duration_ms=result.judge_call.duration_ms,
                                tokens_in=result.judge_call.tokens_in,
                                tokens_out=result.judge_call.tokens_out,
                            )

        counts[verdict.action.value] = counts.get(verdict.action.value, 0) + 1
        metrics.record_book_action(verdict.action.value, run_id)

        if format == "json":
            verdicts.append(verdict.model_dump())
        else:
            typer.echo(
                f"  [{i:>3}/{len(books)}] {book_key:20s} "
                f"action={verdict.action.value:12s} "
                f"conf={verdict.overall_confidence:3d} "
                f"flags={','.join(verdict.risk_flags) or '-':20s}  "
                f"title={title[:60]!r}"
            )

        metrics.set_run_progress(run_id, total=len(books), completed=i)

    elapsed = _t.monotonic() - started
    typer.secho("", fg=typer.colors.WHITE)
    typer.secho(f"Done in {elapsed:.1f}s ({len(books) / elapsed:.1f} books/s)", fg=typer.colors.CYAN)
    for action, count in counts.items():
        typer.echo(f"  {action:14s} {count}")

    if format == "json":
        typer.echo(
            json_lib.dumps(
                {"run_id": run_id, "elapsed_seconds": elapsed, "verdicts": verdicts},
                indent=2,
                default=str,
            )
        )


@app.command()
def hosts(
    ctx: typer.Context,  # noqa: ARG001
) -> None:
    """
    v1.0: Discover and report homelab inference hosts (Ollama, LM Studio, etc.).
    """
    import asyncio as _aio

    from calibre_ai_auditor.verification.host_registry import (
        HostRegistry,
        HostRegistryConfig,
        default_felix_homelab,
    )

    async def _run() -> dict[str, Any]:
        cfg = HostRegistryConfig(hosts=default_felix_homelab())
        reg = HostRegistry(cfg)
        try:
            await reg.health_check_all()
            return reg.summary()
        finally:
            await reg.__aexit__(None, None, None)

    summary = _aio.run(_run())
    typer.echo(json_lib.dumps(summary, indent=2, default=str))


@app.command()
def mcp(
    ctx: typer.Context,  # noqa: ARG001
) -> None:
    """
    v1.2: Start the MCP server (STDIO) exposing read-only audit tools.

    Tools: query_book_audit, list_problematic_books, get_run_metrics, list_recent_runs.
    Requires the optional 'mcp' extra (fastmcp).

    Intended for Hermes and other MCP clients. Run via: bookaudit mcp
    """
    try:
        from calibre_ai_auditor.mcp_server import mcp as mcp_app
    except ImportError as e:
        typer.secho(
            "MCP support not installed. Install with: uv pip install -e '.[mcp]' (or pip install fastmcp)",
            fg=typer.colors.RED,
        )
        raise typer.Exit(1) from e

    typer.echo("Starting calibre-audit MCP server (stdio)...")
    mcp_app.run()


@app.command()
def writer_health(ctx: typer.Context) -> None:
    """Exit successfully only when the production writer heartbeat is fresh."""
    from calibre_ai_auditor.apply.heartbeat import heartbeat_is_fresh, read_writer_heartbeat

    settings: Settings = ctx.obj
    if settings.queue.backend != "valkey":
        raise typer.Exit(1)
    try:
        heartbeat = read_writer_heartbeat(
            settings.queue.valkey_url,
            timeout=settings.queue.connect_timeout_seconds,
        )
    except Exception:
        raise typer.Exit(1) from None
    if not heartbeat_is_fresh(heartbeat, max_age_seconds=settings.writer_heartbeat_max_age_seconds):
        raise typer.Exit(1)
    typer.echo("writer heartbeat is fresh")


@app.command()
def writer(
    ctx: typer.Context,
    poll_seconds: Annotated[float, typer.Option("--poll-seconds", min=0.1)] = 1.0,
    once: Annotated[bool, typer.Option("--once")] = False,
) -> None:
    """Run the dedicated durable metadata writer loop."""
    import time

    from sqlmodel import Session

    from calibre_ai_auditor.apply.guard import acquire_writer_guard, release_writer_guard, writer_guard_is_held
    from calibre_ai_auditor.apply.writer import (
        MetadataWriter,
        claim_next_operation,
        fail_operation,
        reconcile_incomplete_operations,
    )

    settings: Settings = ctx.obj
    if not settings.library.path:
        typer.secho("Writer requires BOOKAUDIT_LIBRARY_PATH", fg=typer.colors.RED)
        raise typer.Exit(1)
    if not settings.library.path.is_dir() or not os.access(settings.library.path, os.R_OK | os.W_OK):
        typer.secho("Writer requires a readable and writable Calibre library directory", fg=typer.colors.RED)
        raise typer.Exit(1)
    if not settings.storage.artifacts_dir.is_dir() or not os.access(settings.storage.artifacts_dir, os.R_OK | os.W_OK):
        typer.secho("Writer requires a readable and writable artifacts directory", fg=typer.colors.RED)
        raise typer.Exit(1)
    if shutil.which("calibredb") is None:
        typer.secho("Writer requires calibredb on PATH", fg=typer.colors.RED)
        raise typer.Exit(1)
    engine = get_engine(settings)
    writer_guard = None
    if engine.dialect.name == "postgresql":
        writer_guard = engine.connect()
        acquired = acquire_writer_guard(writer_guard)
        if not acquired:
            writer_guard.close()
            typer.secho("Another metadata writer owns the production writer lock", fg=typer.colors.RED)
            raise typer.Exit(1)
        current_user, revision = writer_guard.execute(
            text("SELECT current_user, (SELECT version_num FROM alembic_version)")
        ).one()
        if current_user != "bookaudit_writer" or revision != expected_schema_revision():
            writer_guard.close()
            typer.secho("Writer database role or schema revision is invalid", fg=typer.colors.RED)
            raise typer.Exit(1)
    cli = CalibreCLI(settings.library.path)
    pilot_settings = settings.manifestation_v2.supervised_pilot
    runtime_pilot = None
    if pilot_settings.enabled and pilot_settings.pilot_id and pilot_settings.release_digest:
        canonical_library_root = str(Path(os.path.normpath(os.path.abspath(settings.library.path))))
        runtime_pilot = PilotGuard(
            enabled=True,
            pilot_id=pilot_settings.pilot_id,
            library_root=canonical_library_root,
            release_digest=pilot_settings.release_digest,
            alembic_revision=expected_schema_revision(),
            max_operations=pilot_settings.max_operations,
            writer_ready=True,
        )
    metadata_writer = MetadataWriter(
        cli,
        ApplyEngine(cli, settings.storage.artifacts_dir),
        pilot=runtime_pilot,
    )
    heartbeat_owner = str(uuid4())

    # All claims and external writes use the same PostgreSQL connection that
    # owns the session advisory lock. If that connection dies, the next query
    # fails and the process exits instead of continuing unfenced on a new pool
    # connection while another writer takes ownership.
    session = Session(bind=writer_guard if writer_guard is not None else engine)
    try:
        reconciled = reconcile_incomplete_operations(session, cli, settings.storage.artifacts_dir)
        if reconciled:
            typer.echo(f"Reconciled {len(reconciled)} interrupted operations")

        while True:
            if writer_guard is not None and not writer_guard_is_held(writer_guard):
                raise RuntimeError("Metadata writer lost its PostgreSQL advisory lock")
            if settings.queue.backend == "valkey":
                from calibre_ai_auditor.apply.heartbeat import library_root_sha256, publish_writer_heartbeat

                publish_writer_heartbeat(
                    settings.queue.valkey_url,
                    heartbeat_owner,
                    timeout=settings.queue.connect_timeout_seconds,
                    release_digest=pilot_settings.release_digest if pilot_settings.enabled else None,
                    alembic_revision=expected_schema_revision() if pilot_settings.enabled else None,
                    library_root_sha256=(
                        library_root_sha256(settings.library.path) if pilot_settings.enabled else None
                    ),
                    pilot_id=pilot_settings.pilot_id if pilot_settings.enabled else None,
                    max_operations=pilot_settings.max_operations if pilot_settings.enabled else None,
                )
            operation_id = claim_next_operation(session)
            if operation_id:
                try:
                    metadata_writer.process(session, operation_id)
                except Exception as exc:
                    logger.exception("Writer operation %s failed", operation_id)
                    session.rollback()
                    fail_operation(session, operation_id, str(exc))
            if once:
                return
            if operation_id is None:
                time.sleep(poll_seconds)
    finally:
        session.close()
        if writer_guard is not None:
            if not writer_guard.invalidated and writer_guard_is_held(writer_guard):
                release_writer_guard(writer_guard)
            writer_guard.close()


if __name__ == "__main__":
    app()

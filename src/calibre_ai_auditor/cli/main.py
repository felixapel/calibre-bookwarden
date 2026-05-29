import asyncio
import json as json_lib
import shutil
from datetime import datetime
from pathlib import Path
from typing import Annotated

import typer
from sqlmodel import Session, desc, select

from calibre_ai_auditor.apply.engine import ApplyEngine
from calibre_ai_auditor.audit.engine import run_audit
from calibre_ai_auditor.calibre.cli import CalibreCLI
from calibre_ai_auditor.config.settings import Settings, load_settings
from calibre_ai_auditor.extractors.heuristics import extract_heuristics
from calibre_ai_auditor.extractors.text import extract_snippets
from calibre_ai_auditor.storage.db import get_engine, init_db
from calibre_ai_auditor.storage.models import (
    BookRecord,
    Change,
    EvidencePackage,
    Run,
)

app = typer.Typer(pretty_exceptions_show_locals=False)


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
            progress_callback=lambda current, total: typer.echo(
                f"  Auditing book {current}/{total}"
            ),
        )
    typer.secho("Audit complete.", fg=typer.colors.GREEN)


@app.callback()
def main(
    ctx: typer.Context,
    config: Annotated[
        Path | None, typer.Option("--config", "-c", help="Path to config.yml")
    ] = None,
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
    tools = ["calibredb", "ebook-meta", "fetch-ebook-metadata", "ocrmypdf"]
    for tool in tools:
        path = shutil.which(tool)
        status = (
            typer.style("FOUND", fg=typer.colors.GREEN)
            if path
            else typer.style("MISSING", fg=typer.colors.RED)
        )
        typer.echo(f"  {tool:25} : {status} ({path or 'N/A'})")

    typer.echo(f"\nLibrary path: {settings.library.path}")
    typer.echo(f"DB path:      {settings.storage.sqlite_path}")


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
            existing = session.exec(
                select(BookRecord).where(BookRecord.book_key == book_key)
            ).first()
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
                record = session.exec(
                    select(BookRecord).where(BookRecord.book_key == book_key)
                ).first()
                if record:
                    asyncio.run(indexer.index_book(record))

    typer.secho(f"Scan complete. Found {len(books)} books.", fg=typer.colors.GREEN)


@app.command()
def inspect(
    ctx: typer.Context,
    path: Annotated[Path, typer.Option("--path", help="Path to ebook file")],
    no_providers: Annotated[
        bool, typer.Option("--no-providers", help="Skip external fetch")
    ] = False,
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
    judge: Annotated[
        bool, typer.Option("--judge/--no-judge", help="Enable or skip LLM judgment")
    ] = True,
    save_evidence: Annotated[
        bool, typer.Option("--save-evidence", help="Persist evidence JSON")
    ] = True,
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
    Apply approved fixes after backup.
    """
    settings: Settings = ctx.obj
    engine = get_engine(settings)
    cli = CalibreCLI(settings.library.path)
    apply_engine = ApplyEngine(cli, settings.storage.artifacts_dir)

    with Session(engine) as session:
        final_run_id = run
        if run == "latest":
            run_stmt = select(Run).order_by(desc(Run.created_at)).limit(1)
            r = session.exec(run_stmt).first()
            if not r:
                typer.secho("Error: No runs found.", fg=typer.colors.RED)
                raise typer.Exit(1)
            final_run_id = r.run_id

        book_stmt = select(BookRecord).where(BookRecord.run_id == final_run_id)
        if safe_only:
            book_stmt = book_stmt.where(BookRecord.status == "suggest_fix")

        books = session.exec(book_stmt).all()

        if not books:
            typer.echo("No applicable fixes found.")
            return

        typer.echo(f"Found {len(books)} fixes to apply.")
        if not yes and not typer.confirm("Proceed with applying these changes?"):
            raise typer.Abort()

        for book in books:
            ev_stmt = (
                select(EvidencePackage)
                .where(EvidencePackage.book_key == book.book_key)
                .where(EvidencePackage.run_id == final_run_id)
            )
            pkg = session.exec(ev_stmt).first()
            if not pkg or not pkg.decision:
                continue

            patch = pkg.decision.get("proposed_patch")
            if not patch:
                continue

            typer.echo(f"  Applying fix to: {book.current_metadata.get('title')}...")
            try:
                change = apply_engine.apply_patch(session, book, patch)
                session.add(change)
                book.status = "applied"
            except Exception as e:
                typer.secho(f"    Failed: {e}", fg=typer.colors.RED)

        session.commit()
    typer.secho("Apply complete.", fg=typer.colors.GREEN)


@app.command()
def undo(
    ctx: typer.Context,
    change_id: Annotated[int, typer.Argument(help="ID of the change to revert")],
) -> None:
    """
    Revert one applied change.
    """
    settings: Settings = ctx.obj
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

    async def run_ingest():
        engine = get_engine(settings)
        init_db(settings)

        # Test connection first
        if not await bridge.test_connection():
            typer.secho(
                "Error: Could not connect to Paperless-ngx. Check logs.", fg=typer.colors.RED
            )
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
                existing = session.exec(
                    select(BookRecord).where(BookRecord.book_key == book_key)
                ).first()

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


if __name__ == "__main__":
    app()

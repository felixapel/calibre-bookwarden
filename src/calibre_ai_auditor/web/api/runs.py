from collections.abc import Generator
from datetime import datetime
from pathlib import Path
from typing import Annotated, Any

from fastapi import APIRouter, Body, Depends, HTTPException
from pydantic import BaseModel
from sqlmodel import Session, col, desc, select

from calibre_ai_auditor.apply.engine import ApplyEngine
from calibre_ai_auditor.calibre.cli import CalibreCLI
from calibre_ai_auditor.config.settings import Settings, load_settings
from calibre_ai_auditor.storage.db import get_engine
from calibre_ai_auditor.storage.models import BookRecord, Change, Run
from calibre_ai_auditor.web.jobs import get_job_status, start_job
from calibre_ai_auditor.web.safety import require_write_confirmation
from calibre_ai_auditor.web.schemas import RevertRequest

router = APIRouter()


def get_settings() -> Settings:
    return load_settings()


def get_session(
    settings: Annotated[Settings, Depends(get_settings)],
) -> Generator[Session, None, None]:
    engine = get_engine(settings)
    with Session(engine) as session:
        yield session


class ScanRequest(BaseModel):
    library: str | None = None
    search: str | None = None
    limit: int | None = None


async def do_scan(settings: Settings, req: ScanRequest) -> dict[str, Any]:
    library_path = Path(req.library) if req.library else settings.library.path
    if not library_path:
        raise ValueError("Library path not set.")

    run_id = f"run_{datetime.now().strftime('%Y%m%d_%H%M%S')}"
    cli = CalibreCLI(library_path)
    books = cli.list_books(search=req.search)

    if req.limit:
        books = books[: req.limit]

    engine = get_engine(settings)
    with Session(engine) as session:
        run = Run(run_id=run_id, metadata_filter=req.search)
        session.add(run)

        for book in books:
            authors_str = book.get("authors", "")
            authors = [a.strip() for a in authors_str.split("&")] if isinstance(authors_str, str) else []  # noqa: SIM108

            book_key = f"calibre:{book['id']}"
            existing = session.exec(select(BookRecord).where(BookRecord.book_key == book_key)).first()
            if existing:
                existing.run_id = run_id
                existing.current_metadata = {
                    "title": book.get("title"),
                    "authors": authors,
                    "identifiers": book.get("identifiers", {}),
                }
                existing.files = [{"path": f, "format": Path(f).suffix[1:].lower()} for f in book.get("formats", [])]
                existing.status = "scanned"
                book_record = existing
            else:
                book_record = BookRecord(
                    book_key=book_key,
                    run_id=run_id,
                    calibre_book_id=book["id"],
                    source="calibre",
                    current_metadata={
                        "title": book.get("title"),
                        "authors": authors,
                        "identifiers": book.get("identifiers", {}),
                    },
                    files=[{"path": f, "format": Path(f).suffix[1:].lower()} for f in book.get("formats", [])],
                )
            try:
                full_metadata = cli.show_metadata(book["id"])
                lang = full_metadata.get("languages", [None])[0] if full_metadata.get("languages") else None
                book_record.current_metadata.update(
                    {
                        "publisher": full_metadata.get("publisher"),
                        "published_date": full_metadata.get("pubdate"),
                        "language": lang,
                        "series": full_metadata.get("series"),
                        "series_index": full_metadata.get("series_index"),
                        "tags": full_metadata.get("tags", []),
                    }
                )
            except Exception:
                pass

            session.add(book_record)

        run.status = "completed"
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
            for book in books:
                book_key = f"calibre:{book['id']}"
                record = session.exec(select(BookRecord).where(BookRecord.book_key == book_key)).first()
                if record:
                    await indexer.index_book(record)

    return {"run_id": run_id, "books_found": len(books)}


@router.get("/runs")
async def list_runs(session: Annotated[Session, Depends(get_session)]) -> Any:
    runs = session.exec(select(Run).order_by(desc(Run.created_at))).all()
    return runs


@router.post("/runs/scan")
async def scan(req: ScanRequest, settings: Annotated[Settings, Depends(get_settings)]) -> dict[str, str]:
    job_id = await start_job("scan", do_scan, settings, req)
    return {"job_id": job_id}


@router.get("/jobs/{job_id}")
async def get_job(job_id: str) -> dict[str, Any]:
    job = await get_job_status(job_id)
    if not job:
        raise HTTPException(status_code=404, detail="Job not found")
    return job.model_dump()


@router.post("/runs/{run_id}/revert")
async def revert_run(
    run_id: str,
    session: Annotated[Session, Depends(get_session)],
    settings: Annotated[Settings, Depends(get_settings)],
    req: RevertRequest = Body(default_factory=RevertRequest),
) -> dict[str, Any]:
    require_write_confirmation(settings, force=req.force)

    if not settings.library.path:
        raise HTTPException(status_code=400, detail="Library path not set")

    # Find all changes for this run
    changes_stmt = (
        select(Change)
        .where(Change.run_id == run_id)
        .where(col(Change.status).in_(("pending_apply", "applied", "failed_rollback_failed")))
    )
    changes = session.exec(changes_stmt).all()

    if not changes:
        return {
            "status": "success",
            "data": {"message": f"No active changes found to revert for run {run_id}"},
        }

    cli = CalibreCLI(settings.library.path)
    apply_engine = ApplyEngine(cli, settings.storage.artifacts_dir)

    reverted_count = 0
    errors = []

    for change in changes:
        try:
            apply_engine.undo_change(session, change)

            # Reset book record status back to suggest_fix
            book_stmt = select(BookRecord).where(BookRecord.book_key == change.book_key)
            book = session.exec(book_stmt).first()
            if book:
                book.status = "suggest_fix"
                session.add(book)

            reverted_count += 1
        except Exception as e:
            errors.append(f"Failed to revert change {change.id} for book {change.book_key}: {e}")

    session.commit()

    if errors:
        raise HTTPException(status_code=500, detail=f"Revert completed with errors: {'; '.join(errors)}")

    return {
        "status": "success",
        "data": {"message": f"Successfully reverted {reverted_count} changes for run {run_id}"},
    }

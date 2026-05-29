import logging
from collections.abc import Generator
from typing import Any

from fastapi import APIRouter, Body, Depends, HTTPException
from sqlmodel import Session, select

from calibre_ai_auditor.apply.engine import ApplyEngine
from calibre_ai_auditor.calibre.cli import CalibreCLI
from calibre_ai_auditor.config.settings import load_settings
from calibre_ai_auditor.storage.db import get_engine
from calibre_ai_auditor.storage.models import BookRecord, Change, EvidencePackage
from calibre_ai_auditor.web.safety import require_write_confirmation
from calibre_ai_auditor.web.schemas import APIResponse, ApplyRequest, LockFieldRequest, UndoRequest

logger = logging.getLogger(__name__)

router = APIRouter(prefix="", tags=["Review and Apply"])


def get_session() -> Generator[Session, None, None]:
    settings = load_settings()
    engine = get_engine(settings)
    with Session(engine) as session:
        yield session


@router.post("/review/{book_key}/approve", response_model=APIResponse)
async def approve_patch(book_key: str, session: Session = Depends(get_session)) -> Any:
    statement = select(BookRecord).where(BookRecord.book_key == book_key)
    book = session.exec(statement).first()
    if not book:
        raise HTTPException(status_code=404, detail="Book record not found")

    book.status = "suggest_fix"
    session.add(book)
    session.commit()
    return {"status": "success", "data": {"message": f"Patch for {book_key} approved"}}


@router.post("/review/{book_key}/reject", response_model=APIResponse)
async def reject_patch(book_key: str, session: Session = Depends(get_session)) -> Any:
    statement = select(BookRecord).where(BookRecord.book_key == book_key)
    book = session.exec(statement).first()
    if not book:
        raise HTTPException(status_code=404, detail="Book record not found")

    book.status = "scanned"
    session.add(book)
    session.commit()
    return {"status": "success", "data": {"message": f"Patch for {book_key} rejected"}}


@router.post("/review/{book_key}/lock-field", response_model=APIResponse)
async def lock_field(
    book_key: str,
    req: LockFieldRequest,
    session: Session = Depends(get_session),
) -> Any:
    statement = select(BookRecord).where(BookRecord.book_key == book_key)
    book = session.exec(statement).first()
    if not book:
        raise HTTPException(status_code=404, detail="Book record not found")

    locks = dict(book.field_locks or {})
    locks[req.field] = req.value
    book.field_locks = locks
    session.add(book)
    session.commit()
    return {
        "status": "success",
        "data": {"message": f"Field {req.field} locked for {book_key}", "field_locks": locks},
    }


@router.post("/apply", response_model=APIResponse)
async def apply_patches(
    req: ApplyRequest = Body(default_factory=ApplyRequest),
    session: Session = Depends(get_session),
) -> Any:
    settings = load_settings()
    require_write_confirmation(settings, force=req.force)

    if not settings.library.path:
        raise HTTPException(status_code=400, detail="Library path not set")

    # Find books that have been approved / marked safe
    statement = select(BookRecord).where(BookRecord.status == "suggest_fix")
    books = session.exec(statement).all()

    if not books:
        return {
            "status": "success",
            "data": {"message": "No pending approved fixes to apply", "applied_count": 0},
        }

    cli = CalibreCLI(settings.library.path)
    apply_engine = ApplyEngine(cli, settings.storage.artifacts_dir)
    applied_count = 0

    for book in books:
        # Retrieve the matching evidence package for the book's current run
        ev_stmt = (
            select(EvidencePackage)
            .where(EvidencePackage.book_key == book.book_key)
            .where(EvidencePackage.run_id == book.run_id)
        )
        pkg = session.exec(ev_stmt).first()
        if not pkg or not pkg.decision:
            continue

        patch = pkg.decision.get("proposed_patch")
        if not patch:
            continue

        locks = book.field_locks or {}
        if locks:
            patch = {k: v for k, v in patch.items() if k not in locks}
        if not patch:
            continue

        try:
            change = apply_engine.apply_patch(session, book, patch)
            session.add(change)
            book.status = "applied"
            session.add(book)
            applied_count += 1
        except Exception as e:
            # Log failure but continue with other books
            logger.error(f"Failed to apply patch for book {book.book_key}: {e}")

    session.commit()
    return {
        "status": "success",
        "data": {"message": f"Applied {applied_count} patches", "applied_count": applied_count},
    }


@router.post("/undo/{change_id}", response_model=APIResponse)
async def undo_change(
    change_id: int,
    req: UndoRequest = Body(default_factory=UndoRequest),
    session: Session = Depends(get_session),
) -> Any:
    statement = select(Change).where(Change.id == change_id)
    change = session.exec(statement).first()
    if not change:
        raise HTTPException(status_code=404, detail="Change not found")

    settings = load_settings()
    require_write_confirmation(settings, force=req.force)

    if not settings.library.path:
        raise HTTPException(status_code=400, detail="Library path not set")

    if change.status == "undone":
        raise HTTPException(status_code=400, detail="Change is already undone")

    cli = CalibreCLI(settings.library.path)
    apply_engine = ApplyEngine(cli, settings.storage.artifacts_dir)

    try:
        apply_engine.undo_change(session, change)

        # Reset book record status back to suggest_fix
        book_stmt = select(BookRecord).where(BookRecord.book_key == change.book_key)
        book = session.exec(book_stmt).first()
        if book:
            book.status = "suggest_fix"
            session.add(book)

        session.commit()
    except Exception as e:
        raise HTTPException(status_code=500, detail=f"Failed to undo change: {e}")

    return {"status": "success", "data": {"message": f"Change {change_id} successfully undone"}}

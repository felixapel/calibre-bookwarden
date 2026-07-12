from collections.abc import Generator
from typing import Any, cast

from fastapi import APIRouter, Body, Depends, HTTPException
from sqlalchemy import update
from sqlmodel import Session, select

from calibre_ai_auditor.apply.coordinator import (
    create_manual_authorization,
    queue_approved_operations,
    queue_undo_operation,
)
from calibre_ai_auditor.config.settings import load_settings
from calibre_ai_auditor.storage.db import get_engine
from calibre_ai_auditor.storage.models import BookRecord, Change, EvidencePackage
from calibre_ai_auditor.verification.verdict import BookVerdict
from calibre_ai_auditor.web.schemas import (
    APIResponse,
    ApplyRequest,
    LockFieldRequest,
    ManualAuthorizationRequest,
    UndoRequest,
)

router = APIRouter(prefix="", tags=["Review and Apply"])


def get_session() -> Generator[Session, None, None]:
    settings = load_settings()
    engine = get_engine(settings)
    with Session(engine) as session:
        yield session


def claim_book_for_apply(session: Session, book_id: int) -> bool:
    """Atomically claim an approved book before starting external writes."""
    table = cast(Any, BookRecord).__table__
    result = session.execute(
        update(BookRecord).where(table.c.id == book_id).where(table.c.status == "suggest_fix").values(status="applying")
    )
    session.commit()
    return bool(getattr(result, "rowcount", 0) == 1)


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
    if not req.force:
        raise HTTPException(status_code=400, detail="Apply requires explicit confirmation with force=true")
    result = queue_approved_operations(
        session,
        authorization_ids=req.authorization_ids,
    )

    return {
        "status": "success",
        "data": {
            "message": f"Queued {len(result.queued_operation_ids)} metadata operations",
            "queued_count": len(result.queued_operation_ids),
            "operation_ids": result.queued_operation_ids,
            "skipped_book_keys": result.skipped_book_keys,
        },
    }


@router.post("/review/{book_key}/authorize", response_model=APIResponse)
async def authorize_patch(
    book_key: str,
    req: ManualAuthorizationRequest,
    session: Session = Depends(get_session),
) -> Any:
    book = session.exec(select(BookRecord).where(BookRecord.book_key == book_key)).first()
    if book is None:
        raise HTTPException(status_code=404, detail="Book record not found")
    package = session.exec(
        select(EvidencePackage)
        .where(EvidencePackage.book_key == book.book_key)
        .where(EvidencePackage.run_id == book.run_id)
    ).first()
    if package is None or package.decision is None:
        raise HTTPException(status_code=409, detail="No persisted verdict is available")
    try:
        verdict = BookVerdict.model_validate(package.decision)
        authorization = create_manual_authorization(
            session,
            book=book,
            verdict=verdict,
            # The production authentication boundary currently has one API-key
            # principal. Never accept an audit identity asserted by the client.
            actor="api-key",
            reason=req.reason,
        )
        session.commit()
    except ValueError as exc:
        raise HTTPException(status_code=422, detail=str(exc)) from None
    return {"status": "success", "data": {"authorization_id": authorization.authorization_id}}


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

    if not req.force:
        raise HTTPException(status_code=400, detail="Undo requires explicit confirmation with force=true")

    if change.status == "undone":
        raise HTTPException(status_code=400, detail="Change is already undone")

    operation_id = queue_undo_operation(session, change)
    return {
        "status": "success",
        "data": {"message": f"Change {change_id} queued for undo", "operation_id": operation_id},
    }

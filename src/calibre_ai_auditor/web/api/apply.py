import asyncio
import logging
import os
from collections.abc import Generator
from pathlib import Path
from typing import Any, cast

from fastapi import APIRouter, Body, Depends, HTTPException
from pydantic import BaseModel, ConfigDict, Field
from sqlalchemy import update
from sqlmodel import Session, select

from calibre_ai_auditor.apply.coordinator import (
    PilotGuard,
    create_manual_authorization,
    create_v2_manual_authorization,
    load_v2_package,
    queue_undo_operation,
    queue_v2_operation,
)
from calibre_ai_auditor.apply.heartbeat import (
    heartbeat_is_fresh,
    heartbeat_matches_pilot,
    library_root_sha256,
    read_writer_heartbeat,
)
from calibre_ai_auditor.config.settings import Settings, load_settings
from calibre_ai_auditor.storage.db import expected_schema_revision, get_engine
from calibre_ai_auditor.storage.models import BookRecord, Change, EvidencePackage
from calibre_ai_auditor.verification.metrics import get_metrics
from calibre_ai_auditor.verification.verdict import BookVerdict
from calibre_ai_auditor.web.schemas import (
    APIResponse,
    ApplyRequest,
    LockFieldRequest,
    ManualAuthorizationRequest,
    UndoRequest,
)

router = APIRouter(prefix="", tags=["Review and Apply"])
logger = logging.getLogger(__name__)


class V2ApplyRequest(BaseModel):
    model_config = ConfigDict(extra="forbid")

    force: bool = False
    evidence_id: str = Field(min_length=1, max_length=128)
    authorization_id: str = Field(min_length=1, max_length=128)


def get_settings() -> Settings:
    return load_settings()


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
    raise HTTPException(
        status_code=410,
        detail="Legacy V1 apply is disabled; authorize and queue a sealed Manifestation V2 evidence package",
    )


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


@router.post("/review/v2/{evidence_id}/authorize", response_model=APIResponse)
async def authorize_v2_patch(
    evidence_id: str,
    req: ManualAuthorizationRequest,
    session: Session = Depends(get_session),
) -> Any:
    metrics = get_metrics()
    try:
        _stored, package = load_v2_package(session, evidence_id)
        book = session.exec(select(BookRecord).where(BookRecord.book_key == package.book_key)).first()
        if book is None:
            raise ValueError("Book record for the evidence package is unavailable")
        authorization = create_v2_manual_authorization(
            session,
            book=book,
            package=package,
            actor="api-key",
            reason=req.reason,
        )
        session.commit()
    except ValueError as exc:
        metrics.record_v2_authorization("rejected")
        logger.warning(
            "V2 evidence authorization rejected",
            extra={
                "event": "v2_authorization_rejected",
                "evidence_id": evidence_id,
                "outcome": "rejected",
            },
        )
        raise HTTPException(status_code=422, detail=str(exc)) from None
    metrics.record_v2_authorization("success")
    logger.info(
        "V2 evidence authorization created",
        extra={
            "event": "v2_authorization_created",
            "evidence_id": evidence_id,
            "book_key": package.book_key,
            "outcome": "success",
            "tier": package.identity.tier.value,
        },
    )
    return {"status": "success", "data": {"authorization_id": authorization.authorization_id}}


@router.post("/apply/v2", response_model=APIResponse)
async def apply_v2_patches(
    req: V2ApplyRequest,
    session: Session = Depends(get_session),
    settings: Settings = Depends(get_settings),
) -> Any:
    metrics = get_metrics()
    if not req.force:
        metrics.record_v2_apply_request("rejected")
        raise HTTPException(status_code=400, detail="V2 apply requires explicit confirmation with force=true")
    pilot_settings = settings.manifestation_v2.supervised_pilot
    if not pilot_settings.enabled:
        metrics.record_v2_apply_request("disabled")
        logger.warning(
            "Supervised V2 apply rejected by kill switch",
            extra={"event": "v2_apply_disabled", "evidence_id": req.evidence_id, "outcome": "disabled"},
        )
        raise HTTPException(status_code=503, detail="Supervised V2 apply is disabled")
    if not pilot_settings.pilot_id or not pilot_settings.release_digest or settings.library.path is None:
        metrics.record_v2_apply_request("disabled")
        raise HTTPException(status_code=503, detail="Supervised V2 pilot binding is incomplete")

    try:
        heartbeat = await asyncio.to_thread(
            read_writer_heartbeat,
            settings.queue.valkey_url,
            timeout=settings.queue.connect_timeout_seconds,
        )
    except Exception as exc:
        metrics.record_v2_apply_request("writer_unavailable")
        raise HTTPException(status_code=503, detail="Writer heartbeat is unavailable") from exc
    revision = expected_schema_revision()
    library_root = str(Path(os.path.normpath(os.path.abspath(settings.library.path))))
    writer_ready = heartbeat_is_fresh(
        heartbeat,
        max_age_seconds=settings.writer_heartbeat_max_age_seconds,
    ) and heartbeat_matches_pilot(
        heartbeat,
        release_digest=pilot_settings.release_digest,
        alembic_revision=revision,
        library_root_sha256=library_root_sha256(library_root),
        pilot_id=pilot_settings.pilot_id,
        max_operations=pilot_settings.max_operations,
    )
    if not writer_ready:
        metrics.record_v2_apply_request("writer_unavailable")
        logger.warning(
            "Supervised V2 apply rejected by writer binding",
            extra={
                "event": "v2_writer_binding_rejected",
                "evidence_id": req.evidence_id,
                "pilot_id": pilot_settings.pilot_id,
                "outcome": "writer_unavailable",
            },
        )
        raise HTTPException(status_code=409, detail="Fresh writer heartbeat does not match the pilot runtime")
    try:
        operation_id = queue_v2_operation(
            session,
            evidence_id=req.evidence_id.strip(),
            authorization_id=req.authorization_id.strip(),
            pilot=PilotGuard(
                enabled=pilot_settings.enabled,
                pilot_id=pilot_settings.pilot_id,
                library_root=library_root,
                release_digest=pilot_settings.release_digest,
                alembic_revision=revision,
                max_operations=pilot_settings.max_operations,
                writer_ready=writer_ready,
            ),
        )
    except ValueError as exc:
        metrics.record_v2_apply_request("conflict")
        logger.warning(
            "Supervised V2 apply request conflicted with a safety gate",
            extra={
                "event": "v2_apply_conflict",
                "evidence_id": req.evidence_id,
                "pilot_id": pilot_settings.pilot_id,
                "outcome": "conflict",
            },
        )
        raise HTTPException(status_code=409, detail=str(exc)) from None
    metrics.record_v2_apply_request("queued")
    logger.info(
        "Supervised V2 operation queued",
        extra={
            "event": "v2_apply_queued",
            "evidence_id": req.evidence_id,
            "operation_id": operation_id,
            "pilot_id": pilot_settings.pilot_id,
            "outcome": "queued",
        },
    )
    return {
        "status": "success",
        "data": {
            "operation_id": operation_id,
            "evidence_id": req.evidence_id.strip(),
            "pilot_id": pilot_settings.pilot_id,
        },
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

    if not req.force:
        raise HTTPException(status_code=400, detail="Undo requires explicit confirmation with force=true")

    if change.status == "undone":
        raise HTTPException(status_code=400, detail="Change is already undone")

    operation_id = queue_undo_operation(session, change)
    return {
        "status": "success",
        "data": {"message": f"Change {change_id} queued for undo", "operation_id": operation_id},
    }

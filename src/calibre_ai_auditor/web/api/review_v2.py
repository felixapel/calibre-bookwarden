"""Read-only review projections for sealed Manifestation V2 evidence."""

from __future__ import annotations

import logging
from collections.abc import Generator
from datetime import datetime
from typing import Any, Literal, cast

from fastapi import APIRouter, Depends, HTTPException, Query
from pydantic import BaseModel
from sqlalchemy import func
from sqlmodel import Session, desc, select

from calibre_ai_auditor.apply.coordinator import find_current_v2_authorization, load_v2_package
from calibre_ai_auditor.config.settings import load_settings
from calibre_ai_auditor.storage.db import get_engine
from calibre_ai_auditor.storage.models import EvidencePackage, OperationLedger
from calibre_ai_auditor.verification.identity_v2 import IdentityTier
from calibre_ai_auditor.verification.pipeline_v2 import BookAuditState, EvidencePackageV2

logger = logging.getLogger(__name__)
router = APIRouter(tags=["Manifestation V2 Review"])


class ReviewV2Summary(BaseModel):
    evidence_id: str
    run_id: str
    book_key: str
    created_at: datetime
    state: BookAuditState
    tier: IdentityTier
    current_metadata: dict[str, Any]
    manifestation_ids: dict[str, str]
    patch_fields: list[str]
    risk_flags: list[str]


class AuthorizationStatus(BaseModel):
    authorization_id: str
    actor: str
    reason: str
    created_at: datetime


class OperationStatus(BaseModel):
    operation_id: str
    state: str
    pilot_id: str | None
    change_id: int | None
    created_at: datetime
    updated_at: datetime
    completed_at: datetime | None
    error_code: str | None


class ReviewV2Detail(BaseModel):
    package: EvidencePackageV2
    authorization: AuthorizationStatus | None
    operation: OperationStatus | None


class ReviewV2ListEnvelope(BaseModel):
    status: Literal["success"] = "success"
    data: list[ReviewV2Summary]
    meta: dict[str, int]


class ReviewV2DetailEnvelope(BaseModel):
    status: Literal["success"] = "success"
    data: ReviewV2Detail


class OperationStatusEnvelope(BaseModel):
    status: Literal["success"] = "success"
    data: OperationStatus


def get_session() -> Generator[Session, None, None]:
    settings = load_settings()
    with Session(get_engine(settings)) as session:
        yield session


def _summary(package: EvidencePackageV2) -> ReviewV2Summary:
    return ReviewV2Summary(
        evidence_id=package.evidence_id,
        run_id=package.run_id,
        book_key=package.book_key,
        created_at=package.created_at,
        state=package.state,
        tier=package.identity.tier,
        current_metadata=package.snapshot.current_metadata,
        manifestation_ids=package.identity.manifestation_ids,
        patch_fields=sorted(package.identity.auto_patch),
        risk_flags=package.identity.risk_flags,
    )


def _error_code(operation: OperationLedger) -> str | None:
    if operation.state == "unknown":
        return "reconciliation_required"
    if operation.state == "restore_failed":
        return "restore_failed"
    if operation.state == "failed":
        return "operation_failed"
    if operation.error:
        return "operation_error"
    return None


def _operation_status(operation: OperationLedger | None) -> OperationStatus | None:
    if operation is None:
        return None
    return OperationStatus(
        operation_id=operation.operation_id,
        state=operation.state,
        pilot_id=operation.pilot_id,
        change_id=operation.change_id,
        created_at=operation.created_at,
        updated_at=operation.updated_at,
        completed_at=operation.completed_at,
        error_code=_error_code(operation),
    )


@router.get("/review/v2", response_model=ReviewV2ListEnvelope)
async def list_review_v2(
    run_id: str | None = Query(default=None, min_length=1, max_length=128),
    state: BookAuditState | None = None,
    tier: IdentityTier | None = None,
    limit: int = Query(default=50, ge=1, le=100),
    offset: int = Query(default=0, ge=0),
    session: Session = Depends(get_session),
) -> ReviewV2ListEnvelope:
    table = cast(Any, EvidencePackage).__table__
    filters = [table.c.schema_version == 2]
    if run_id is not None:
        filters.append(table.c.run_id == run_id)
    if state is not None:
        filters.append(table.c.observations["state"].as_string() == state.value)
    if tier is not None:
        filters.append(table.c.observations["identity"]["tier"].as_string() == tier.value)

    total = session.exec(select(func.count()).select_from(EvidencePackage).where(*filters)).one()
    rows = session.exec(
        select(EvidencePackage)
        .where(*filters)
        .order_by(desc(EvidencePackage.created_at), desc(EvidencePackage.id))
        .offset(offset)
        .limit(limit)
    ).all()

    packages: list[EvidencePackageV2] = []
    for row in rows:
        try:
            _stored, package = load_v2_package(session, row.evidence_id)
        except ValueError:
            logger.warning(
                "review_v2_invalid_evidence",
                extra={"evidence_id": row.evidence_id, "run_id": row.run_id},
            )
            raise HTTPException(
                status_code=409,
                detail=f"Invalid sealed V2 evidence: {row.evidence_id}",
            ) from None
        packages.append(package)

    return ReviewV2ListEnvelope(
        data=[_summary(package) for package in packages],
        meta={"total": int(total), "limit": limit, "offset": offset},
    )


@router.get("/review/v2/{evidence_id}", response_model=ReviewV2DetailEnvelope)
async def get_review_v2(
    evidence_id: str,
    session: Session = Depends(get_session),
) -> ReviewV2DetailEnvelope:
    stored = session.exec(select(EvidencePackage).where(EvidencePackage.evidence_id == evidence_id)).first()
    if stored is None or stored.schema_version != 2:
        raise HTTPException(status_code=404, detail="V2 evidence package not found")
    try:
        _stored, package = load_v2_package(session, evidence_id)
    except ValueError as exc:
        raise HTTPException(status_code=409, detail=str(exc)) from None

    authorization = find_current_v2_authorization(session, package)
    operation = session.exec(
        select(OperationLedger)
        .where(OperationLedger.evidence_id == package.evidence_id)
        .where(OperationLedger.policy_version == "manifestation-v2")
        .order_by(desc(OperationLedger.created_at), desc(OperationLedger.id))
    ).first()
    authorization_status = (
        AuthorizationStatus(
            authorization_id=authorization.authorization_id,
            actor=authorization.actor,
            reason=authorization.reason,
            created_at=authorization.created_at,
        )
        if authorization is not None
        else None
    )
    return ReviewV2DetailEnvelope(
        data=ReviewV2Detail(
            package=package,
            authorization=authorization_status,
            operation=_operation_status(operation),
        )
    )


@router.get("/operations/{operation_id}", response_model=OperationStatusEnvelope)
async def get_operation_status(
    operation_id: str,
    session: Session = Depends(get_session),
) -> OperationStatusEnvelope:
    operation = session.exec(select(OperationLedger).where(OperationLedger.operation_id == operation_id)).first()
    if operation is None:
        raise HTTPException(status_code=404, detail="Operation not found")
    status = _operation_status(operation)
    if status is None:  # pragma: no cover - guarded by the query above
        raise HTTPException(status_code=404, detail="Operation not found")
    return OperationStatusEnvelope(data=status)

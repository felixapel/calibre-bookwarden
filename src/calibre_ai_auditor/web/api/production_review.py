"""Read-only Certificate A projections for sealed Manifestation V2 evidence."""

from __future__ import annotations

from datetime import datetime
from typing import Any, Literal, cast

from fastapi import APIRouter, Depends, HTTPException, Path, Query
from pydantic import BaseModel
from sqlalchemy import func
from sqlmodel import Session, col, desc, select

from calibre_ai_auditor.storage.models import EvidencePackage, VerificationRun
from calibre_ai_auditor.verification.identity_v2 import IdentityTier
from calibre_ai_auditor.verification.pipeline_v2 import BookAuditState, EvidencePackageV2
from calibre_ai_auditor.web.production_dependencies import get_production_session

router = APIRouter(prefix="/review/v2", tags=["Certificate A Review"])


class CertificateAReviewSummary(BaseModel):
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


class CertificateAReviewPage(BaseModel):
    status: Literal["success"] = "success"
    data: list[CertificateAReviewSummary]
    meta: dict[str, int]


class CertificateAReviewDetail(BaseModel):
    package: EvidencePackageV2
    authorization: None = None
    operation: None = None
    writes_enabled: Literal[False] = False


class CertificateAReviewDetailEnvelope(BaseModel):
    status: Literal["success"] = "success"
    data: CertificateAReviewDetail


def _load_sealed_package(stored: EvidencePackage) -> EvidencePackageV2:
    if stored.schema_version != 2 or stored.observations is None:
        raise HTTPException(
            status_code=409,
            detail={"code": "evidence_integrity_failed"},
        )
    try:
        package = EvidencePackageV2.model_validate(stored.observations)
    except ValueError:
        raise HTTPException(
            status_code=409,
            detail={"code": "evidence_integrity_failed"},
        ) from None
    if (
        not package.verify_seal()
        or package.evidence_id != stored.evidence_id
        or package.run_id != stored.run_id
        or package.book_key != stored.book_key
    ):
        raise HTTPException(
            status_code=409,
            detail={"code": "evidence_integrity_failed"},
        )
    return package


def _summary(package: EvidencePackageV2) -> CertificateAReviewSummary:
    return CertificateAReviewSummary(
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


def _base_filters(
    *,
    run_id: str | None,
    state: BookAuditState | None,
    tier: IdentityTier | None,
) -> list[Any]:
    evidence = cast(Any, EvidencePackage).__table__
    run = cast(Any, VerificationRun).__table__
    filters: list[Any] = [
        evidence.c.schema_version == 2,
        run.c.contract_version == "certificate-a-v1",
        run.c.pipeline_version == "manifestation-v2",
    ]
    if run_id is not None:
        filters.append(evidence.c.run_id == run_id)
    if state is not None:
        filters.append(evidence.c.observations["state"].as_string() == state.value)
    if tier is not None:
        filters.append(evidence.c.observations["identity"]["tier"].as_string() == tier.value)
    return filters


@router.get("", response_model=CertificateAReviewPage)
def list_certificate_a_review(
    run_id: str | None = Query(default=None, min_length=1, max_length=128),
    state: BookAuditState | None = None,
    tier: IdentityTier | None = None,
    limit: int = Query(default=50, ge=1, le=100),
    offset: int = Query(default=0, ge=0),
    session: Session = Depends(get_production_session),
) -> CertificateAReviewPage:
    join_condition = col(EvidencePackage.run_id) == col(VerificationRun.run_id)
    filters = _base_filters(run_id=run_id, state=state, tier=tier)
    total = session.exec(
        select(func.count(col(EvidencePackage.id)))
        .select_from(EvidencePackage)
        .join(VerificationRun, join_condition)
        .where(*filters)
    ).one()
    rows = session.exec(
        select(EvidencePackage)
        .join(VerificationRun, join_condition)
        .where(*filters)
        .order_by(desc(EvidencePackage.created_at), desc(EvidencePackage.id))
        .offset(offset)
        .limit(limit)
    ).all()
    return CertificateAReviewPage(
        data=[_summary(_load_sealed_package(row)) for row in rows],
        meta={"total": int(total), "limit": limit, "offset": offset},
    )


@router.get("/{evidence_id}", response_model=CertificateAReviewDetailEnvelope)
def get_certificate_a_review(
    evidence_id: str = Path(min_length=1, max_length=160, pattern=r"^[A-Za-z0-9._:-]+$"),
    session: Session = Depends(get_production_session),
) -> CertificateAReviewDetailEnvelope:
    stored = session.exec(
        select(EvidencePackage)
        .join(VerificationRun, col(EvidencePackage.run_id) == col(VerificationRun.run_id))
        .where(col(EvidencePackage.evidence_id) == evidence_id)
        .where(col(VerificationRun.contract_version) == "certificate-a-v1")
        .where(col(VerificationRun.pipeline_version) == "manifestation-v2")
    ).first()
    if stored is None:
        raise HTTPException(status_code=404, detail={"code": "evidence_not_found"})
    package = _load_sealed_package(stored)
    return CertificateAReviewDetailEnvelope(data=CertificateAReviewDetail(package=package))

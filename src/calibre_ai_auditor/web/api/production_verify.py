"""Certificate A request-only verification API.

The web process persists an immutable request and returns. It never enumerates
the Calibre library and never starts in-process verification work.
"""

from __future__ import annotations

import hashlib
import json
import os
from datetime import UTC, datetime
from pathlib import Path
from typing import Annotated, Any, Literal
from uuid import uuid4

from fastapi import APIRouter, Depends, Header, HTTPException, Query, status
from pydantic import BaseModel, ConfigDict, Field
from sqlalchemy.exc import IntegrityError
from sqlmodel import Session, col, desc, select

from calibre_ai_auditor.config.settings import Settings
from calibre_ai_auditor.storage.db import expected_schema_revision
from calibre_ai_auditor.storage.models import VerificationResult, VerificationRun
from calibre_ai_auditor.web.production_dependencies import (
    get_production_session,
    get_production_settings,
)

CERTIFICATE_A_CONTRACT_VERSION = "certificate-a-v1"
ACTIVE_STATUSES = frozenset({"pending", "inventorying", "running", "cancelling"})
TERMINAL_STATUSES = frozenset(
    {
        "cancelled",
        "completed",
        "completed_with_errors",
        "failed",
        "source_changed",
        "blocked_recovery",
    }
)
RunStatus = Literal[
    "pending",
    "inventorying",
    "running",
    "cancelling",
    "cancelled",
    "completed",
    "completed_with_errors",
    "failed",
    "source_changed",
    "blocked_recovery",
]

router = APIRouter(prefix="/verify", tags=["Certificate A Verification"])


class CertificateAVerifyRequest(BaseModel):
    model_config = ConfigDict(extra="forbid")

    limit: int | None = Field(default=None, gt=0, le=10_000)
    use_ocr: bool = True
    confirm_calibre_stopped: Literal[True]


class CertificateARunSummary(BaseModel):
    run_id: str
    status: RunStatus
    started_at: datetime
    finished_at: datetime | None
    total: int | None
    completed: int
    counts: dict[str, int]
    error_code: str | None


class CertificateAResultSummary(BaseModel):
    book_key: str
    state: str
    evidence_id: str | None


class CertificateARunDetail(CertificateARunSummary):
    results: list[CertificateAResultSummary]


class CertificateARunPage(BaseModel):
    runs: list[CertificateARunSummary]
    limit: int
    offset: int


def _utc_timestamp(value: datetime | None) -> datetime | None:
    if value is None:
        return None
    if value.tzinfo is None:
        return value.replace(tzinfo=UTC)
    return value.astimezone(UTC)


def _summary(run: VerificationRun) -> CertificateARunSummary:
    total = run.total if run.inventory_finished_at is not None else None
    return CertificateARunSummary(
        run_id=run.run_id,
        status=run.status,  # type: ignore[arg-type]
        started_at=_utc_timestamp(run.started_at) or datetime.now(UTC),
        finished_at=_utc_timestamp(run.finished_at),
        total=total,
        completed=run.completed,
        counts=run.counts,
        error_code=run.error_code,
    )


def _configuration_errors(settings: Settings) -> list[str]:
    errors: list[str] = []
    if settings.profile != "production":
        errors.append("production_profile_required")
    if settings.database.backend != "postgres":
        errors.append("postgres_required")
    if settings.queue.backend != "valkey":
        errors.append("valkey_required")
    if settings.release_digest is None:
        errors.append("release_digest_required")
    if settings.library.path is None:
        errors.append("offline_library_root_required")
    if not settings.library.read_only:
        errors.append("read_only_library_required")
    if settings.allow_remote_file_upload:
        errors.append("uploads_must_be_disabled")
    if settings.privacy.allow_remote_text or settings.privacy.allow_remote_images:
        errors.append("remote_content_must_be_disabled")
    if settings.providers.calibre_fetch or not settings.providers.google_books or not settings.providers.openlibrary:
        errors.append("exact_isbn_providers_required")
    if settings.recognition_v2.ocr.backends != ["tesseract"]:
        errors.append("tesseract_only_required")
    if settings.recognition_v2.vision.enabled:
        errors.append("vision_must_be_disabled")
    if settings.manifestation_v2.auto_apply.enabled or settings.manifestation_v2.supervised_pilot.enabled:
        errors.append("certificate_a_must_be_shadow_only")
    if settings.paperless.enabled:
        errors.append("paperless_must_be_disabled")
    if settings.preview.gotenberg_enabled:
        errors.append("preview_must_be_disabled")
    if settings.extractors.tika.enabled:
        errors.append("tika_must_be_disabled")
    if settings.vectors.enabled:
        errors.append("vectors_must_be_disabled")
    if settings.manga_mode.enabled:
        errors.append("manga_must_be_disabled")
    return errors


def _canonical_root(settings: Settings) -> str:
    if settings.library.path is None:  # guarded by _configuration_errors
        raise ValueError("offline library root is unavailable")
    return str(Path(os.path.normpath(os.path.abspath(settings.library.path))))


def _sha256_text(value: str) -> str:
    return hashlib.sha256(value.encode("utf-8")).hexdigest()


def _request_sha256(request: CertificateAVerifyRequest) -> str:
    canonical = json.dumps(
        request.model_dump(mode="json"),
        ensure_ascii=True,
        separators=(",", ":"),
        sort_keys=True,
    )
    return _sha256_text(canonical)


def _effective_config(
    settings: Settings,
    request: CertificateAVerifyRequest,
    *,
    source_root_sha256: str,
) -> dict[str, Any]:
    ocr = settings.recognition_v2.ocr
    return {
        "contract_version": CERTIFICATE_A_CONTRACT_VERSION,
        "pipeline_version": "manifestation-v2",
        "policy_version": "manifestation-v2",
        "schema_revision": expected_schema_revision(),
        "release_digest": settings.release_digest,
        "mode": "shadow",
        "source": {
            "kind": "offline-folder",
            "root_sha256": source_root_sha256,
        },
        "limits": {"book_limit": request.limit},
        "providers": ["google_books_isbn", "openlibrary_isbn"],
        "recognition": {
            "ocr_enabled": request.use_ocr,
            "ocr_backend": "tesseract",
            "ocr_language": ocr.language,
            "ocr_max_pages": ocr.max_pages,
            "vision_enabled": False,
            "llm_enabled": False,
        },
        "privacy": {"remote_text": False, "remote_images": False},
    }


def _existing_for_key(session: Session, idempotency_key: str) -> VerificationRun | None:
    return session.exec(select(VerificationRun).where(VerificationRun.idempotency_key == idempotency_key)).first()


def _active_for_source(session: Session, source_root_sha256: str) -> VerificationRun | None:
    return session.exec(
        select(VerificationRun)
        .where(VerificationRun.contract_version == CERTIFICATE_A_CONTRACT_VERSION)
        .where(VerificationRun.source_root_sha256 == source_root_sha256)
        .where(col(VerificationRun.status).in_(ACTIVE_STATUSES))
        .order_by(col(VerificationRun.started_at))
    ).first()


def _idempotent_or_conflict(
    existing: VerificationRun,
    *,
    request_sha256: str,
) -> CertificateARunSummary:
    if existing.request_sha256 != request_sha256:
        raise HTTPException(
            status_code=409,
            detail={"code": "idempotency_key_reused"},
        )
    return _summary(existing)


@router.post("", response_model=CertificateARunSummary, status_code=status.HTTP_202_ACCEPTED)
def start_verification(
    request: CertificateAVerifyRequest,
    idempotency_key: Annotated[
        str,
        Header(
            alias="Idempotency-Key",
            min_length=16,
            max_length=128,
            pattern=r"^[A-Za-z0-9._:-]+$",
        ),
    ],
    settings: Annotated[Settings, Depends(get_production_settings)],
    session: Annotated[Session, Depends(get_production_session)],
) -> CertificateARunSummary:
    errors = _configuration_errors(settings)
    if errors:
        raise HTTPException(
            status_code=503,
            detail={"code": "certificate_a_configuration_invalid", "checks": errors},
        )

    request_hash = _request_sha256(request)
    existing = _existing_for_key(session, idempotency_key)
    if existing is not None:
        return _idempotent_or_conflict(existing, request_sha256=request_hash)

    source_root = _canonical_root(settings)
    source_hash = _sha256_text(source_root)
    active = _active_for_source(session, source_hash)
    if active is not None:
        raise HTTPException(
            status_code=409,
            detail={"code": "active_run_exists", "active_run_id": active.run_id},
        )

    run = VerificationRun(
        run_id=f"verify_{uuid4().hex}",
        status="pending",
        total=0,
        completed=0,
        counts={},
        use_llm=False,
        pipeline_version="manifestation-v2",
        mode="shadow",
        contract_version=CERTIFICATE_A_CONTRACT_VERSION,
        idempotency_key=idempotency_key,
        request_sha256=request_hash,
        source_root=source_root,
        source_root_sha256=source_hash,
        effective_config=_effective_config(
            settings,
            request,
            source_root_sha256=source_hash,
        ),
        fence_token=0,
    )
    session.add(run)
    try:
        session.commit()
        session.refresh(run)
    except IntegrityError:
        session.rollback()
        raced = _existing_for_key(session, idempotency_key)
        if raced is not None:
            return _idempotent_or_conflict(raced, request_sha256=request_hash)
        active = _active_for_source(session, source_hash)
        if active is not None:
            raise HTTPException(
                status_code=409,
                detail={"code": "active_run_exists", "active_run_id": active.run_id},
            ) from None
        raise HTTPException(
            status_code=409,
            detail={"code": "verification_request_conflict"},
        ) from None
    return _summary(run)


@router.get("/runs", response_model=CertificateARunPage)
def list_runs(
    limit: int = Query(default=50, ge=1, le=100),
    offset: int = Query(default=0, ge=0),
    session: Session = Depends(get_production_session),
) -> CertificateARunPage:
    runs = session.exec(
        select(VerificationRun)
        .where(VerificationRun.contract_version == CERTIFICATE_A_CONTRACT_VERSION)
        .where(VerificationRun.pipeline_version == "manifestation-v2")
        .order_by(desc(VerificationRun.started_at), desc(VerificationRun.id))
        .offset(offset)
        .limit(limit)
    ).all()
    return CertificateARunPage(runs=[_summary(run) for run in runs], limit=limit, offset=offset)


def _certificate_a_run(session: Session, run_id: str) -> VerificationRun:
    run = session.exec(
        select(VerificationRun)
        .where(VerificationRun.run_id == run_id)
        .where(VerificationRun.contract_version == CERTIFICATE_A_CONTRACT_VERSION)
        .where(VerificationRun.pipeline_version == "manifestation-v2")
    ).first()
    if run is None:
        raise HTTPException(status_code=404, detail={"code": "run_not_found"})
    return run


@router.get("/{run_id}", response_model=CertificateARunDetail)
def get_run(
    run_id: str,
    session: Session = Depends(get_production_session),
) -> CertificateARunDetail:
    run = _certificate_a_run(session, run_id)
    rows = session.exec(
        select(VerificationResult).where(VerificationResult.run_id == run_id).order_by(col(VerificationResult.id))
    ).all()
    summary = _summary(run)
    return CertificateARunDetail(
        **summary.model_dump(),
        results=[
            CertificateAResultSummary(
                book_key=row.book_key,
                state=row.state,
                evidence_id=row.evidence_id,
            )
            for row in rows
        ],
    )


@router.post("/{run_id}/cancel", response_model=CertificateARunSummary, status_code=status.HTTP_202_ACCEPTED)
def cancel_run(
    run_id: str,
    session: Session = Depends(get_production_session),
) -> CertificateARunSummary:
    run = _certificate_a_run(session, run_id)
    if run.status in TERMINAL_STATUSES or run.finished_at is not None:
        raise HTTPException(
            status_code=409,
            detail={"code": "run_already_terminal", "status": run.status},
        )
    if run.status != "cancelling":
        run.status = "cancelling"
        run.cancel_requested_at = datetime.now(UTC).replace(tzinfo=None)
        session.add(run)
        session.commit()
        session.refresh(run)
    return _summary(run)

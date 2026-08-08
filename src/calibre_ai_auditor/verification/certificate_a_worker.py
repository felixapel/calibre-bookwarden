"""Fenced PostgreSQL queue and worker primitives for Certificate A.

The web process only inserts requests.  A separate verifier process claims
them atomically, increments a monotonic fence, and predicates every durable
write on the owner/fence pair and a live lease.
"""

from __future__ import annotations

import asyncio
import hashlib
import logging
import os
import threading
from dataclasses import dataclass
from datetime import UTC, datetime, timedelta
from pathlib import Path
from typing import Any, Literal
from uuid import uuid4

from pydantic import BaseModel, ConfigDict, Field, ValidationError
from sqlalchemy import and_, case, or_, update
from sqlalchemy.engine import Engine
from sqlmodel import Session, col, select

from calibre_ai_auditor.calibre.offline import (
    OfflineCalibreSource,
    OfflineLibraryChangedError,
)
from calibre_ai_auditor.config.settings import Settings
from calibre_ai_auditor.storage.db import expected_schema_revision
from calibre_ai_auditor.storage.models import (
    BookRecord,
    EvidencePackage,
    VerificationResult,
    VerificationRun,
)
from calibre_ai_auditor.verification.pipeline_v2 import (
    TERMINAL_BOOK_STATES,
    BookAuditState,
    EvidencePackageV2,
    LibraryAuditPipeline,
)

logger = logging.getLogger(__name__)

CERTIFICATE_A_CONTRACT_VERSION = "certificate-a-v1"
CLAIMABLE_STATUSES = frozenset({"pending", "inventorying", "running", "cancelling"})
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
_RUN_TABLE: Any = VerificationRun.__table__  # type: ignore[attr-defined]


class LostVerifierLeaseError(RuntimeError):
    """A stale verifier attempted to write after losing its fence."""


class SourceSnapshotMismatchError(RuntimeError):
    """Recovery observed a different source or selected membership."""


class CertificateAContractError(RuntimeError):
    """A persisted run cannot be executed under the current release boundary."""


class VerifierCancellationRequestedError(RuntimeError):
    """The operator requested cancellation at a safe worker boundary."""


class _SourceContract(BaseModel):
    model_config = ConfigDict(extra="forbid")

    kind: Literal["offline-folder"]
    root_sha256: str = Field(pattern=r"^[0-9a-f]{64}$")


class _LimitsContract(BaseModel):
    model_config = ConfigDict(extra="forbid")

    book_limit: int | None = Field(default=None, gt=0, le=10_000)


class _RecognitionContract(BaseModel):
    model_config = ConfigDict(extra="forbid")

    ocr_enabled: bool
    ocr_backend: Literal["tesseract"]
    ocr_language: str = Field(pattern=r"^[a-z]{2,3}$")
    ocr_max_pages: int = Field(ge=1, le=12)
    vision_enabled: Literal[False]
    llm_enabled: Literal[False]


class _PrivacyContract(BaseModel):
    model_config = ConfigDict(extra="forbid")

    remote_text: Literal[False]
    remote_images: Literal[False]


class _EffectiveContract(BaseModel):
    model_config = ConfigDict(extra="forbid")

    contract_version: Literal["certificate-a-v1"]
    pipeline_version: Literal["manifestation-v2"]
    policy_version: Literal["manifestation-v2"]
    schema_revision: str
    release_digest: str = Field(pattern=r"^sha256:[0-9a-f]{64}$")
    mode: Literal["shadow"]
    source: _SourceContract
    limits: _LimitsContract
    providers: tuple[Literal["google_books_isbn", "openlibrary_isbn"], ...]
    recognition: _RecognitionContract
    privacy: _PrivacyContract


@dataclass(frozen=True)
class CertificateAClaim:
    run_id: str
    owner: str
    fence_token: int


@dataclass(frozen=True)
class CertificateAContract:
    run_id: str
    source_root: Path
    source_root_sha256: str
    release_digest: str
    book_limit: int | None
    use_ocr: bool
    ocr_backend: str
    ocr_language: str
    ocr_max_pages: int
    providers: tuple[str, ...]


def _utc_naive(moment: datetime | None = None) -> datetime:
    value = moment or datetime.now(UTC)
    if value.tzinfo is not None:
        return value.astimezone(UTC).replace(tzinfo=None)
    return value


def _absolute_lexical(path: Path) -> Path:
    return Path(os.path.normpath(os.path.abspath(os.fspath(path))))


def _claim_predicate(moment: datetime) -> Any:
    run = _RUN_TABLE.c
    return and_(
        run.contract_version == CERTIFICATE_A_CONTRACT_VERSION,
        run.pipeline_version == "manifestation-v2",
        run.mode == "shadow",
        run.finished_at.is_(None),
        run.status.in_(CLAIMABLE_STATUSES),
        or_(
            run.lease_owner.is_(None),
            run.lease_expires_at.is_(None),
            run.lease_expires_at <= moment,
        ),
    )


def claim_next_certificate_a_run(
    engine: Engine,
    *,
    owner: str,
    now: datetime | None = None,
    ttl_seconds: int = 120,
    run_id: str | None = None,
) -> CertificateAClaim | None:
    """Atomically claim one pending/recoverable run and increment its fence."""
    if not owner or len(owner) > 128:
        raise ValueError("verifier owner must contain between 1 and 128 characters")
    moment = _utc_naive(now)
    expires = moment + timedelta(seconds=max(3, ttl_seconds))
    predicate = _claim_predicate(moment)
    run = _RUN_TABLE.c
    candidate = (
        select(VerificationRun.id)
        .where(predicate)
        .order_by(col(VerificationRun.started_at), col(VerificationRun.id))
        .limit(1)
    )
    if run_id is not None:
        candidate = candidate.where(VerificationRun.run_id == run_id)
    if engine.dialect.name == "postgresql":
        candidate = candidate.with_for_update(skip_locked=True)

    statement = (
        update(VerificationRun)
        .where(run.id == candidate.scalar_subquery())
        .where(_claim_predicate(moment))
        .values(
            lease_owner=owner,
            lease_expires_at=expires,
            claimed_at=moment,
            heartbeat_at=moment,
            fence_token=run.fence_token + 1,
            status=case((run.status == "pending", "inventorying"), else_=run.status),
        )
        .returning(run.run_id, run.fence_token)
    )
    with engine.begin() as connection:
        row = connection.execute(statement).first()
    if row is None:
        return None
    return CertificateAClaim(run_id=str(row[0]), owner=owner, fence_token=int(row[1]))


def heartbeat_certificate_a_run(
    engine: Engine,
    claim: CertificateAClaim,
    *,
    now: datetime | None = None,
    ttl_seconds: int = 120,
) -> bool:
    """Extend only a still-live matching owner/fence lease."""
    moment = _utc_naive(now)
    expires = moment + timedelta(seconds=max(3, ttl_seconds))
    run = _RUN_TABLE.c
    statement = (
        update(VerificationRun)
        .where(run.run_id == claim.run_id)
        .where(run.contract_version == CERTIFICATE_A_CONTRACT_VERSION)
        .where(run.lease_owner == claim.owner)
        .where(run.fence_token == claim.fence_token)
        .where(run.finished_at.is_(None))
        .where(run.status.in_(CLAIMABLE_STATUSES))
        .where(run.lease_expires_at > moment)
        .values(lease_expires_at=expires, heartbeat_at=moment)
    )
    with engine.begin() as connection:
        return connection.execute(statement).rowcount == 1


def _load_owned_run(session: Session, claim: CertificateAClaim, moment: datetime) -> VerificationRun:
    run = session.exec(select(VerificationRun).where(VerificationRun.run_id == claim.run_id).with_for_update()).first()
    if (
        run is None
        or run.contract_version != CERTIFICATE_A_CONTRACT_VERSION
        or run.lease_owner != claim.owner
        or run.fence_token != claim.fence_token
        or run.finished_at is not None
        or run.status not in CLAIMABLE_STATUSES
        or run.lease_expires_at is None
        or _utc_naive(run.lease_expires_at) <= moment
    ):
        raise LostVerifierLeaseError("verifier lease or fence is no longer current")
    return run


def load_certificate_a_contract(
    engine: Engine,
    claim: CertificateAClaim,
    settings: Settings,
    *,
    now: datetime | None = None,
) -> CertificateAContract:
    """Validate and reconstruct execution solely from the persisted contract."""
    moment = _utc_naive(now)
    with Session(engine) as session:
        run = _load_owned_run(session, claim, moment)
        source_root = run.source_root
        source_root_sha256 = run.source_root_sha256
        raw_effective = run.effective_config

    try:
        effective = _EffectiveContract.model_validate(raw_effective)
    except ValidationError as exc:
        raise CertificateAContractError("persisted Certificate A options are invalid") from exc
    if effective.providers != ("google_books_isbn", "openlibrary_isbn"):
        raise CertificateAContractError("persisted provider contract is not the exact Certificate A allowlist")
    if effective.schema_revision != expected_schema_revision():
        raise CertificateAContractError("persisted schema revision does not match this release")
    if settings.release_digest != effective.release_digest:
        raise CertificateAContractError("persisted release digest does not match the verifier release")
    if source_root is None or source_root_sha256 is None:
        raise CertificateAContractError("persisted source root is missing")
    canonical_root = _absolute_lexical(Path(source_root))
    if str(canonical_root) != source_root:
        raise CertificateAContractError("persisted source root is not canonical")
    actual_root_hash = hashlib.sha256(source_root.encode()).hexdigest()
    if source_root_sha256 != actual_root_hash or effective.source.root_sha256 != actual_root_hash:
        raise CertificateAContractError("persisted source root identity is inconsistent")
    configured_root = settings.library.path
    if configured_root is None or _absolute_lexical(configured_root) != canonical_root:
        raise CertificateAContractError("configured source root differs from the persisted source root")
    if settings.profile != "production" or not settings.library.read_only:
        raise CertificateAContractError("verifier requires the read-only production profile")

    return CertificateAContract(
        run_id=claim.run_id,
        source_root=canonical_root,
        source_root_sha256=actual_root_hash,
        release_digest=effective.release_digest,
        book_limit=effective.limits.book_limit,
        use_ocr=effective.recognition.ocr_enabled,
        ocr_backend=effective.recognition.ocr_backend,
        ocr_language=effective.recognition.ocr_language,
        ocr_max_pages=effective.recognition.ocr_max_pages,
        providers=tuple(effective.providers),
    )


class FencedCertificateAStore:
    """Transactional result store whose every mutation verifies the live fence."""

    def __init__(
        self,
        engine: Engine,
        claim: CertificateAClaim,
        *,
        clock: Any = _utc_naive,
    ) -> None:
        self.engine = engine
        self.claim = claim
        self._clock = clock

    def _moment(self) -> datetime:
        return _utc_naive(self._clock())

    def _run(self, session: Session, *, allow_cancelling: bool = True) -> VerificationRun:
        run = _load_owned_run(session, self.claim, self._moment())
        if not allow_cancelling and (run.status == "cancelling" or run.cancel_requested_at is not None):
            raise VerifierCancellationRequestedError("verification cancellation was requested")
        return run

    @staticmethod
    def _result(session: Session, run_id: str, book_key: str) -> VerificationResult:
        result = session.exec(
            select(VerificationResult)
            .where(VerificationResult.run_id == run_id)
            .where(VerificationResult.book_key == book_key)
        ).first()
        if result is None:
            raise ValueError("book is outside the frozen Certificate A membership")
        return result

    def inventory(self, *, book_keys: list[str], source_snapshot: dict[str, Any]) -> None:
        if len(book_keys) != len(set(book_keys)) or any(not key.strip() for key in book_keys):
            raise ValueError("Certificate A inventory must contain unique non-empty book keys")
        with Session(self.engine) as session:
            run = self._run(session, allow_cancelling=False)
            rows = session.exec(select(VerificationResult).where(VerificationResult.run_id == self.claim.run_id)).all()
            existing_keys = {row.book_key for row in rows}
            if run.source_snapshot is not None and run.source_snapshot != source_snapshot:
                raise SourceSnapshotMismatchError("offline source snapshot changed during recovery")
            if existing_keys and existing_keys != set(book_keys):
                raise SourceSnapshotMismatchError("selected library membership changed during recovery")
            if not existing_keys:
                for book_key in book_keys:
                    session.add(
                        VerificationResult(
                            result_id=str(uuid4()),
                            run_id=self.claim.run_id,
                            book_key=book_key,
                            state=BookAuditState.pending.value,
                        )
                    )
            run.source_snapshot = source_snapshot
            run.total = len(book_keys)
            run.inventory_finished_at = run.inventory_finished_at or self._moment()
            run.status = "running"
            session.add(run)
            session.commit()

    def record_state(self, book_key: str, state: BookAuditState) -> None:
        with Session(self.engine) as session:
            self._run(session, allow_cancelling=False)
            result = self._result(session, self.claim.run_id, book_key)
            result.state = state.value
            session.add(result)
            session.commit()

    def record_package(self, package: EvidencePackageV2) -> None:
        if package.run_id != self.claim.run_id or not package.verify_seal():
            raise ValueError("evidence package identity or seal is invalid")
        payload = package.model_dump(mode="json")
        with Session(self.engine) as session:
            self._run(session)
            result = self._result(session, self.claim.run_id, package.book_key)
            book = session.exec(select(BookRecord).where(BookRecord.book_key == package.book_key)).first()
            source = package.snapshot.source.kind if package.snapshot.source is not None else "calibre"
            format_by_reference = {item.path: item.format.upper() for item in package.formats}
            files: list[dict[str, str]] = []
            for path in package.snapshot.files:
                format_name = format_by_reference.get(path)
                if format_name is None and package.snapshot.source is not None:
                    format_name = path.rsplit(":", 1)[-1]
                if format_name is None:
                    format_name = path.rsplit(".", 1)[-1].upper() if "." in path else "UNKNOWN"
                files.append({"path": path, "format": format_name})
            if book is None:
                book = BookRecord(
                    book_key=package.book_key,
                    run_id=package.run_id,
                    calibre_book_id=package.snapshot.calibre_book_id,
                    source=source,
                )
            book.run_id = package.run_id
            book.calibre_book_id = package.snapshot.calibre_book_id
            book.source = source
            book.files = files
            book.current_metadata = package.snapshot.current_metadata
            book.status = package.state.value
            session.add(book)

            evidence = session.exec(
                select(EvidencePackage).where(EvidencePackage.evidence_id == package.evidence_id)
            ).first()
            if evidence is None:
                session.add(
                    EvidencePackage(
                        evidence_id=package.evidence_id,
                        book_key=package.book_key,
                        run_id=package.run_id,
                        schema_version=package.schema_version,
                        current=package.snapshot.current_metadata,
                        extracted=package.identity.model_dump(mode="json"),
                        risk_flags=package.identity.risk_flags,
                        decision=None,
                        observations=payload,
                    )
                )
            elif evidence.observations != payload:
                raise ValueError("evidence id already exists with different sealed content")
            result.evidence_id = package.evidence_id
            result.state = package.state.value
            result.verdict = {
                "schema_version": package.schema_version,
                "evidence_id": package.evidence_id,
                "book_key": package.book_key,
                "state": package.state.value,
                "identity": package.identity.model_dump(mode="json"),
                "warnings": package.warnings,
            }
            session.add(result)
            self._refresh_counts(session)
            session.commit()

    def resumable_book_keys(self) -> set[str]:
        terminal = {
            state.value
            for state in TERMINAL_BOOK_STATES
            if state not in {BookAuditState.failed, BookAuditState.source_changed}
        }
        with Session(self.engine) as session:
            self._run(session, allow_cancelling=False)
            rows = session.exec(select(VerificationResult).where(VerificationResult.run_id == self.claim.run_id)).all()
            complete: set[str] = set()
            for row in rows:
                if not row.evidence_id or row.state not in terminal:
                    continue
                evidence = session.exec(
                    select(EvidencePackage).where(EvidencePackage.evidence_id == row.evidence_id)
                ).first()
                if evidence is None or evidence.schema_version != 2 or not evidence.observations:
                    continue
                try:
                    package = EvidencePackageV2.model_validate(evidence.observations)
                except ValueError:
                    continue
                if (
                    package.verify_seal()
                    and package.evidence_id == row.evidence_id
                    and package.run_id == self.claim.run_id
                    and package.book_key == row.book_key
                    and evidence.book_key == package.book_key
                    and evidence.run_id == package.run_id
                ):
                    complete.add(row.book_key)
            return complete

    def cancellation_requested(self) -> bool:
        with Session(self.engine) as session:
            run = self._run(session)
            return run.status == "cancelling" or run.cancel_requested_at is not None

    def finish(self, status: str, *, error_code: str | None = None) -> None:
        if status not in TERMINAL_STATUSES:
            raise ValueError("invalid Certificate A terminal status")
        with Session(self.engine) as session:
            run = self._run(session)
            self._refresh_counts(session, run=run)
            run.status = status
            run.error_code = error_code
            run.finished_at = self._moment()
            run.lease_owner = None
            run.lease_expires_at = None
            session.add(run)
            session.commit()

    def _refresh_counts(self, session: Session, *, run: VerificationRun | None = None) -> None:
        durable_run = run or self._run(session)
        rows = session.exec(select(VerificationResult).where(VerificationResult.run_id == self.claim.run_id)).all()
        terminal = {state.value for state in TERMINAL_BOOK_STATES}
        counts: dict[str, int] = {}
        completed = 0
        for row in rows:
            if row.state in terminal and row.evidence_id:
                completed += 1
                counts[row.state] = counts.get(row.state, 0) + 1
        durable_run.completed = completed
        durable_run.counts = counts
        session.add(durable_run)


class _HeartbeatGuard:
    """Keep a lease fresh from a real thread while synchronous extractors run."""

    def __init__(self, engine: Engine, claim: CertificateAClaim, *, ttl_seconds: int) -> None:
        self.engine = engine
        self.claim = claim
        self.ttl_seconds = max(30, ttl_seconds)
        self._stop = threading.Event()
        self._lost = threading.Event()
        self._thread = threading.Thread(
            target=self._run,
            name=f"certificate-a-heartbeat-{claim.run_id[-12:]}",
            daemon=False,
        )

    def __enter__(self) -> _HeartbeatGuard:
        self._thread.start()
        return self

    def __exit__(self, _exc_type: object, _exc: object, _traceback: object) -> None:
        self._stop.set()
        self._thread.join(timeout=min(10.0, self.ttl_seconds / 3))
        if self._thread.is_alive():
            raise RuntimeError("verifier heartbeat thread did not stop")

    def _run(self) -> None:
        interval = max(1.0, self.ttl_seconds / 3)
        while not self._stop.wait(interval):
            try:
                fresh = heartbeat_certificate_a_run(
                    self.engine,
                    self.claim,
                    ttl_seconds=self.ttl_seconds,
                )
            except Exception:
                logger.exception("Certificate A heartbeat failed for run %s", self.claim.run_id)
                fresh = False
            if not fresh:
                self._lost.set()
                return

    def ensure_current(self) -> None:
        if self._lost.is_set():
            raise LostVerifierLeaseError("verifier heartbeat lost the active fence")


async def execute_certificate_a_claim(
    *,
    database_engine: Engine,
    claim: CertificateAClaim,
    settings: Settings,
    evidence_enricher: Any | None = None,
    source_factory: Any = OfflineCalibreSource,
) -> str:
    """Execute exactly one claimed request from its immutable persisted options."""
    store = FencedCertificateAStore(database_engine, claim)
    ttl_seconds = settings.verifier.lease_ttl_seconds
    with _HeartbeatGuard(database_engine, claim, ttl_seconds=ttl_seconds) as heartbeat:
        try:
            contract = load_certificate_a_contract(database_engine, claim, settings)
            if store.cancellation_requested():
                store.finish("cancelled", error_code="cancelled_before_inventory")
                return "cancelled"

            scratch = settings.verifier.scratch_dir
            with source_factory(
                contract.source_root,
                snapshot_root=scratch,
                confirm_calibre_stopped=True,
            ) as source:
                books = source.list_books()
                if contract.book_limit is not None:
                    books = books[: contract.book_limit]
                book_keys = [f"calibre-offline:{source.fingerprint}:{int(book['id'])}" for book in books]
                source_snapshot = source.snapshot_manifest
                source_snapshot["selected_book_keys"] = book_keys
                store.inventory(book_keys=book_keys, source_snapshot=source_snapshot)
                heartbeat.ensure_current()
                if store.cancellation_requested():
                    store.finish("cancelled", error_code="cancelled_after_inventory")
                    return "cancelled"

                if evidence_enricher is None:
                    from calibre_ai_auditor.verification.service_v2 import build_v2_enricher

                    runtime_settings = settings.model_copy(deep=True)
                    runtime_settings.providers = runtime_settings.providers.model_copy(
                        update={"calibre_fetch": False, "google_books": True, "openlibrary": True}
                    )
                    runtime_settings.recognition_v2 = runtime_settings.recognition_v2.model_copy(
                        update={
                            "ocr": runtime_settings.recognition_v2.ocr.model_copy(
                                update={
                                    "enabled": contract.use_ocr,
                                    "backends": ["tesseract"],
                                    "max_pages": contract.ocr_max_pages,
                                    "language": contract.ocr_language,
                                }
                            ),
                            "vision": runtime_settings.recognition_v2.vision.model_copy(update={"enabled": False}),
                        }
                    )
                    evidence_enricher = build_v2_enricher(
                        runtime_settings,
                        use_llm=False,
                        use_ocr=contract.use_ocr,
                        use_vision=False,
                        run_allows_remote_text=False,
                        run_allows_remote_images=False,
                        use_public_providers=True,
                    )

                def record_package(package: EvidencePackageV2) -> None:
                    store.record_package(package)
                    if package.state is BookAuditState.source_changed:
                        raise OfflineLibraryChangedError("offline source changed during verification")

                pipeline = LibraryAuditPipeline(
                    cli=source,
                    evidence_enricher=evidence_enricher,
                    state_callback=store.record_state,
                    package_callback=record_package,
                    scratch_root=scratch,
                )
                result = await pipeline.run(
                    run_id=claim.run_id,
                    limit=contract.book_limit or 0,
                    skip_book_keys=store.resumable_book_keys(),
                )
                source.assert_unchanged()
                heartbeat.ensure_current()
                if store.cancellation_requested():
                    store.finish("cancelled", error_code="cancelled_after_processing")
                    return "cancelled"
                status = result.status.value
                store.finish(status)
                return status
        except VerifierCancellationRequestedError:
            store.finish("cancelled", error_code="cancelled_by_operator")
            return "cancelled"
        except (OfflineLibraryChangedError, SourceSnapshotMismatchError):
            store.finish("source_changed", error_code="offline_source_changed")
            return "source_changed"
        except CertificateAContractError:
            store.finish("blocked_recovery", error_code="certificate_a_contract_mismatch")
            return "blocked_recovery"
        except LostVerifierLeaseError:
            raise
        except Exception:
            logger.exception("Certificate A verifier failed for run %s", claim.run_id)
            store.finish("failed", error_code="verifier_failed")
            return "failed"


async def run_certificate_a_worker(settings: Settings, *, once: bool = False) -> bool:
    """Poll PostgreSQL, executing one run at a time; return whether work ran."""
    from calibre_ai_auditor.storage.db import get_engine

    if settings.profile != "production" or settings.database.backend != "postgres":
        raise CertificateAContractError("Certificate A verifier requires production PostgreSQL")
    engine = get_engine(settings)
    owner = f"verifier-{uuid4().hex[:16]}"
    processed = False
    while True:
        claim = claim_next_certificate_a_run(
            engine,
            owner=owner,
            ttl_seconds=settings.verifier.lease_ttl_seconds,
        )
        if claim is None:
            if once:
                return processed
            await asyncio.sleep(settings.verifier.poll_interval_seconds)
            continue
        processed = True
        try:
            await execute_certificate_a_claim(
                database_engine=engine,
                claim=claim,
                settings=settings,
            )
        except LostVerifierLeaseError:
            logger.error("Certificate A verifier lost fence for run %s", claim.run_id)
        if once:
            return processed

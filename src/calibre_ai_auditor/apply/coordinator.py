"""Read-only API coordinator for durable metadata write requests."""

import hashlib
import json
import logging
import os
from dataclasses import dataclass
from pathlib import Path
from uuid import uuid4

from sqlalchemy import func, or_, text
from sqlmodel import Session, col, desc, select

from calibre_ai_auditor.apply.heartbeat import library_root_sha256
from calibre_ai_auditor.storage.db import expected_schema_revision
from calibre_ai_auditor.storage.models import (
    BookRecord,
    BookWriteLock,
    Change,
    EvidencePackage,
    ManualAuthorization,
    OperationIncidentAcknowledgement,
    OperationLedger,
    OutboxEvent,
    PilotSession,
    utc_now,
)
from calibre_ai_auditor.storage.operations import create_operation
from calibre_ai_auditor.verification.identity_v2 import CanonicalPatch, IdentityTier
from calibre_ai_auditor.verification.pipeline_v2 import EvidencePackageV2
from calibre_ai_auditor.verification.restore import ConservativeAutoApply
from calibre_ai_auditor.verification.verdict import BookVerdict

logger = logging.getLogger(__name__)

_NONTERMINAL_OPERATION_STATES = frozenset({"requested", "claimed", "writing", "verifying", "restoring"})
_V2_QUEUE_ADVISORY_LOCK_ID = 0x43414C495632


def _hash_json(value: object) -> str:
    encoded = json.dumps(value, sort_keys=True, separators=(",", ":"), ensure_ascii=True).encode()
    return hashlib.sha256(encoded).hexdigest()


def _hash_verdict(verdict: BookVerdict) -> str:
    return _hash_json(verdict.model_dump(mode="json", exclude={"created_at"}))


def _canonicalize_legacy_patch(raw_patch: dict[str, object]) -> dict[str, object]:
    patch = dict(raw_patch)
    if "published_date" in patch:
        if "pubdate" in patch:
            raise ValueError("legacy and canonical publication date fields conflict")
        patch["pubdate"] = patch.pop("published_date")
    if "language" in patch:
        if "languages" in patch:
            raise ValueError("legacy and canonical language fields conflict")
        language = patch.pop("language")
        patch["languages"] = language if isinstance(language, list) else [language]
    return CanonicalPatch.model_validate(patch).model_dump(mode="json", exclude_none=True)


def load_v2_package(session: Session, evidence_id: str) -> tuple[EvidencePackage, EvidencePackageV2]:
    stored = session.exec(select(EvidencePackage).where(EvidencePackage.evidence_id == evidence_id)).first()
    if stored is None or stored.schema_version != 2 or not stored.observations:
        raise ValueError("sealed V2 evidence package is unavailable")
    package = EvidencePackageV2.model_validate(stored.observations)
    if (
        not package.verify_seal()
        or package.evidence_id != stored.evidence_id
        or package.book_key != stored.book_key
        or package.run_id != stored.run_id
        or stored.current != package.snapshot.current_metadata
        or stored.extracted != package.identity.model_dump(mode="json")
    ):
        raise ValueError("V2 evidence package identity or seal is invalid")
    return stored, package


def _book_file_paths(book: BookRecord) -> list[str]:
    paths: list[str] = []
    for item in book.files or []:
        if isinstance(item, dict) and isinstance(item.get("path"), str):
            paths.append(item["path"])
    return paths


def _validate_v2_book_snapshot(book: BookRecord, package: EvidencePackageV2) -> None:
    if (
        package.error is not None
        or package.state.value != "shadowed"
        or book.book_key != package.book_key
        or book.run_id != package.run_id
        or book.calibre_book_id != package.snapshot.calibre_book_id
        or book.current_metadata != package.snapshot.current_metadata
        or _book_file_paths(book) != package.snapshot.files
    ):
        raise ValueError("persisted book no longer matches the exact sealed V2 snapshot")


def _v2_patch(package: EvidencePackageV2, book: BookRecord) -> dict[str, object]:
    patch = CanonicalPatch.model_validate(package.identity.auto_patch).model_dump(mode="json", exclude_none=True)
    locks = set(book.field_locks or {})
    return {field: value for field, value in patch.items() if field not in locks}


def _current_value_for_patch(book: BookRecord, field: str) -> object:
    """Map a canonical V2 field to its value in the Calibre snapshot."""
    if field == "edition_statement":
        return book.current_metadata.get("#edition", book.current_metadata.get("edition_statement"))
    return book.current_metadata.get(field)


def create_v2_manual_authorization(
    session: Session,
    *,
    book: BookRecord,
    package: EvidencePackageV2,
    actor: str,
    reason: str,
) -> ManualAuthorization:
    if not actor.strip() or not reason.strip() or not package.verify_seal() or package.package_sha256 is None:
        raise ValueError("actor, reason, and a valid sealed package are required")
    if package.book_key != book.book_key or package.run_id != book.run_id:
        raise ValueError("evidence package does not belong to the current book snapshot")
    _validate_v2_book_snapshot(book, package)
    patch = _v2_patch(package, book)
    if package.identity.tier is not IdentityTier.tier_a or not patch:
        raise ValueError("only a Tier A package with a non-empty canonical patch can be authorized")
    authorization = ManualAuthorization(
        authorization_id=str(uuid4()),
        book_key=book.book_key,
        run_id=book.run_id,
        verdict_hash=package.package_sha256,
        patch_hash=_hash_json(patch),
        actor=actor.strip(),
        reason=reason.strip(),
    )
    session.add(authorization)
    session.flush()
    return authorization


def _v2_authorization_matches(
    authorization: ManualAuthorization | None,
    *,
    book: BookRecord,
    package: EvidencePackageV2,
    patch: dict[str, object],
) -> bool:
    return bool(
        authorization
        and package.package_sha256
        and authorization.book_key == book.book_key
        and authorization.run_id == book.run_id
        and authorization.verdict_hash == package.package_sha256
        and authorization.patch_hash == _hash_json(patch)
    )


def find_current_v2_authorization(
    session: Session,
    package: EvidencePackageV2,
) -> ManualAuthorization | None:
    """Return the newest authorization that still matches the live review state."""
    book = session.exec(select(BookRecord).where(BookRecord.book_key == package.book_key)).first()
    if book is None:
        return None
    try:
        _validate_v2_book_snapshot(book, package)
        patch = _v2_patch(package, book)
    except ValueError:
        return None
    authorization = session.exec(
        select(ManualAuthorization)
        .where(ManualAuthorization.book_key == package.book_key)
        .where(ManualAuthorization.run_id == package.run_id)
        .where(ManualAuthorization.verdict_hash == package.package_sha256)
        .where(ManualAuthorization.patch_hash == _hash_json(patch))
        .order_by(desc(ManualAuthorization.created_at), desc(ManualAuthorization.id))
    ).first()
    return (
        authorization
        if _v2_authorization_matches(
            authorization,
            book=book,
            package=package,
            patch=patch,
        )
        else None
    )


def create_manual_authorization(
    session: Session,
    *,
    book: BookRecord,
    verdict: BookVerdict,
    actor: str,
    reason: str,
) -> ManualAuthorization:
    """Persist an immutable approval for the exact current verdict and patch."""
    if not actor.strip() or not reason.strip():
        raise ValueError("actor and reason are required")
    authorization = ManualAuthorization(
        authorization_id=str(uuid4()),
        book_key=book.book_key,
        run_id=book.run_id,
        verdict_hash=_hash_verdict(verdict),
        patch_hash=_hash_json(verdict.proposed_patch),
        actor=actor.strip(),
        reason=reason.strip(),
    )
    session.add(authorization)
    session.flush()
    return authorization


def _authorization_matches(
    authorization: ManualAuthorization | None,
    *,
    book: BookRecord,
    verdict: BookVerdict,
) -> bool:
    return bool(
        authorization
        and authorization.book_key == book.book_key
        and authorization.run_id == book.run_id
        and authorization.verdict_hash == _hash_verdict(verdict)
        and authorization.patch_hash == _hash_json(verdict.proposed_patch)
    )


@dataclass(frozen=True)
class CoordinationResult:
    queued_operation_ids: list[str]
    skipped_book_keys: list[str]


@dataclass(frozen=True)
class PilotGuard:
    """Runtime facts required before reserving one supervised V2 write."""

    enabled: bool
    pilot_id: str
    library_root: str
    release_digest: str
    alembic_revision: str
    max_operations: int
    writer_ready: bool


def _canonical_library_root(raw_root: str) -> str:
    return str(Path(os.path.normpath(os.path.abspath(raw_root))))


def _validate_pilot_guard(pilot: PilotGuard, package: EvidencePackageV2) -> str:
    if not pilot.enabled:
        raise ValueError("supervised V2 apply is disabled")
    if not pilot.writer_ready:
        raise ValueError("a fresh writer heartbeat is required")
    if not pilot.pilot_id.strip() or len(pilot.pilot_id) > 128:
        raise ValueError("a valid pilot id is required")
    if not pilot.release_digest.startswith("sha256:") or len(pilot.release_digest) != 71:
        raise ValueError("a sha256 release digest is required")
    try:
        int(pilot.release_digest.removeprefix("sha256:"), 16)
    except ValueError as exc:
        raise ValueError("a sha256 release digest is required") from exc
    if not pilot.alembic_revision.strip():
        raise ValueError("an Alembic revision is required")
    if not 1 <= pilot.max_operations <= 5:
        raise ValueError("the supervised pilot is limited to five operations")
    canonical_root = _canonical_library_root(pilot.library_root)
    if pilot.library_root != canonical_root or package.snapshot.library_root != canonical_root:
        raise ValueError("pilot and evidence library roots do not match")
    return library_root_sha256(canonical_root)


def _lock_or_create_pilot(
    session: Session,
    *,
    pilot: PilotGuard,
    library_root_sha256: str,
) -> PilotSession:
    bound_operations = _pilot_operation_count(session, pilot.pilot_id)
    stored = session.exec(select(PilotSession).where(PilotSession.pilot_id == pilot.pilot_id).with_for_update()).first()
    if stored is None:
        if bound_operations:
            raise ValueError("pilot budget ledger has operations without a persisted session")
        stored = PilotSession(
            pilot_id=pilot.pilot_id,
            library_root_sha256=library_root_sha256,
            release_digest=pilot.release_digest,
            alembic_revision=pilot.alembic_revision,
            max_operations=pilot.max_operations,
        )
        session.add(stored)
        session.flush()
    if (
        stored.library_root_sha256 != library_root_sha256
        or stored.release_digest != pilot.release_digest
        or stored.alembic_revision != pilot.alembic_revision
        or stored.max_operations != pilot.max_operations
    ):
        raise ValueError("pilot runtime binding does not match its persisted session")
    if stored.state != "open":
        raise ValueError("pilot session is closed")
    if stored.reserved_operations != bound_operations:
        raise ValueError("pilot budget ledger does not match its bound operations")
    if stored.reserved_operations >= stored.max_operations:
        raise ValueError("pilot operation limit has been reached")
    return stored


def _pilot_operation_count(session: Session, pilot_id: str) -> int:
    return int(
        session.exec(
            select(func.count()).select_from(OperationLedger).where(OperationLedger.pilot_id == pilot_id)
        ).one()
    )


def _lock_v2_queue_mutex(session: Session) -> None:
    """Serialize every production V2 reservation on one stable transaction lock."""
    if session.get_bind().dialect.name == "postgresql":
        session.execute(
            text("SELECT pg_advisory_xact_lock(:lock_id)"),
            {"lock_id": _V2_QUEUE_ADVISORY_LOCK_ID},
        ).scalar_one()
        return

    # Unit tests use SQLite, while the production contract requires PostgreSQL.
    # Preserve deterministic single-process behavior without pretending that a
    # mutable book key is the production mutex.
    sentinel = session.exec(
        select(BookRecord.book_key).order_by(BookRecord.book_key).limit(1).with_for_update()
    ).first()
    if sentinel is None:
        raise ValueError("a persisted book is required before starting a supervised pilot")


def acknowledge_v2_failure(
    session: Session,
    *,
    operation_id: str,
    actor: str,
    reason: str,
) -> OperationIncidentAcknowledgement:
    """Append evidence that a safe terminal V2 failure was reviewed by an operator."""
    clean_actor = actor.strip()
    clean_reason = reason.strip()
    if not clean_actor or len(clean_actor) > 128 or len(clean_reason) < 12 or len(clean_reason) > 1000:
        raise ValueError("a valid actor and detailed acknowledgement reason are required")
    operation = session.exec(
        select(OperationLedger).where(OperationLedger.operation_id == operation_id).with_for_update()
    ).first()
    if (
        operation is None
        or operation.policy_version != "manifestation-v2"
        or operation.state != "failed"
        or operation.completed_at is None
        or not operation.pilot_id
    ):
        raise ValueError("only a completed V2 operation in a safe failed state can be acknowledged")
    pilot = session.exec(
        select(PilotSession).where(PilotSession.pilot_id == operation.pilot_id).with_for_update()
    ).first()
    if pilot is None or pilot.state != "stopped":
        raise ValueError("the failed V2 operation's supervised pilot must be stopped before acknowledgement")
    existing = session.get(OperationIncidentAcknowledgement, operation_id)
    if existing is not None:
        raise ValueError("operation incident is already acknowledged")
    outbox = session.exec(select(OutboxEvent).where(OutboxEvent.aggregate_id == operation_id).with_for_update()).first()
    active_lock = session.get(BookWriteLock, operation.book_key, with_for_update=True)
    if outbox is None or outbox.status != "failed" or active_lock is not None:
        raise ValueError("the failed V2 operation is not safely quiescent")
    acknowledgement = OperationIncidentAcknowledgement(
        operation_id=operation.operation_id,
        actor=clean_actor,
        reason=clean_reason,
    )
    session.add(acknowledgement)
    session.flush()
    return acknowledgement


def validate_apply_operation(
    session: Session,
    operation: OperationLedger,
    book: BookRecord,
    *,
    pilot: PilotGuard | None = None,
) -> None:
    """Revalidate a sealed queued operation at the privileged writer boundary."""
    if operation.policy_version != "manifestation-v2":
        raise ValueError("legacy metadata apply is disabled at the writer boundary")
    _validate_v2_apply_operation(session, operation, book, pilot=pilot)


def _validate_v2_apply_operation(
    session: Session,
    operation: OperationLedger,
    book: BookRecord,
    *,
    pilot: PilotGuard | None,
) -> None:
    if (
        operation.operation_type != "apply_metadata"
        or not operation.evidence_id
        or operation.run_id != book.run_id
        or operation.calibre_book_id != book.calibre_book_id
        or operation.field_locks_hash != _hash_json(book.field_locks or {})
    ):
        raise ValueError("V2 operation identity changed after authorization")
    _stored, package = load_v2_package(session, operation.evidence_id)
    _validate_v2_book_snapshot(book, package)
    if pilot is None:
        raise ValueError("V2 writer runtime pilot binding is unavailable")
    try:
        runtime_root_sha256 = _validate_pilot_guard(pilot, package)
    except ValueError as exc:
        raise ValueError(f"V2 writer runtime pilot binding is invalid: {exc}") from exc
    if not operation.pilot_id or operation.pilot_id != pilot.pilot_id:
        raise ValueError("V2 operation is not bound to a supervised pilot")
    stored_pilot = session.get(PilotSession, operation.pilot_id)
    if stored_pilot is not None and stored_pilot.reserved_operations != _pilot_operation_count(
        session, operation.pilot_id
    ):
        raise ValueError("V2 supervised pilot budget ledger does not match its bound operations")
    if (
        stored_pilot is None
        or stored_pilot.state != "open"
        or stored_pilot.reserved_operations < 1
        or stored_pilot.reserved_operations > stored_pilot.max_operations
        or stored_pilot.library_root_sha256 != runtime_root_sha256
        or stored_pilot.release_digest != pilot.release_digest
        or stored_pilot.alembic_revision != pilot.alembic_revision
        or stored_pilot.alembic_revision != expected_schema_revision()
        or stored_pilot.max_operations != pilot.max_operations
    ):
        raise ValueError("V2 supervised pilot runtime binding is invalid or stopped")
    patch = _v2_patch(package, book)
    if (
        package.identity.tier is not IdentityTier.tier_a
        or package.package_sha256 != operation.verdict_hash
        or patch != operation.requested_patch
        or operation.patch_hash != _hash_json(patch)
    ):
        raise ValueError("V2 evidence seal or canonical patch changed after authorization")
    authorization = session.exec(
        select(ManualAuthorization).where(ManualAuthorization.authorization_id == operation.authorization_id)
    ).first()
    if not _v2_authorization_matches(authorization, book=book, package=package, patch=patch):
        raise ValueError("V2 operation lacks an exact manual authorization")


def queue_undo_operation(session: Session, change: Change) -> str:
    """Queue an idempotent undo without granting the API filesystem write access."""
    if change.id is None:
        raise ValueError("change must be persisted before it can be undone")
    book = session.exec(select(BookRecord).where(BookRecord.book_key == change.book_key)).first()
    operation = create_operation(
        session,
        idempotency_key=f"undo:{change.id}:{change.status}",
        operation_type="undo_change",
        book_key=change.book_key,
        run_id=change.run_id,
        requested_patch={"change_id": change.id},
        calibre_book_id=book.calibre_book_id if book else None,
        expected_before_metadata=change.after_metadata,
    )
    if book:
        book.status = "undo_queued"
        session.add(book)
    session.commit()
    return operation.operation_id


def queue_approved_operations(
    session: Session,
    *,
    book_keys: list[str],
    authorization_ids: dict[str, str],
) -> CoordinationResult:
    """Validate verdicts and atomically enqueue idempotent writer operations."""
    books = session.exec(
        select(BookRecord).where(BookRecord.status == "suggest_fix").where(col(BookRecord.book_key).in_(book_keys))
    ).all()
    gate = ConservativeAutoApply(dry_run=False)
    queued: list[str] = []
    skipped: list[str] = []

    for book in books:
        package = session.exec(
            select(EvidencePackage)
            .where(EvidencePackage.book_key == book.book_key)
            .where(EvidencePackage.run_id == book.run_id)
        ).first()
        if package is None or package.decision is None:
            skipped.append(book.book_key)
            continue
        try:
            verdict = BookVerdict.model_validate(package.decision)
        except ValueError:
            skipped.append(book.book_key)
            continue
        if verdict.book_key != book.book_key or verdict.run_id != book.run_id:
            skipped.append(book.book_key)
            continue

        raw_patch = {key: value for key, value in verdict.proposed_patch.items() if key not in (book.field_locks or {})}
        try:
            patch = _canonicalize_legacy_patch(raw_patch)
        except ValueError:
            skipped.append(book.book_key)
            continue
        if not patch:
            skipped.append(book.book_key)
            continue

        eligible, _ = gate.is_eligible(verdict)
        authorization: ManualAuthorization | None = None
        authorization_id = authorization_ids.get(book.book_key)
        if authorization_id:
            authorization = session.exec(
                select(ManualAuthorization).where(ManualAuthorization.authorization_id == authorization_id)
            ).first()
        if not eligible and not _authorization_matches(authorization, book=book, verdict=verdict):
            skipped.append(book.book_key)
            continue

        operation = create_operation(
            session,
            idempotency_key=f"apply:{book.run_id}:{book.book_key}:{_hash_json(patch)}",
            operation_type="apply_metadata",
            book_key=book.book_key,
            run_id=book.run_id,
            requested_patch=patch,
            authorization_id=authorization.authorization_id if authorization else None,
            calibre_book_id=book.calibre_book_id,
            verdict_hash=_hash_verdict(verdict),
            patch_hash=_hash_json(patch),
            field_locks_hash=_hash_json(book.field_locks or {}),
            expected_before_metadata={field: book.current_metadata.get(field) for field in patch},
        )
        if operation.state == "requested":
            book.status = "apply_queued"
            session.add(book)
            queued.append(operation.operation_id)
        else:
            skipped.append(book.book_key)

    session.commit()
    return CoordinationResult(queued_operation_ids=queued, skipped_book_keys=skipped)


def queue_v2_operation(
    session: Session,
    *,
    evidence_id: str,
    authorization_id: str,
    pilot: PilotGuard,
) -> str:
    """Atomically reserve and enqueue exactly one supervised V2 operation."""
    try:
        _stored, package = load_v2_package(session, evidence_id)
        book = session.exec(select(BookRecord).where(BookRecord.book_key == package.book_key)).first()
        if book is None or book.run_id != package.run_id or package.identity.tier is not IdentityTier.tier_a:
            raise ValueError("book snapshot or Tier A identity is unavailable")
        _validate_v2_book_snapshot(book, package)
        patch = _v2_patch(package, book)
        if not patch:
            raise ValueError("canonical patch is empty")
        authorization = session.exec(
            select(ManualAuthorization).where(ManualAuthorization.authorization_id == authorization_id)
        ).first()
        if not _v2_authorization_matches(authorization, book=book, package=package, patch=patch):
            raise ValueError("exact manual authorization is required")

        library_root_sha256 = _validate_pilot_guard(pilot, package)
        _lock_v2_queue_mutex(session)
        stored_pilot = _lock_or_create_pilot(
            session,
            pilot=pilot,
            library_root_sha256=library_root_sha256,
        )

        nonterminal = session.exec(
            select(OperationLedger).where(col(OperationLedger.state).in_(_NONTERMINAL_OPERATION_STATES))
        ).first()
        if nonterminal is not None:
            raise ValueError("another nonterminal operation is already present")
        unpublished = session.exec(
            select(OutboxEvent)
            .outerjoin(
                OperationLedger,
                col(OperationLedger.operation_id) == col(OutboxEvent.aggregate_id),
            )
            .outerjoin(
                OperationIncidentAcknowledgement,
                col(OperationIncidentAcknowledgement.operation_id) == col(OutboxEvent.aggregate_id),
            )
            .outerjoin(
                PilotSession,
                col(PilotSession.pilot_id) == col(OperationLedger.pilot_id),
            )
            .outerjoin(
                BookWriteLock,
                col(BookWriteLock.book_key) == col(OperationLedger.book_key),
            )
            .where(OutboxEvent.status != "published")
            .where(
                or_(
                    col(OperationIncidentAcknowledgement.operation_id).is_(None),
                    col(OperationLedger.policy_version) != "manifestation-v2",
                    col(OperationLedger.state) != "failed",
                    col(OperationLedger.completed_at).is_(None),
                    col(OutboxEvent.status) != "failed",
                    col(OperationLedger.pilot_id).is_(None),
                    col(PilotSession.pilot_id).is_(None),
                    col(PilotSession.state) != "stopped",
                    col(OperationLedger.pilot_id) == stored_pilot.pilot_id,
                    col(BookWriteLock.book_key).is_not(None),
                )
            )
        ).first()
        if unpublished is not None:
            raise ValueError("the writer outbox is not empty")

        operation = create_operation(
            session,
            idempotency_key=f"apply-v2:{pilot.pilot_id}:{package.evidence_id}:{_hash_json(patch)}",
            operation_type="apply_metadata",
            book_key=book.book_key,
            run_id=book.run_id,
            requested_patch=patch,
            authorization_id=authorization.authorization_id if authorization else None,
            calibre_book_id=book.calibre_book_id,
            verdict_hash=package.package_sha256,
            patch_hash=_hash_json(patch),
            field_locks_hash=_hash_json(book.field_locks or {}),
            expected_before_metadata={field: _current_value_for_patch(book, field) for field in patch},
            policy_version="manifestation-v2",
            evidence_id=package.evidence_id,
            pilot_id=stored_pilot.pilot_id,
        )
        if operation.state != "requested":
            raise ValueError("operation was already queued")
        expected_reserved_operations = stored_pilot.reserved_operations + 1
        bound_operations = _pilot_operation_count(session, stored_pilot.pilot_id)
        if bound_operations != expected_reserved_operations:
            raise ValueError("pilot budget ledger changed while reserving an operation")
        stored_pilot.reserved_operations = bound_operations
        stored_pilot.updated_at = utc_now()
        book.status = "apply_queued"
        session.add(stored_pilot)
        session.add(book)
        session.commit()
        return operation.operation_id
    except Exception:
        session.rollback()
        raise

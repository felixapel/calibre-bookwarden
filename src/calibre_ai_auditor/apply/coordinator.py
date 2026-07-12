"""Read-only API coordinator for durable metadata write requests."""

import hashlib
import json
import logging
from dataclasses import dataclass
from uuid import uuid4

from sqlmodel import Session, col, select

from calibre_ai_auditor.storage.models import BookRecord, Change, EvidencePackage, ManualAuthorization, OperationLedger
from calibre_ai_auditor.storage.operations import create_operation
from calibre_ai_auditor.verification.restore import ConservativeAutoApply
from calibre_ai_auditor.verification.verdict import BookVerdict

logger = logging.getLogger(__name__)


def _hash_json(value: object) -> str:
    encoded = json.dumps(value, sort_keys=True, separators=(",", ":"), ensure_ascii=True).encode()
    return hashlib.sha256(encoded).hexdigest()


def _hash_verdict(verdict: BookVerdict) -> str:
    return _hash_json(verdict.model_dump(mode="json", exclude={"created_at"}))


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


def validate_apply_operation(session: Session, operation: OperationLedger, book: BookRecord) -> None:
    """Revalidate a sealed queued operation at the privileged writer boundary."""
    if (
        operation.operation_type != "apply_metadata"
        or operation.policy_version != "v1"
        or operation.run_id != book.run_id
        or operation.calibre_book_id != book.calibre_book_id
        or operation.patch_hash != _hash_json(operation.requested_patch)
        or operation.field_locks_hash != _hash_json(book.field_locks or {})
    ):
        raise ValueError("operation identity or seal changed after authorization")
    package = session.exec(
        select(EvidencePackage)
        .where(EvidencePackage.book_key == book.book_key)
        .where(EvidencePackage.run_id == book.run_id)
    ).first()
    if package is None or package.decision is None:
        raise ValueError("persisted verdict is unavailable")
    verdict = BookVerdict.model_validate(package.decision)
    if operation.verdict_hash != _hash_verdict(verdict):
        raise ValueError("persisted verdict changed after authorization")
    patch = {key: value for key, value in verdict.proposed_patch.items() if key not in (book.field_locks or {})}
    if patch != operation.requested_patch:
        raise ValueError("authorized patch no longer matches the operation")
    eligible, _ = ConservativeAutoApply(dry_run=False).is_eligible(verdict)
    authorization = None
    if operation.authorization_id:
        authorization = session.exec(
            select(ManualAuthorization).where(
                ManualAuthorization.authorization_id == operation.authorization_id,
            )
        ).first()
    if not eligible and not _authorization_matches(authorization, book=book, verdict=verdict):
        raise ValueError("operation is no longer eligible or exactly authorized")


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
        select(BookRecord)
        .where(BookRecord.status == "suggest_fix")
        .where(col(BookRecord.book_key).in_(book_keys))
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

        patch = {key: value for key, value in verdict.proposed_patch.items() if key not in (book.field_locks or {})}
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

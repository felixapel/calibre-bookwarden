"""Read-only API coordinator for durable metadata write requests."""

import hashlib
import json
import logging
from dataclasses import dataclass
from uuid import uuid4

from sqlmodel import Session, select

from calibre_ai_auditor.storage.models import BookRecord, Change, EvidencePackage, ManualAuthorization
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


def queue_undo_operation(session: Session, change: Change) -> str:
    """Queue an idempotent undo without granting the API filesystem write access."""
    if change.id is None:
        raise ValueError("change must be persisted before it can be undone")
    operation = create_operation(
        session,
        idempotency_key=f"undo:{change.id}:{change.status}",
        operation_type="undo_change",
        book_key=change.book_key,
        run_id=change.run_id,
        requested_patch={"change_id": change.id},
    )
    book = session.exec(select(BookRecord).where(BookRecord.book_key == change.book_key)).first()
    if book:
        book.status = "undo_queued"
        session.add(book)
    session.commit()
    return operation.operation_id


def queue_approved_operations(
    session: Session,
    *,
    authorization_ids: dict[str, str],
) -> CoordinationResult:
    """Validate verdicts and atomically enqueue idempotent writer operations."""
    books = session.exec(select(BookRecord).where(BookRecord.status == "suggest_fix")).all()
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
        )
        book.status = "apply_queued"
        session.add(book)
        queued.append(operation.operation_id)

    session.commit()
    return CoordinationResult(queued_operation_ids=queued, skipped_book_keys=skipped)

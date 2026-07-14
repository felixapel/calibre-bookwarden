from __future__ import annotations

from datetime import UTC, datetime

import pytest
from sqlmodel import Session, SQLModel, create_engine, select

from calibre_ai_auditor.apply.coordinator import (
    create_v2_manual_authorization,
    queue_v2_operations,
    validate_apply_operation,
)
from calibre_ai_auditor.storage.models import BookRecord, EvidencePackage, OperationLedger
from calibre_ai_auditor.verification.identity_v2 import IdentityTier, ManifestationResolution
from calibre_ai_auditor.verification.pipeline_v2 import BookAuditState, BookSnapshot, EvidencePackageV2


def _v2_package() -> EvidencePackageV2:
    snapshot = BookSnapshot(
        book_key="calibre:8",
        calibre_book_id=8,
        current_metadata={"title": "Old title", "#edition": "First edition"},
        files=[],
        snapshot_sha256="0" * 64,
    )
    snapshot = snapshot.model_copy(update={"snapshot_sha256": snapshot.calculated_sha256()})
    return EvidencePackageV2(
        evidence_id="evidence-v2-apply",
        run_id="run-v2-apply",
        book_key="calibre:8",
        created_at=datetime.now(UTC),
        state=BookAuditState.shadowed,
        snapshot=snapshot,
        identity=ManifestationResolution(
            tier=IdentityTier.tier_a,
            manifestation_ids={"isbn": "9780306406157"},
            auto_patch={"title": "Exact title", "edition_statement": "Second edition"},
        ),
    ).seal()


def _seed(session: Session) -> tuple[BookRecord, EvidencePackageV2]:
    package = _v2_package()
    book = BookRecord(
        book_key=package.book_key,
        run_id=package.run_id,
        calibre_book_id=8,
        status="shadowed",
        current_metadata=package.snapshot.current_metadata,
    )
    session.add(book)
    session.add(
        EvidencePackage(
            evidence_id=package.evidence_id,
            book_key=package.book_key,
            run_id=package.run_id,
            schema_version=2,
            current=package.snapshot.current_metadata,
            extracted=package.identity.model_dump(mode="json"),
            observations=package.model_dump(mode="json"),
        )
    )
    session.commit()
    return book, package


def test_v2_queue_is_bound_to_sealed_evidence_patch_and_manual_authorization() -> None:
    engine = create_engine("sqlite:///:memory:")
    SQLModel.metadata.create_all(engine)
    with Session(engine) as session:
        book, package = _seed(session)
        authorization = create_v2_manual_authorization(
            session,
            book=book,
            package=package,
            actor="operator",
            reason="Compared the exact edition",
        )
        session.commit()

        result = queue_v2_operations(
            session,
            evidence_ids=[package.evidence_id],
            authorization_ids={package.evidence_id: authorization.authorization_id},
        )
        operation = session.exec(select(OperationLedger)).one()

        assert result.queued_operation_ids == [operation.operation_id]
        assert operation.policy_version == "manifestation-v2"
        assert operation.evidence_id == package.evidence_id
        assert operation.requested_patch == {
            "title": "Exact title",
            "edition_statement": "Second edition",
        }
        assert operation.expected_before_metadata == {
            "title": "Old title",
            "edition_statement": "First edition",
        }
        validate_apply_operation(session, operation, book)


def test_v2_queue_requires_tier_a_nonempty_patch_and_exact_authorization() -> None:
    engine = create_engine("sqlite:///:memory:")
    SQLModel.metadata.create_all(engine)
    with Session(engine) as session:
        _book, package = _seed(session)

        result = queue_v2_operations(session, evidence_ids=[package.evidence_id], authorization_ids={})

    assert result.queued_operation_ids == []
    assert result.skipped_book_keys == ["calibre:8"]


def test_v2_validation_rejects_tampered_stored_package_after_queue() -> None:
    engine = create_engine("sqlite:///:memory:")
    SQLModel.metadata.create_all(engine)
    with Session(engine) as session:
        book, package = _seed(session)
        authorization = create_v2_manual_authorization(
            session,
            book=book,
            package=package,
            actor="operator",
            reason="Compared the exact edition",
        )
        session.commit()
        queue_v2_operations(
            session,
            evidence_ids=[package.evidence_id],
            authorization_ids={package.evidence_id: authorization.authorization_id},
        )
        operation = session.exec(select(OperationLedger)).one()
        stored = session.exec(select(EvidencePackage)).one()
        assert stored.observations is not None
        stored.observations = {**stored.observations, "state": "verified"}
        session.add(stored)
        session.commit()

        with pytest.raises(ValueError, match="seal"):
            validate_apply_operation(session, operation, book)

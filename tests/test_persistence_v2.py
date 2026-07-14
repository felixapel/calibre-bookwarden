from __future__ import annotations

from datetime import UTC, datetime

import pytest
from sqlmodel import Session, SQLModel, create_engine, select

from calibre_ai_auditor.storage.models import EvidencePackage, VerificationResult, VerificationRun
from calibre_ai_auditor.verification.identity_v2 import (
    FormatEvidence,
    FormatEvidenceStatus,
    IdentityTier,
    ManifestationResolution,
)
from calibre_ai_auditor.verification.persistence_v2 import SQLAuditStore
from calibre_ai_auditor.verification.pipeline_v2 import (
    AuditMode,
    BookAuditState,
    BookSnapshot,
    EvidencePackageV2,
    NullEvidenceEnricher,
)
from calibre_ai_auditor.verification.service_v2 import run_persisted_library_audit


def _package() -> EvidencePackageV2:
    snapshot = BookSnapshot(
        book_key="calibre:1",
        calibre_book_id=1,
        current_metadata={"title": "Current"},
        files=["/library/book.epub"],
        snapshot_sha256="0" * 64,
    )
    snapshot = snapshot.model_copy(update={"snapshot_sha256": snapshot.calculated_sha256()})
    return EvidencePackageV2(
        evidence_id="evidence-v2-1",
        run_id="run-v2",
        book_key="calibre:1",
        created_at=datetime.now(UTC),
        state=BookAuditState.review,
        snapshot=snapshot,
        formats=[
            FormatEvidence(
                path="/library/book.epub",
                format="EPUB",
                sha256="a" * 64,
                status=FormatEvidenceStatus.readable,
            )
        ],
        identity=ManifestationResolution(
            tier=IdentityTier.tier_b,
            risk_flags=["missing_external_manifestation_confirmation"],
        ),
    ).seal()


def test_store_persists_sealed_v2_package_without_forging_legacy_decision() -> None:
    engine = create_engine("sqlite:///:memory:")
    SQLModel.metadata.create_all(engine)
    store = SQLAuditStore(engine, "run-v2")

    store.start(book_keys=["calibre:1", "calibre:2"], mode="shadow", use_llm=False)
    store.record_state("calibre:1", BookAuditState.extracting)
    store.record_package(_package())
    store.finish("completed")

    with Session(engine) as session:
        run = session.exec(select(VerificationRun)).one()
        result = session.exec(select(VerificationResult).where(VerificationResult.book_key == "calibre:1")).one()
        evidence = session.exec(select(EvidencePackage)).one()

    assert run.pipeline_version == "manifestation-v2"
    assert run.mode == "shadow"
    assert run.completed == 1
    assert result.evidence_id == "evidence-v2-1"
    assert result.state == "review"
    assert result.verdict["identity"]["tier"] == "B"
    assert evidence.schema_version == 2
    assert evidence.decision is None
    assert evidence.observations is not None
    assert EvidencePackageV2.model_validate(evidence.observations).verify_seal()
    assert store.resumable_book_keys() == {"calibre:1"}


def test_store_rejects_unsealed_or_tampered_packages() -> None:
    engine = create_engine("sqlite:///:memory:")
    SQLModel.metadata.create_all(engine)
    store = SQLAuditStore(engine, "run-v2")
    store.start(book_keys=["calibre:1"], mode="shadow", use_llm=False)
    package = _package().model_copy(update={"package_sha256": "0" * 64})

    with pytest.raises(ValueError, match="seal"):
        store.record_package(package)


def test_resume_rejects_changed_frozen_library_membership() -> None:
    engine = create_engine("sqlite:///:memory:")
    SQLModel.metadata.create_all(engine)
    store = SQLAuditStore(engine, "run-v2")
    store.start(book_keys=["calibre:1", "calibre:2"], mode="shadow", use_llm=False)

    with pytest.raises(ValueError, match="membership"):
        store.start(book_keys=["calibre:1", "calibre:3"], mode="shadow", use_llm=False)


def test_resume_retries_terminal_row_with_tampered_evidence() -> None:
    engine = create_engine("sqlite:///:memory:")
    SQLModel.metadata.create_all(engine)
    store = SQLAuditStore(engine, "run-v2")
    store.start(book_keys=["calibre:1"], mode="shadow", use_llm=False)
    store.record_package(_package())

    with Session(engine) as session:
        stored = session.exec(select(EvidencePackage)).one()
        assert stored.observations is not None
        stored.observations = {**stored.observations, "state": "verified"}
        session.add(stored)
        session.commit()

    assert store.resumable_book_keys() == set()


def test_record_package_rejects_existing_evidence_id_with_different_payload() -> None:
    engine = create_engine("sqlite:///:memory:")
    SQLModel.metadata.create_all(engine)
    store = SQLAuditStore(engine, "run-v2")
    store.start(book_keys=["calibre:1"], mode="shadow", use_llm=False)
    package = _package()
    store.record_package(package)
    changed = package.model_copy(update={"warnings": ["different"]}).seal()

    with pytest.raises(ValueError, match="already exists"):
        store.record_package(changed)


@pytest.mark.asyncio
async def test_persisted_audit_resumes_without_reprocessing_terminal_books(tmp_path) -> None:
    file_path = tmp_path / "book.unknown"
    file_path.write_bytes(b"not a supported ebook")

    class CLI:
        def list_books(self):
            return [{"id": 1, "formats": [str(file_path)]}]

        def show_metadata(self, _book_id: int):
            return {"id": 1, "title": "Current", "formats": [str(file_path)]}

    engine = create_engine("sqlite:///:memory:")
    SQLModel.metadata.create_all(engine)
    first = await run_persisted_library_audit(
        cli=CLI(),
        database_engine=engine,
        run_id="resume-v2",
        limit=0,
        mode=AuditMode.shadow,
        evidence_enricher=NullEvidenceEnricher(),
        use_llm=False,
    )
    second = await run_persisted_library_audit(
        cli=CLI(),
        database_engine=engine,
        run_id="resume-v2",
        limit=0,
        mode=AuditMode.shadow,
        evidence_enricher=NullEvidenceEnricher(),
        use_llm=False,
    )

    assert len(first.packages) == 1
    assert second.packages == []
    with Session(engine) as session:
        assert len(session.exec(select(EvidencePackage)).all()) == 1

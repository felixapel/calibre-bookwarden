from __future__ import annotations

from collections.abc import Iterator
from contextlib import contextmanager
from datetime import UTC, datetime
from pathlib import Path

import pytest
from sqlmodel import Session, SQLModel, create_engine, select

from calibre_ai_auditor.storage.models import BookRecord, EvidencePackage, VerificationResult, VerificationRun
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
from tests.v2_fixtures import as_remote_package


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


def test_store_preserves_remote_source_and_format_without_path_inference() -> None:
    engine = create_engine("sqlite:///:memory:")
    SQLModel.metadata.create_all(engine)
    package = as_remote_package(_package())
    store = SQLAuditStore(engine, "run-v2")
    store.start(book_keys=[package.book_key], mode="shadow", use_llm=False)

    store.record_package(package)

    with Session(engine) as session:
        book = session.exec(select(BookRecord)).one()
    assert book.source == "calibre_content_server"
    assert book.files == [{"path": package.snapshot.files[0], "format": "EPUB"}]


def test_local_package_seal_remains_compatible_with_pre_remote_payload() -> None:
    import hashlib
    import json

    package = _package()
    payload = package.model_dump(mode="json", exclude={"package_sha256"})
    payload["snapshot"].pop("source")
    payload["snapshot"].pop("source_revision_sha256")
    encoded = json.dumps(payload, sort_keys=True, separators=(",", ":"), ensure_ascii=True).encode()

    assert package.package_sha256 == hashlib.sha256(encoded).hexdigest()


def test_store_rejects_unsealed_or_tampered_packages() -> None:
    engine = create_engine("sqlite:///:memory:")
    SQLModel.metadata.create_all(engine)
    store = SQLAuditStore(engine, "run-v2")
    store.start(book_keys=["calibre:1"], mode="shadow", use_llm=False)
    package = _package().model_copy(update={"package_sha256": "0" * 64})

    with pytest.raises(ValueError, match="seal"):
        store.record_package(package)


def test_resume_retries_a_source_changed_book() -> None:
    engine = create_engine("sqlite:///:memory:")
    SQLModel.metadata.create_all(engine)
    store = SQLAuditStore(engine, "run-v2")
    store.start(book_keys=["calibre:1"], mode="shadow", use_llm=False)
    base = _package()
    changed = base.model_copy(
        update={
            "state": BookAuditState.source_changed,
            "formats": [],
            "identity": ManifestationResolution(
                tier=IdentityTier.tier_c,
                risk_flags=["source_changed"],
            ),
            "error": "source changed",
            "package_sha256": None,
        }
    ).seal()
    store.record_package(changed)

    assert store.resumable_book_keys() == set()


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


@pytest.mark.asyncio
async def test_persisted_audit_preserves_remote_reader_capabilities(tmp_path: Path) -> None:
    class RemoteCLI:
        source_kind = "calibre_content_server"
        fingerprint = "d" * 64

        def list_books(self):
            return [{"id": 7, "title": "Private", "formats": ["/remote/book.unknown"]}]

        def show_metadata(self, _book_id: int):
            return {"id": 7, "title": "Private", "formats": ["/remote/book.unknown"]}

        def format_references(self, book_id: int, _raw_formats: object) -> list[str]:
            return [f"calibre-server:{self.fingerprint}:{book_id}:UNKNOWN"]

        def format_from_reference(self, _reference: str) -> str:
            return "UNKNOWN"

        @contextmanager
        def export_format(self, _book_id: int, _format: str, *, scratch_root: Path) -> Iterator[Path]:
            path = scratch_root / "book.unknown"
            path.write_bytes(b"not a supported ebook")
            try:
                yield path
            finally:
                path.unlink()

    engine = create_engine("sqlite:///:memory:")
    SQLModel.metadata.create_all(engine)
    result = await run_persisted_library_audit(
        cli=RemoteCLI(),
        database_engine=engine,
        run_id="remote-v2",
        limit=0,
        mode=AuditMode.shadow,
        evidence_enricher=NullEvidenceEnricher(),
        use_llm=False,
        scratch_root=tmp_path,
    )

    expected_key = f"calibre-server:{'d' * 64}:7"
    assert result.snapshot.book_keys == [expected_key]
    with Session(engine) as session:
        book = session.exec(select(BookRecord)).one()
    assert book.book_key == expected_key
    assert book.source == "calibre_content_server"

from __future__ import annotations

import hashlib
from collections.abc import Iterator
from contextlib import contextmanager
from datetime import UTC, datetime, timedelta
from pathlib import Path

import pytest
from sqlalchemy.engine import Engine
from sqlmodel import Session, SQLModel, create_engine, select

from calibre_ai_auditor.config.settings import Settings
from calibre_ai_auditor.storage.db import expected_schema_revision
from calibre_ai_auditor.storage.models import VerificationResult, VerificationRun
from calibre_ai_auditor.verification.certificate_a_worker import (
    CertificateAContractError,
    FencedCertificateAStore,
    LostVerifierLeaseError,
    SourceSnapshotMismatchError,
    claim_next_certificate_a_run,
    execute_certificate_a_claim,
    heartbeat_certificate_a_run,
    load_certificate_a_contract,
)
from calibre_ai_auditor.verification.pipeline_v2 import BookAuditState, NullEvidenceEnricher

RELEASE_DIGEST = f"sha256:{'a' * 64}"


@pytest.fixture
def engine(tmp_path: Path) -> Engine:
    result = create_engine(
        f"sqlite:///{tmp_path / 'worker.db'}",
        connect_args={"check_same_thread": False},
    )
    SQLModel.metadata.create_all(result)
    return result


def _settings(library: Path) -> Settings:
    return Settings(
        profile="production",
        release_digest=RELEASE_DIGEST,
        library={"path": library, "read_only": True},
        database={"backend": "postgres", "postgres_dsn": "postgresql+psycopg://verifier:test@db/audit"},
        queue={"backend": "valkey", "valkey_url": "redis://valkey:6379/0"},
        providers={"calibre_fetch": False, "google_books": True, "openlibrary": True},
        recognition_v2={
            "ocr": {"enabled": True, "backends": ["tesseract"], "max_pages": 6, "language": "eng"},
            "vision": {"enabled": False},
        },
        ollama_enabled=False,
        lmstudio_enabled=False,
    )


def _seed_run(engine: Engine, library: Path, *, run_id: str = "verify_certificate_a") -> str:
    root = str(library.absolute())
    root_sha256 = hashlib.sha256(root.encode()).hexdigest()
    with Session(engine) as session:
        session.add(
            VerificationRun(
                run_id=run_id,
                status="pending",
                total=0,
                completed=0,
                counts={},
                use_llm=False,
                pipeline_version="manifestation-v2",
                mode="shadow",
                contract_version="certificate-a-v1",
                idempotency_key=f"request-{run_id}",
                request_sha256="b" * 64,
                source_root=root,
                source_root_sha256=root_sha256,
                effective_config={
                    "contract_version": "certificate-a-v1",
                    "pipeline_version": "manifestation-v2",
                    "policy_version": "manifestation-v2",
                    "schema_revision": expected_schema_revision(),
                    "release_digest": RELEASE_DIGEST,
                    "mode": "shadow",
                    "source": {"kind": "offline-folder", "root_sha256": root_sha256},
                    "limits": {"book_limit": 2},
                    "providers": ["google_books_isbn", "openlibrary_isbn"],
                    "recognition": {
                        "ocr_enabled": False,
                        "ocr_backend": "tesseract",
                        "ocr_language": "eng",
                        "ocr_max_pages": 6,
                        "vision_enabled": False,
                        "llm_enabled": False,
                    },
                    "privacy": {"remote_text": False, "remote_images": False},
                },
                fence_token=0,
            )
        )
        session.commit()
    return run_id


def test_claim_is_atomic_and_reclaim_increments_the_fence(engine: Engine, tmp_path: Path) -> None:
    run_id = _seed_run(engine, tmp_path / "library")
    now = datetime.now(UTC).replace(tzinfo=None)

    first = claim_next_certificate_a_run(engine, owner="verifier-a", now=now, ttl_seconds=60)
    blocked = claim_next_certificate_a_run(engine, owner="verifier-b", now=now, ttl_seconds=60)

    assert first is not None
    assert first.run_id == run_id
    assert first.fence_token == 1
    assert blocked is None
    assert heartbeat_certificate_a_run(engine, first, now=now + timedelta(seconds=10), ttl_seconds=60)

    reclaimed = claim_next_certificate_a_run(
        engine,
        owner="verifier-b",
        now=now + timedelta(seconds=71),
        ttl_seconds=60,
    )
    assert reclaimed is not None
    assert reclaimed.run_id == run_id
    assert reclaimed.fence_token == 2
    assert not heartbeat_certificate_a_run(
        engine,
        first,
        now=now + timedelta(seconds=72),
        ttl_seconds=60,
    )


def test_every_store_write_is_fenced_and_inventory_is_replay_safe(engine: Engine, tmp_path: Path) -> None:
    run_id = _seed_run(engine, tmp_path / "library")
    now = datetime.now(UTC).replace(tzinfo=None)
    first = claim_next_certificate_a_run(engine, owner="verifier-a", now=now, ttl_seconds=10)
    assert first is not None
    first_store = FencedCertificateAStore(engine, first, clock=lambda: now + timedelta(seconds=1))
    snapshot = {"fingerprint": "c" * 64, "book_count": 2, "books": [{"book_id": 1}, {"book_id": 2}]}

    first_store.inventory(
        book_keys=["calibre-offline:c:1", "calibre-offline:c:2"],
        source_snapshot=snapshot,
    )
    with Session(engine) as session:
        run = session.exec(select(VerificationRun).where(VerificationRun.run_id == run_id)).one()
        assert run.status == "running"
        assert run.total == 2
        assert run.source_snapshot == snapshot
        assert run.inventory_finished_at is not None
        assert len(session.exec(select(VerificationResult)).all()) == 2

    second = claim_next_certificate_a_run(
        engine,
        owner="verifier-b",
        now=now + timedelta(seconds=11),
        ttl_seconds=30,
    )
    assert second is not None
    with pytest.raises(LostVerifierLeaseError):
        first_store.record_state("calibre-offline:c:1", BookAuditState.extracting)

    second_store = FencedCertificateAStore(engine, second, clock=lambda: now + timedelta(seconds=12))
    second_store.inventory(
        book_keys=["calibre-offline:c:1", "calibre-offline:c:2"],
        source_snapshot=snapshot,
    )
    with pytest.raises(SourceSnapshotMismatchError):
        second_store.inventory(
            book_keys=["calibre-offline:c:1", "calibre-offline:c:3"],
            source_snapshot={**snapshot, "fingerprint": "d" * 64},
        )


def test_contract_is_rebuilt_only_from_persisted_certificate_a_options(engine: Engine, tmp_path: Path) -> None:
    library = tmp_path / "library"
    run_id = _seed_run(engine, library)
    claim = claim_next_certificate_a_run(engine, owner="verifier-a")
    assert claim is not None

    contract = load_certificate_a_contract(engine, claim, _settings(library))

    assert contract.run_id == run_id
    assert contract.source_root == library.absolute()
    assert contract.book_limit == 2
    assert contract.use_ocr is False
    assert contract.ocr_backend == "tesseract"
    assert contract.ocr_language == "eng"
    assert contract.ocr_max_pages == 6
    assert contract.providers == ("google_books_isbn", "openlibrary_isbn")


def test_contract_rejects_release_or_root_drift(engine: Engine, tmp_path: Path) -> None:
    library = tmp_path / "library"
    _seed_run(engine, library)
    claim = claim_next_certificate_a_run(engine, owner="verifier-a")
    assert claim is not None

    wrong_release = _settings(library).model_copy(update={"release_digest": f"sha256:{'d' * 64}"})
    with pytest.raises(CertificateAContractError, match="release"):
        load_certificate_a_contract(engine, claim, wrong_release)

    with pytest.raises(CertificateAContractError, match="source root"):
        load_certificate_a_contract(engine, claim, _settings(tmp_path / "another-library"))


@pytest.mark.asyncio
async def test_claim_execution_uses_frozen_options_and_finishes_durably(engine: Engine, tmp_path: Path) -> None:
    library = tmp_path / "library"
    library.mkdir()
    run_id = _seed_run(engine, library)
    claim = claim_next_certificate_a_run(engine, owner="verifier-a")
    assert claim is not None
    settings = _settings(library)
    settings.verifier = settings.verifier.model_copy(
        update={"scratch_dir": tmp_path / "scratch", "lease_ttl_seconds": 30}
    )

    class FakeOfflineSource:
        source_kind = "offline_calibre_snapshot"
        fingerprint = "c" * 64

        def __init__(
            self,
            root: Path,
            *,
            snapshot_root: Path,
            confirm_calibre_stopped: bool,
        ) -> None:
            assert root == library
            assert snapshot_root == tmp_path / "scratch"
            assert confirm_calibre_stopped is True

        def __enter__(self):  # type: ignore[no-untyped-def]
            return self

        def __exit__(self, *_args: object) -> None:
            return None

        @property
        def snapshot_manifest(self) -> dict[str, object]:
            return {"fingerprint": self.fingerprint, "book_count": 1, "books": [{"book_id": 1}]}

        def list_books(self) -> list[dict[str, object]]:
            return [{"id": 1, "title": "Frozen", "formats": [str(library / "book.unknown")]}]

        def show_metadata(self, _book_id: int) -> dict[str, object]:
            return self.list_books()[0]

        def format_references(self, book_id: int, _raw: object) -> list[str]:
            return [f"calibre-offline:{self.fingerprint}:{book_id}:UNKNOWN"]

        def format_from_reference(self, _reference: str) -> str:
            return "UNKNOWN"

        @contextmanager
        def export_format(
            self,
            _book_id: int,
            _format: str,
            *,
            scratch_root: Path,
        ) -> Iterator[Path]:
            scratch_root.mkdir(parents=True, exist_ok=True)
            path = scratch_root / "materialized.unknown"
            path.write_bytes(b"fixture")
            try:
                yield path
            finally:
                path.unlink()

        def assert_unchanged(self, _book_id: int | None = None) -> None:
            return None

    status = await execute_certificate_a_claim(
        database_engine=engine,
        claim=claim,
        settings=settings,
        evidence_enricher=NullEvidenceEnricher(),
        source_factory=FakeOfflineSource,
    )

    assert status == "completed"
    with Session(engine) as session:
        run = session.exec(select(VerificationRun).where(VerificationRun.run_id == run_id)).one()
        result = session.exec(select(VerificationResult).where(VerificationResult.run_id == run_id)).one()
        assert run.status == "completed"
        assert run.finished_at is not None
        assert run.lease_owner is None
        assert run.total == 1
        assert run.completed == 1
        assert run.source_snapshot is not None
        assert run.source_snapshot["selected_book_keys"] == [f"calibre-offline:{'c' * 64}:1"]
        assert result.state == BookAuditState.review.value
        assert result.evidence_id is not None


@pytest.mark.asyncio
async def test_claim_honors_cancellation_after_processing(
    engine: Engine,
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    library = tmp_path / "library"
    library.mkdir()
    run_id = _seed_run(engine, library)
    claim = claim_next_certificate_a_run(engine, owner="verifier-a")
    assert claim is not None
    settings = _settings(library)
    settings.verifier = settings.verifier.model_copy(
        update={"scratch_dir": tmp_path / "scratch", "lease_ttl_seconds": 30}
    )

    class EmptyOfflineSource:
        fingerprint = "d" * 64

        def __init__(
            self,
            root: Path,
            *,
            snapshot_root: Path,
            confirm_calibre_stopped: bool,
        ) -> None:
            assert root == library
            assert snapshot_root == tmp_path / "scratch"
            assert confirm_calibre_stopped is True

        def __enter__(self):  # type: ignore[no-untyped-def]
            return self

        def __exit__(self, *_args: object) -> None:
            return None

        @property
        def snapshot_manifest(self) -> dict[str, object]:
            return {"fingerprint": self.fingerprint, "book_count": 0, "books": []}

        def list_books(self) -> list[dict[str, object]]:
            return []

        def assert_unchanged(self, _book_id: int | None = None) -> None:
            return None

    checks = iter((False, False, True))
    monkeypatch.setattr(
        FencedCertificateAStore,
        "cancellation_requested",
        lambda _self: next(checks),
    )

    status = await execute_certificate_a_claim(
        database_engine=engine,
        claim=claim,
        settings=settings,
        evidence_enricher=NullEvidenceEnricher(),
        source_factory=EmptyOfflineSource,
    )

    assert status == "cancelled"
    with Session(engine) as session:
        run = session.exec(select(VerificationRun).where(VerificationRun.run_id == run_id)).one()
        assert run.status == "cancelled"
        assert run.error_code == "cancelled_after_processing"

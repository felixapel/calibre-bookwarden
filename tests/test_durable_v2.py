"""Tests for restart-safe Manifestation V2 verify leases (shipped durable_v2)."""

from __future__ import annotations

from datetime import UTC, datetime, timedelta
from pathlib import Path
from uuid import uuid4

import pytest
from sqlalchemy.engine import Engine
from sqlmodel import Session, SQLModel, create_engine, select

from calibre_ai_auditor.storage.models import VerificationResult, VerificationRun
from calibre_ai_auditor.verification.durable_v2 import (
    claim_verification_run,
    heartbeat_verification_run,
    incomplete_book_keys,
    list_recoverable_v2_run_ids,
    new_worker_id,
    release_verification_run,
    reset_stale_in_flight_states,
)
from calibre_ai_auditor.verification.persistence_v2 import SQLAuditStore
from calibre_ai_auditor.verification.pipeline_v2 import AuditMode, BookAuditState


@pytest.fixture
def engine(tmp_path: Path) -> Engine:
    eng = create_engine(f"sqlite:///{tmp_path / 'durable.db'}", connect_args={"check_same_thread": False})
    SQLModel.metadata.create_all(eng)
    return eng


def _seed_v2_run(engine: Engine, run_id: str, book_keys: list[str]) -> None:
    store = SQLAuditStore(engine, run_id)
    store.start(book_keys=book_keys, mode=AuditMode.shadow.value, use_llm=False)


def test_claim_blocks_second_owner_until_expiry(engine: Engine) -> None:
    run_id = f"verify_{uuid4().hex[:8]}"
    _seed_v2_run(engine, run_id, ["calibre:1", "calibre:2"])
    assert claim_verification_run(engine, run_id, owner="worker-a", ttl_seconds=60) is True
    assert claim_verification_run(engine, run_id, owner="worker-b", ttl_seconds=60) is False
    # Same owner may refresh.
    assert claim_verification_run(engine, run_id, owner="worker-a", ttl_seconds=60) is True
    # Expired lease is reclaimable.
    past = datetime.now(UTC).replace(tzinfo=None) - timedelta(seconds=5)
    with Session(engine) as session:
        run = session.exec(select(VerificationRun).where(VerificationRun.run_id == run_id)).one()
        run.lease_expires_at = past
        session.add(run)
        session.commit()
    assert claim_verification_run(engine, run_id, owner="worker-b", ttl_seconds=60) is True
    with Session(engine) as session:
        run = session.exec(select(VerificationRun).where(VerificationRun.run_id == run_id)).one()
        assert run.lease_owner == "worker-b"


def test_release_allows_reclaim(engine: Engine) -> None:
    run_id = f"verify_{uuid4().hex[:8]}"
    _seed_v2_run(engine, run_id, ["calibre:1"])
    owner = new_worker_id()
    assert claim_verification_run(engine, run_id, owner=owner) is True
    release_verification_run(engine, run_id, owner=owner)
    assert claim_verification_run(engine, run_id, owner="other") is True


def test_heartbeat_requires_owner(engine: Engine) -> None:
    run_id = f"verify_{uuid4().hex[:8]}"
    _seed_v2_run(engine, run_id, ["calibre:1"])
    assert claim_verification_run(engine, run_id, owner="a") is True
    assert heartbeat_verification_run(engine, run_id, owner="b") is False
    assert heartbeat_verification_run(engine, run_id, owner="a") is True


def test_list_recoverable_excludes_live_leases_and_terminals(engine: Engine) -> None:
    live = f"verify_live_{uuid4().hex[:6]}"
    orphan = f"verify_orphan_{uuid4().hex[:6]}"
    done = f"verify_done_{uuid4().hex[:6]}"
    _seed_v2_run(engine, live, ["calibre:1"])
    _seed_v2_run(engine, orphan, ["calibre:2"])
    _seed_v2_run(engine, done, ["calibre:3"])
    claim_verification_run(engine, live, owner="live-worker", ttl_seconds=300)
    # orphan: no lease
    SQLAuditStore(engine, done).finish("completed")
    recoverable = list_recoverable_v2_run_ids(engine)
    assert orphan in recoverable
    assert live not in recoverable
    assert done not in recoverable


def test_incomplete_and_reset_in_flight_drive_resume_membership(engine: Engine) -> None:
    run_id = f"verify_{uuid4().hex[:8]}"
    keys = ["calibre:1", "calibre:2", "calibre:3"]
    _seed_v2_run(engine, run_id, keys)
    # Book 1 sealed as review (complete); book 2 mid-flight; book 3 pending.
    with Session(engine) as session:
        rows = {
            row.book_key: row
            for row in session.exec(select(VerificationResult).where(VerificationResult.run_id == run_id)).all()
        }
        rows["calibre:1"].state = BookAuditState.review.value
        rows["calibre:1"].evidence_id = "evidence_sealed_1"
        rows["calibre:1"].verdict = {
            "schema_version": 2,
            "state": "review",
            "identity": {"tier": "B"},
        }
        rows["calibre:2"].state = BookAuditState.extracting.value
        session.add(rows["calibre:1"])
        session.add(rows["calibre:2"])
        session.commit()

    # Without a valid sealed package, resumable_book_keys stays empty for book 1.
    # incomplete includes all three until seals exist — mid-flight reset to pending.
    reset = reset_stale_in_flight_states(engine, run_id)
    assert reset >= 1
    with Session(engine) as session:
        mid = session.exec(
            select(VerificationResult)
            .where(VerificationResult.run_id == run_id)
            .where(VerificationResult.book_key == "calibre:2")
        ).one()
        assert mid.state == BookAuditState.pending.value
    incomplete = incomplete_book_keys(engine, run_id)
    assert "calibre:2" in incomplete
    assert "calibre:3" in incomplete


def test_claim_rejects_terminal_run(engine: Engine) -> None:
    run_id = f"verify_{uuid4().hex[:8]}"
    _seed_v2_run(engine, run_id, ["calibre:1"])
    SQLAuditStore(engine, run_id).finish("failed")
    assert claim_verification_run(engine, run_id, owner="x") is False


def test_resume_skips_sealed_books_via_resumable_set(engine: Engine) -> None:
    """Shipped resume path: sealed packages become skip_book_keys; incomplete remain."""
    from datetime import UTC as _UTC

    from calibre_ai_auditor.verification.identity_v2 import (
        FormatEvidence,
        FormatEvidenceStatus,
        IdentityTier,
        ManifestationResolution,
    )
    from calibre_ai_auditor.verification.pipeline_v2 import BookSnapshot, EvidencePackageV2

    run_id = "run-resume-1"
    store = SQLAuditStore(engine, run_id)
    store.start(book_keys=["calibre:1", "calibre:2"], mode=AuditMode.shadow.value, use_llm=False)
    snapshot = BookSnapshot(
        book_key="calibre:1",
        calibre_book_id=1,
        current_metadata={"title": "Done"},
        files=["/library/a.epub"],
        snapshot_sha256="0" * 64,
    )
    snapshot = snapshot.model_copy(update={"snapshot_sha256": snapshot.calculated_sha256()})
    package = EvidencePackageV2(
        evidence_id="evidence-resume-1",
        run_id=run_id,
        book_key="calibre:1",
        created_at=datetime.now(_UTC),
        state=BookAuditState.review,
        snapshot=snapshot,
        formats=[
            FormatEvidence(
                path="/library/a.epub",
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
    store.record_package(package)

    assert store.resumable_book_keys() == {"calibre:1"}
    incomplete = incomplete_book_keys(engine, run_id)
    assert incomplete == {"calibre:2"}
    # Crash mid-book 2 then reclaim
    with Session(engine) as session:
        row = session.exec(
            select(VerificationResult)
            .where(VerificationResult.run_id == run_id)
            .where(VerificationResult.book_key == "calibre:2")
        ).one()
        row.state = BookAuditState.extracting.value
        session.add(row)
        session.commit()
    assert reset_stale_in_flight_states(engine, run_id) == 1
    assert claim_verification_run(engine, run_id, owner="recover-1") is True
    assert incomplete_book_keys(engine, run_id) == {"calibre:2"}

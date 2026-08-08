"""PostgreSQL concurrency and fence checks for the Certificate A verifier."""

from __future__ import annotations

import os
from concurrent.futures import ThreadPoolExecutor
from datetime import UTC, datetime, timedelta
from threading import Barrier

import pytest
from sqlmodel import Session, create_engine, select

from calibre_ai_auditor.storage.models import VerificationRun
from calibre_ai_auditor.verification.certificate_a_worker import (
    FencedCertificateAStore,
    LostVerifierLeaseError,
    claim_next_certificate_a_run,
)


@pytest.mark.skipif(not os.environ.get("TEST_POSTGRES_DSN"), reason="TEST_POSTGRES_DSN is not configured")
def test_postgres_claim_is_single_owner_and_stale_fence_cannot_write(
    isolated_postgres_dsn: str,
) -> None:
    engine = create_engine(isolated_postgres_dsn)
    started = datetime.now(UTC).replace(tzinfo=None)
    try:
        with Session(engine) as session:
            session.add(
                VerificationRun(
                    run_id="certificate-a-postgres-fence",
                    status="pending",
                    started_at=started,
                    pipeline_version="manifestation-v2",
                    mode="shadow",
                    contract_version="certificate-a-v1",
                )
            )
            session.commit()

        start = Barrier(2)

        def contend(owner: str):  # type: ignore[no-untyped-def]
            start.wait(timeout=10)
            return claim_next_certificate_a_run(
                engine,
                owner=owner,
                now=started,
                ttl_seconds=3,
            )

        with ThreadPoolExecutor(max_workers=2) as executor:
            claims = list(executor.map(contend, ("verifier-a", "verifier-b")))

        winners = [claim for claim in claims if claim is not None]
        assert len(winners) == 1
        first = winners[0]
        assert first.fence_token == 1

        reclaimed = claim_next_certificate_a_run(
            engine,
            owner="verifier-recovery",
            now=started + timedelta(seconds=4),
            ttl_seconds=30,
        )
        assert reclaimed is not None
        assert reclaimed.run_id == first.run_id
        assert reclaimed.fence_token == 2

        stale_store = FencedCertificateAStore(
            engine,
            first,
            clock=lambda: started + timedelta(seconds=5),
        )
        with pytest.raises(LostVerifierLeaseError):
            stale_store.inventory(
                book_keys=["calibre-offline:fixture:1"],
                source_snapshot={"fingerprint": "a" * 64, "book_count": 1},
            )

        recovery_store = FencedCertificateAStore(
            engine,
            reclaimed,
            clock=lambda: started + timedelta(seconds=5),
        )
        recovery_store.inventory(
            book_keys=["calibre-offline:fixture:1"],
            source_snapshot={"fingerprint": "a" * 64, "book_count": 1},
        )
        with Session(engine) as session:
            run = session.exec(select(VerificationRun)).one()
            assert run.lease_owner == "verifier-recovery"
            assert run.fence_token == 2
            assert run.status == "running"
            assert run.total == 1
    finally:
        engine.dispose()

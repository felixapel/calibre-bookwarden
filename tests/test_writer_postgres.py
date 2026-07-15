"""PostgreSQL-only writer concurrency checks."""

import os

import pytest
from sqlmodel import Session, create_engine

from calibre_ai_auditor.apply.guard import acquire_writer_guard, writer_guard_is_held
from calibre_ai_auditor.apply.writer import claim_next_operation
from calibre_ai_auditor.storage.models import BookRecord
from calibre_ai_auditor.storage.operations import create_operation


@pytest.mark.skipif(not os.environ.get("TEST_POSTGRES_DSN"), reason="TEST_POSTGRES_DSN is not configured")
def test_postgres_allows_only_one_active_operation_per_book(isolated_postgres_dsn: str) -> None:
    engine = create_engine(isolated_postgres_dsn)
    try:
        with Session(engine) as setup:
            setup.add(BookRecord(book_key="calibre:99", run_id="run-99", calibre_book_id=99))
            first = create_operation(
                setup,
                idempotency_key="first",
                operation_type="apply_metadata",
                book_key="calibre:99",
                run_id="run-99",
                requested_patch={"title": "First"},
            )
            second = create_operation(
                setup,
                idempotency_key="second",
                operation_type="apply_metadata",
                book_key="calibre:99",
                run_id="run-99",
                requested_patch={"title": "Second"},
            )
            setup.commit()
            first_id = first.operation_id
            second_id = second.operation_id

        with Session(engine) as worker_one:
            assert claim_next_operation(worker_one, lease_owner="worker-one") == first_id
        with Session(engine) as worker_two:
            assert claim_next_operation(worker_two, lease_owner="worker-two") is None
        assert second_id != first_id
    finally:
        engine.dispose()


@pytest.mark.skipif(not os.environ.get("TEST_POSTGRES_DSN"), reason="TEST_POSTGRES_DSN is not configured")
def test_postgres_writer_guard_fails_closed_when_owner_connection_closes(disposable_postgres_dsn: str) -> None:
    engine = create_engine(disposable_postgres_dsn)
    owner = engine.connect()
    contender = engine.connect()
    try:
        assert acquire_writer_guard(owner)
        assert writer_guard_is_held(owner)
        assert not acquire_writer_guard(contender)
        owner.invalidate()
        owner.close()
        assert acquire_writer_guard(contender)
        assert writer_guard_is_held(contender)
    finally:
        owner.close()
        contender.close()

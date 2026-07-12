"""PostgreSQL-only writer concurrency checks."""

import os

import pytest
from sqlmodel import Session, SQLModel, create_engine

from calibre_ai_auditor.apply.writer import claim_next_operation
from calibre_ai_auditor.storage.models import BookRecord
from calibre_ai_auditor.storage.operations import create_operation


@pytest.mark.skipif(not os.environ.get("TEST_POSTGRES_DSN"), reason="TEST_POSTGRES_DSN is not configured")
def test_postgres_allows_only_one_active_operation_per_book() -> None:
    engine = create_engine(os.environ["TEST_POSTGRES_DSN"])
    SQLModel.metadata.drop_all(engine)
    SQLModel.metadata.create_all(engine)
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
        SQLModel.metadata.drop_all(engine)

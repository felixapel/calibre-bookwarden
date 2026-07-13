"""Real Calibre + PostgreSQL crash-window reconciliation test."""

import hashlib
import os
import shutil
import signal
import subprocess
import sys
from pathlib import Path
from uuid import uuid4

import pytest
from sqlalchemy import create_engine
from sqlalchemy.engine import make_url
from sqlmodel import Session, select

from calibre_ai_auditor.apply.engine import ApplyEngine
from calibre_ai_auditor.apply.writer import reconcile_incomplete_operations
from calibre_ai_auditor.calibre.cli import CalibreCLI
from calibre_ai_auditor.storage.models import BookRecord, Change, OperationLedger, OutboxEvent


@pytest.mark.skipif(
    not os.environ.get("TEST_POSTGRES_DSN") or shutil.which("calibredb") is None,
    reason="TEST_POSTGRES_DSN and calibredb are required",
)
def test_sigkill_after_partial_calibre_write_is_restored_on_restart(tmp_path: Path, monkeypatch) -> None:
    dsn = os.environ["TEST_POSTGRES_DSN"]
    if "test" not in (make_url(dsn).database or "").lower():
        pytest.fail("TEST_POSTGRES_DSN must name an unmistakably disposable test database")
    # Debian's Calibre must use its system Python modules, not the app venv.
    monkeypatch.delenv("PYTHONPATH", raising=False)
    library = tmp_path / "library"
    library.mkdir()
    subprocess.run(
        [
            "calibredb",
            "add",
            "--empty",
            "--title",
            "Original Title",
            "--authors",
            "Crash Test",
            "--with-library",
            str(library),
        ],
        check=True,
        capture_output=True,
        text=True,
    )
    cli = CalibreCLI(library)
    backup = tmp_path / "before.opf"
    partial = tmp_path / "partial.opf"
    cli.export_opf(1, backup)
    ApplyEngine(cli, tmp_path / "artifacts")._build_target_opf(backup, partial, {"title": "Partial Title"})

    suffix = uuid4().hex
    operation_id = f"crash-{suffix}"
    book_key = f"calibre-crash:{suffix}"
    engine = create_engine(dsn)
    with Session(engine) as session:
        session.add(
            BookRecord(
                book_key=book_key,
                run_id=f"run-{suffix}",
                calibre_book_id=1,
                source="calibre",
                current_metadata={"title": "Original Title"},
                status="suggest_fix",
            )
        )
        session.add(
            OperationLedger(
                operation_id=operation_id,
                idempotency_key=f"crash:{suffix}",
                operation_type="apply_metadata",
                book_key=book_key,
                run_id=f"run-{suffix}",
                calibre_book_id=1,
                state="writing",
                requested_patch={"title": "Target Title"},
                before_metadata={"title": "Original Title"},
                target_metadata={"title": "Target Title"},
            )
        )
        session.add(
            OutboxEvent(
                event_id=f"event-{suffix}",
                aggregate_id=operation_id,
                event_type="operation.requested",
                status="processing",
            )
        )
        session.add(
            Change(
                operation_id=operation_id,
                book_key=book_key,
                run_id=f"run-{suffix}",
                before_metadata={"title": "Original Title"},
                after_metadata={"title": "Target Title"},
                backup_opf_path=str(backup),
                backup_opf_sha256=hashlib.sha256(backup.read_bytes()).hexdigest(),
                status="pending_apply",
            )
        )
        session.commit()

    child = subprocess.run(
        [
            sys.executable,
            "-c",
            (
                "import os,signal,subprocess,sys;"
                "subprocess.run(sys.argv[1:],check=True);"
                "os.kill(os.getpid(),signal.SIGKILL)"
            ),
            "calibredb",
            "set_metadata",
            "1",
            str(partial),
            "--with-library",
            str(library),
        ],
        check=False,
    )
    assert child.returncode == -signal.SIGKILL
    assert cli.show_metadata(1)["title"] == "Partial Title"

    try:
        with Session(engine) as session:
            assert reconcile_incomplete_operations(session, cli) == [operation_id]
            operation = session.exec(select(OperationLedger).where(OperationLedger.operation_id == operation_id)).one()
            change = session.exec(select(Change).where(Change.operation_id == operation_id)).one()
            event = session.exec(select(OutboxEvent).where(OutboxEvent.aggregate_id == operation_id)).one()
            assert operation.state == "restored"
            assert change.status == "failed_rolled_back"
            assert event.status == "failed"
            assert cli.show_metadata(1)["title"] == "Original Title"
    finally:
        with engine.begin() as connection:
            connection.exec_driver_sql('DELETE FROM "change" WHERE operation_id = %s', (operation_id,))
            connection.exec_driver_sql("DELETE FROM outboxevent WHERE aggregate_id = %s", (operation_id,))
            connection.exec_driver_sql("DELETE FROM operationledger WHERE operation_id = %s", (operation_id,))
            connection.exec_driver_sql("DELETE FROM bookrecord WHERE book_key = %s", (book_key,))
        engine.dispose()

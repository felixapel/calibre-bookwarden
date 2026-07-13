"""Production retention gate against real PostgreSQL and Valkey services."""

import hashlib
import json
import os
from datetime import UTC, datetime, timedelta
from pathlib import Path
from uuid import uuid4

import pytest
import redis
from sqlalchemy import text
from sqlalchemy.engine import make_url
from sqlmodel import Session, create_engine
from typer.testing import CliRunner

from calibre_ai_auditor.apply.guard import WRITER_ADVISORY_LOCK_ID
from calibre_ai_auditor.apply.heartbeat import WRITER_HEARTBEAT_KEY
from calibre_ai_auditor.cli.main import app
from calibre_ai_auditor.storage.models import OperationLedger

runner = CliRunner()


@pytest.mark.skipif(
    not os.environ.get("TEST_POSTGRES_DSN") or not os.environ.get("TEST_VALKEY_URL"),
    reason="TEST_POSTGRES_DSN and TEST_VALKEY_URL are required",
)
def test_production_retention_fails_closed_then_deletes_with_real_services(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch
) -> None:
    dsn = os.environ["TEST_POSTGRES_DSN"]
    valkey_url = os.environ["TEST_VALKEY_URL"]
    database = make_url(dsn).database or ""
    if "test" not in database.lower():
        pytest.fail("TEST_POSTGRES_DSN must name an unmistakably disposable test database")
    if make_url(valkey_url).database not in {"0", "1", None}:
        pytest.fail("TEST_VALKEY_URL must use a disposable test database")

    artifacts = tmp_path / "artifacts"
    restore_point = artifacts / "restore" / "run-old" / "calibre-1"
    restore_point.mkdir(parents=True)
    (restore_point / "restore.json").write_text(
        json.dumps(
            {
                "run_id": "run-old",
                "book_key": "calibre:1",
                "applied_at": (datetime.now(UTC) - timedelta(days=31)).isoformat(),
                "ttl_seconds": 30 * 24 * 3600,
            }
        )
    )
    database_dump = tmp_path / "database.dump"
    archive = tmp_path / "artifacts.tar"
    database_dump.write_bytes(b"database backup")
    archive.write_bytes(b"artifact backup")
    backup_manifest = tmp_path / "backup.json"
    backup_manifest.write_text(
        json.dumps(
            {
                "created_at": datetime.now(UTC).isoformat(),
                "database_dump": database_dump.name,
                "database_sha256": hashlib.sha256(database_dump.read_bytes()).hexdigest(),
                "artifacts_archive": archive.name,
                "artifacts_sha256": hashlib.sha256(archive.read_bytes()).hexdigest(),
            }
        )
    )
    config = tmp_path / "config.yml"
    config.write_text(
        "\n".join(
            (
                "profile: production",
                "database:",
                "  backend: postgres",
                f"  postgres_dsn: {dsn}",
                "queue:",
                "  backend: valkey",
                f"  valkey_url: {valkey_url}",
                "  connect_timeout_seconds: 2",
                "storage:",
                f"  artifacts_dir: {artifacts}",
            )
        )
        + "\n"
    )
    command = [
        "-c",
        str(config),
        "retention",
        "--execute",
        "--backup-reference",
        str(backup_manifest),
    ]
    engine = create_engine(dsn)
    valkey = redis.from_url(valkey_url, decode_responses=True)
    operation_id = f"retention-gate-{uuid4().hex}"
    monkeypatch.setattr("calibre_ai_auditor.cli.main.get_engine", lambda _settings: engine)
    valkey.delete(WRITER_HEARTBEAT_KEY)
    try:
        with engine.connect() as lock_owner:
            lock_owner.execute(
                text("SELECT pg_advisory_lock(:lock_id)"),
                {"lock_id": WRITER_ADVISORY_LOCK_ID},
            )
            try:
                locked = runner.invoke(app, command)
                assert locked.exit_code == 1
                assert "Metadata writer is active" in locked.stdout
                assert restore_point.exists()
            finally:
                lock_owner.execute(
                    text("SELECT pg_advisory_unlock(:lock_id)"),
                    {"lock_id": WRITER_ADVISORY_LOCK_ID},
                )

        valkey.set(
            WRITER_HEARTBEAT_KEY,
            json.dumps({"owner": "integration-writer", "timestamp": datetime.now(UTC).isoformat()}),
            ex=60,
        )
        heartbeat = runner.invoke(app, command)
        assert heartbeat.exit_code == 1
        assert "Writer heartbeat or non-terminal operations remain" in heartbeat.stdout
        assert restore_point.exists()
        valkey.delete(WRITER_HEARTBEAT_KEY)

        with Session(engine) as session:
            session.add(
                OperationLedger(
                    operation_id=operation_id,
                    idempotency_key=operation_id,
                    operation_type="apply_metadata",
                    book_key="calibre:1",
                    state="requested",
                )
            )
            session.commit()
        nonterminal = runner.invoke(app, command)
        assert nonterminal.exit_code == 1
        assert "Writer heartbeat or non-terminal operations remain" in nonterminal.stdout
        assert restore_point.exists()

        with engine.begin() as connection:
            connection.execute(
                text("DELETE FROM operationledger WHERE operation_id = :operation_id"),
                {"operation_id": operation_id},
            )
        clean = runner.invoke(app, command)
        assert clean.exit_code == 0
        assert "Deleted 1 expired restore points" in clean.stdout
        assert not restore_point.exists()
    finally:
        valkey.delete(WRITER_HEARTBEAT_KEY)
        valkey.close()
        with engine.begin() as connection:
            connection.execute(
                text("DELETE FROM operationledger WHERE operation_id = :operation_id"),
                {"operation_id": operation_id},
            )
        engine.dispose()

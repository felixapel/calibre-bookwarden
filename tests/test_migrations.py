from importlib import import_module
from pathlib import Path

import pytest
from alembic import command
from alembic.config import Config
from sqlalchemy import create_engine, inspect, text


def _config(database_path: Path) -> Config:
    config = Config("alembic.ini")
    config.set_main_option("sqlalchemy.url", f"sqlite:///{database_path}")
    return config


def test_clean_database_upgrades_and_downgrades(tmp_path: Path) -> None:
    database_path = tmp_path / "migration.db"
    config = _config(database_path)

    command.upgrade(config, "head")

    tables = set(inspect(create_engine(f"sqlite:///{database_path}")).get_table_names())
    assert "alembic_version" in tables
    assert "bookrecord" in tables
    assert "operationledger" in tables
    assert "outboxevent" in tables
    assert "verificationrun" in tables
    assert "verificationresult" in tables
    assert "manualauthorization" in tables
    assert "pilotsession" in tables
    assert "operationincidentacknowledgement" in tables
    inspector = inspect(create_engine(f"sqlite:///{database_path}"))
    assert {column["name"] for column in inspector.get_columns("evidencepackage")} >= {
        "schema_version",
        "observations",
    }
    assert {column["name"] for column in inspector.get_columns("verificationrun")} >= {
        "pipeline_version",
        "mode",
    }
    assert {column["name"] for column in inspector.get_columns("verificationresult")} >= {
        "evidence_id",
        "state",
    }
    assert {column["name"] for column in inspector.get_columns("change")} >= {
        "backup_cover_path",
        "backup_cover_sha256",
        "before_custom",
    }
    assert {column["name"] for column in inspector.get_columns("operationledger")} >= {
        "evidence_id",
        "pilot_id",
        "rollback_cover_path",
        "rollback_cover_sha256",
        "rollback_custom",
    }
    assert {
        constraint["name"] for constraint in inspector.get_check_constraints("operationincidentacknowledgement")
    } >= {
        "ck_incident_ack_actor_length",
        "ck_incident_ack_reason_length",
    }
    assert {foreign_key["name"] for foreign_key in inspector.get_foreign_keys("operationincidentacknowledgement")} == {
        "fk_incident_ack_operation"
    }

    command.downgrade(config, "base")

    remaining = set(inspect(create_engine(f"sqlite:///{database_path}")).get_table_names())
    assert remaining == {"alembic_version"}


def test_v2_migration_preserves_legacy_verification_rows(tmp_path: Path) -> None:
    database_path = tmp_path / "legacy.db"
    config = _config(database_path)
    command.upgrade(config, "b18f4c2d7a90")
    engine = create_engine(f"sqlite:///{database_path}")
    with engine.begin() as connection:
        connection.execute(
            text(
                """
                INSERT INTO verificationrun
                  (run_id, status, started_at, total, completed, counts, use_llm)
                VALUES
                  ('legacy-run', 'completed', '2026-07-14 00:00:00', 1, 1, '{}', 0)
                """
            )
        )

    command.upgrade(config, "head")

    with engine.connect() as connection:
        row = connection.execute(
            text("SELECT pipeline_version, mode FROM verificationrun WHERE run_id='legacy-run'")
        ).one()
    assert row.pipeline_version == "v1"
    assert row.mode == "legacy"


def test_downgrade_refuses_to_drop_persisted_v2_audit_evidence(tmp_path: Path) -> None:
    database_path = tmp_path / "v2-active.db"
    config = _config(database_path)
    command.upgrade(config, "head")
    engine = create_engine(f"sqlite:///{database_path}")
    with engine.begin() as connection:
        connection.execute(
            text(
                """
                INSERT INTO verificationrun
                  (run_id, status, started_at, finished_at, total, completed, counts, use_llm,
                   pipeline_version, mode)
                VALUES
                  ('v2-run', 'completed', '2026-07-14 00:00:00', '2026-07-14 00:01:00',
                   0, 0, '{}', 0, 'manifestation-v2', 'shadow')
                """
            )
        )

    with pytest.raises(RuntimeError, match="persisted Manifestation V2"):
        command.downgrade(config, "base")


@pytest.mark.parametrize(
    ("pipeline_version", "mode", "status", "finished_at", "lease_owner", "lease_expires_at"),
    [
        ("manifestation-v2", "shadow", "running", None, None, None),
        ("manifestation-v2", "shadow", "completed", None, None, None),
        ("manifestation-v2", "shadow", "unexpected", "2026-07-17 12:00:00", None, None),
        (
            "manifestation-v2",
            "shadow",
            "completed",
            "2026-07-17 12:00:00",
            "stale-worker",
            "2026-07-17 10:00:00",
        ),
        ("v1", "legacy", "completed", "2026-07-17 12:00:00", "unexpected-v1-worker", None),
        ("v1", "legacy", "completed", "2026-07-17 12:00:00", None, "2026-07-17 10:00:00"),
    ],
)
def test_lease_downgrade_refuses_unsafe_run_state(
    tmp_path: Path,
    pipeline_version: str,
    mode: str,
    status: str,
    finished_at: str | None,
    lease_owner: str | None,
    lease_expires_at: str | None,
) -> None:
    database_path = tmp_path / "v2-lease-active.db"
    config = _config(database_path)
    command.upgrade(config, "head")
    engine = create_engine(f"sqlite:///{database_path}")
    with engine.begin() as connection:
        connection.execute(
            text(
                """
                INSERT INTO verificationrun
                  (run_id, status, started_at, finished_at, total, completed, counts, use_llm,
                   pipeline_version, mode, lease_owner, lease_expires_at)
                VALUES
                  ('v2-lease-run', :status, '2026-07-17 11:00:00', :finished_at, 1, 0, '{}', 0,
                   :pipeline_version, :mode, :lease_owner, :lease_expires_at)
                """
            ),
            {
                "status": status,
                "finished_at": finished_at,
                "pipeline_version": pipeline_version,
                "mode": mode,
                "lease_owner": lease_owner,
                "lease_expires_at": lease_expires_at,
            },
        )

    with pytest.raises(RuntimeError, match="verification run lease state"):
        command.downgrade(config, "a72c9d4e8f31")

    inspector = inspect(engine)
    columns = {column["name"] for column in inspector.get_columns("verificationrun")}
    assert {"lease_owner", "lease_expires_at"} <= columns
    indexes = {index["name"] for index in inspector.get_indexes("verificationrun")}
    assert {"ix_verificationrun_lease_owner", "ix_verificationrun_lease_expires_at"} <= indexes
    with engine.connect() as connection:
        assert connection.execute(text("SELECT version_num FROM alembic_version")).scalar_one() == "c8e1f0a2b4d6"
        row = connection.execute(
            text(
                "SELECT pipeline_version, status, finished_at, lease_owner, lease_expires_at "
                "FROM verificationrun WHERE run_id = 'v2-lease-run'"
            )
        ).one()
    assert row.pipeline_version == pipeline_version
    assert row.status == status
    assert (str(row.finished_at) if row.finished_at is not None else None) == finished_at
    assert row.lease_owner == lease_owner
    assert (str(row.lease_expires_at) if row.lease_expires_at is not None else None) == lease_expires_at


def test_postgres_lease_downgrade_locks_before_checking_preconditions(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    migration = import_module("migrations.versions.c8e1f0a2b4d6_add_verification_run_lease")
    statements: list[str] = []

    class FakeDialect:
        name = "postgresql"

    class FakeResult:
        def first(self) -> object:
            return object()

    class FakeBind:
        dialect = FakeDialect()

        def execute(self, statement: object) -> FakeResult:
            statements.append(str(statement).strip())
            return FakeResult()

    monkeypatch.setattr(migration.op, "get_bind", FakeBind)

    with pytest.raises(RuntimeError, match="verification run lease state"):
        migration.downgrade()

    assert statements[0] == "LOCK TABLE verificationrun IN ACCESS EXCLUSIVE MODE NOWAIT"
    assert statements[1].startswith("SELECT 1")


def test_lease_downgrade_allows_terminal_unleased_v2_runs(tmp_path: Path) -> None:
    database_path = tmp_path / "v2-lease-terminal.db"
    config = _config(database_path)
    command.upgrade(config, "head")
    engine = create_engine(f"sqlite:///{database_path}")
    with engine.begin() as connection:
        connection.execute(
            text(
                """
                INSERT INTO verificationrun
                  (run_id, status, started_at, finished_at, total, completed, counts, use_llm,
                   pipeline_version, mode, lease_owner, lease_expires_at)
                VALUES
                  ('v2-terminal-run', 'completed', '2026-07-17 11:00:00', '2026-07-17 12:00:00',
                   0, 0, '{}', 0, 'manifestation-v2', 'shadow', NULL, NULL)
                """
            )
        )

    command.downgrade(config, "a72c9d4e8f31")

    columns = {column["name"] for column in inspect(engine).get_columns("verificationrun")}
    assert "lease_owner" not in columns
    assert "lease_expires_at" not in columns
    with engine.connect() as connection:
        row = connection.execute(
            text("SELECT pipeline_version, status FROM verificationrun WHERE run_id = 'v2-terminal-run'")
        ).one()
    assert row.pipeline_version == "manifestation-v2"
    assert row.status == "completed"

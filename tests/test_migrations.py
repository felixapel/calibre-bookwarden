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
        "rollback_cover_path",
        "rollback_cover_sha256",
        "rollback_custom",
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
                  (run_id, status, started_at, total, completed, counts, use_llm, pipeline_version, mode)
                VALUES
                  ('v2-run', 'completed', '2026-07-14 00:00:00', 0, 0, '{}', 0,
                   'manifestation-v2', 'shadow')
                """
            )
        )

    with pytest.raises(RuntimeError, match="persisted Manifestation V2"):
        command.downgrade(config, "base")

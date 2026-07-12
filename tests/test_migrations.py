from pathlib import Path

from alembic import command
from alembic.config import Config
from sqlalchemy import create_engine, inspect


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

    command.downgrade(config, "base")

    remaining = set(inspect(create_engine(f"sqlite:///{database_path}")).get_table_names())
    assert remaining == {"alembic_version"}

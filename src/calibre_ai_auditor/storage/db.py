import os
from pathlib import Path

from alembic import command
from alembic.config import Config
from alembic.script import ScriptDirectory
from sqlalchemy.engine import Engine
from sqlmodel import create_engine

from calibre_ai_auditor.config.settings import Settings

_engines: dict[str, Engine] = {}


def _settings_dsn(settings: Settings) -> tuple[str, dict[str, object]]:
    if settings.database.backend == "postgres":
        if not settings.database.postgres_dsn:
            raise ValueError("BOOKAUDIT_DATABASE__POSTGRES_DSN is required for the postgres backend")
        return settings.database.postgres_dsn, {}
    # Default to SQLite
    sqlite_url = f"sqlite:///{settings.storage.sqlite_path}"
    # Ensure parent directory exists
    settings.storage.sqlite_path.parent.mkdir(parents=True, exist_ok=True)
    # The API serves threaded requests; the default SQLite driver guard
    # (check_same_thread) would raise under concurrency.
    return sqlite_url, {"check_same_thread": False}


def get_engine(settings: Settings) -> Engine:
    """Engine cached per DSN so settings changes (e.g. tests) never get a stale engine."""
    dsn, connect_args = _settings_dsn(settings)
    engine = _engines.get(dsn)
    if engine is None:
        engine = create_engine(dsn, connect_args=connect_args)
        _engines[dsn] = engine
    return engine


def dispose_engines() -> None:
    """Release all cached engines (test isolation, shutdown hygiene)."""
    while _engines:
        _, engine = _engines.popitem()
        engine.dispose()


def init_db(settings: Settings) -> None:
    """Upgrade the configured database to the repository's Alembic head."""
    engine = get_engine(settings)
    repository_root = Path(os.environ.get("BOOKAUDIT_REPOSITORY_ROOT", Path(__file__).resolve().parents[3]))
    config = Config(repository_root / "alembic.ini")
    config.set_main_option("script_location", str(repository_root / "migrations"))
    config.set_main_option("sqlalchemy.url", engine.url.render_as_string(hide_password=False).replace("%", "%%"))
    command.upgrade(config, "head")


def expected_schema_revision() -> str:
    repository_root = Path(os.environ.get("BOOKAUDIT_REPOSITORY_ROOT", Path(__file__).resolve().parents[3]))
    config = Config(repository_root / "alembic.ini")
    config.set_main_option("script_location", str(repository_root / "migrations"))
    head = ScriptDirectory.from_config(config).get_current_head()
    if head is None:
        raise RuntimeError("Alembic repository has no schema head")
    return head

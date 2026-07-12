import os
from pathlib import Path

from alembic import command
from alembic.config import Config
from sqlalchemy.engine import Engine
from sqlmodel import create_engine

from calibre_ai_auditor.config.settings import Settings

_engine: Engine | None = None


def get_engine(settings: Settings) -> Engine:
    global _engine
    if _engine is not None:
        return _engine

    if settings.database.backend == "postgres":
        if not settings.database.postgres_dsn:
            raise ValueError("BOOKAUDIT_DATABASE__POSTGRES_DSN is required for the postgres backend")
        _engine = create_engine(settings.database.postgres_dsn)
    else:
        # Default to SQLite
        sqlite_url = f"sqlite:///{settings.storage.sqlite_path}"
        # Ensure parent directory exists
        settings.storage.sqlite_path.parent.mkdir(parents=True, exist_ok=True)
        _engine = create_engine(sqlite_url)
    return _engine


def init_db(settings: Settings) -> None:
    """Upgrade the configured database to the repository's Alembic head."""
    engine = get_engine(settings)
    repository_root = Path(os.environ.get("BOOKAUDIT_REPOSITORY_ROOT", Path(__file__).resolve().parents[3]))
    config = Config(repository_root / "alembic.ini")
    config.set_main_option("script_location", str(repository_root / "migrations"))
    config.set_main_option("sqlalchemy.url", engine.url.render_as_string(hide_password=False).replace("%", "%%"))
    command.upgrade(config, "head")

from sqlalchemy.engine import Engine
from sqlmodel import SQLModel, create_engine

from calibre_ai_auditor.config.settings import Settings

_engine: Engine | None = None


def get_engine(settings: Settings) -> Engine:
    global _engine
    if _engine is not None:
        return _engine

    if settings.database.backend == "postgres":
        _engine = create_engine(settings.database.postgres_dsn)
    else:
        # Default to SQLite
        sqlite_url = f"sqlite:///{settings.storage.sqlite_path}"
        # Ensure parent directory exists
        settings.storage.sqlite_path.parent.mkdir(parents=True, exist_ok=True)
        _engine = create_engine(sqlite_url)
    return _engine



def init_db(settings: Settings) -> None:
    engine = get_engine(settings)
    SQLModel.metadata.create_all(engine)

    # Perform runtime migrations for added columns
    from sqlalchemy import inspect, text
    inspector = inspect(engine)
    if "bookrecord" in inspector.get_table_names():
        columns = [c["name"] for c in inspector.get_columns("bookrecord")]
        if "paperless_document_id" not in columns:
            with engine.begin() as conn:
                conn.execute(text("ALTER TABLE bookrecord ADD COLUMN paperless_document_id INTEGER"))
        if "field_locks" not in columns:
            with engine.begin() as conn:
                conn.execute(text("ALTER TABLE bookrecord ADD COLUMN field_locks JSON"))

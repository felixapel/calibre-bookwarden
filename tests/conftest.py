import os
from collections.abc import Iterator
from uuid import uuid4

import pytest
from sqlalchemy.engine import make_url
from sqlmodel import SQLModel, create_engine

import calibre_ai_auditor.storage.models  # noqa: F401


@pytest.fixture(autouse=True)
def isolate_bookaudit_env(monkeypatch: pytest.MonkeyPatch) -> Iterator[None]:
    """Prevent developer .env / Docker paths from breaking unit tests."""
    preserved: dict[str, str | None] = {}
    for key in list(os.environ):
        if key.startswith("BOOKAUDIT_"):
            preserved[key] = os.environ.pop(key)
    monkeypatch.setenv("BOOKAUDIT_DATABASE__BACKEND", "sqlite")

    import calibre_ai_auditor.storage.db as db_module

    db_module._engine = None
    yield
    db_module._engine = None
    for key, value in preserved.items():
        if value is None:
            os.environ.pop(key, None)
        else:
            os.environ[key] = value


@pytest.fixture
def disposable_postgres_dsn() -> str:
    """Return a PostgreSQL DSN only when it clearly names a test database."""
    dsn = os.environ.get("TEST_POSTGRES_DSN")
    if not dsn:
        pytest.skip("TEST_POSTGRES_DSN is not configured")

    base_url = make_url(dsn)
    database = (base_url.database or "").lower()
    if not (database.startswith("test_") or database.endswith("_test")):
        pytest.fail("TEST_POSTGRES_DSN must name an unmistakably disposable test database")
    return dsn


@pytest.fixture
def isolated_postgres_dsn(disposable_postgres_dsn: str) -> Iterator[str]:
    """Provide a private PostgreSQL schema without mutating the shared test schema."""
    base_url = make_url(disposable_postgres_dsn)

    schema = f"pytest_{uuid4().hex}"
    admin_engine = create_engine(base_url)
    isolated_url = base_url.update_query_dict({"options": f"-csearch_path={schema}"})
    isolated_engine = create_engine(isolated_url)

    try:
        with admin_engine.begin() as connection:
            connection.exec_driver_sql(f'CREATE SCHEMA "{schema}"')
        SQLModel.metadata.create_all(isolated_engine)
        yield isolated_url.render_as_string(hide_password=False)
    finally:
        isolated_engine.dispose()
        try:
            with admin_engine.begin() as connection:
                connection.exec_driver_sql(f'DROP SCHEMA IF EXISTS "{schema}" CASCADE')
        finally:
            admin_engine.dispose()

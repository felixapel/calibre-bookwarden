"""PostgreSQL abuse checks for the app/writer privilege boundary."""

import os

import pytest
from alembic import command
from alembic.config import Config
from sqlalchemy import create_engine, text
from sqlalchemy.engine import make_url
from sqlalchemy.exc import ProgrammingError


@pytest.mark.skipif(not os.environ.get("TEST_POSTGRES_DSN"), reason="TEST_POSTGRES_DSN is not configured")
def test_runtime_roles_cannot_mutate_each_others_security_tables() -> None:
    dsn = os.environ["TEST_POSTGRES_DSN"]
    url = make_url(dsn)
    if "test" not in (url.database or "").lower():
        pytest.fail("TEST_POSTGRES_DSN must name an unmistakably disposable test database")

    admin = create_engine(url)
    app_password = "acl-app-test-password"
    writer_password = "acl-writer-test-password"
    with admin.begin() as connection:
        for role in ("bookaudit_app", "bookaudit_writer"):
            connection.execute(text(f"DROP OWNED BY {role}") if _role_exists(connection, role) else text("SELECT 1"))
            connection.execute(text(f"DROP ROLE IF EXISTS {role}"))
        connection.execute(text(f"CREATE ROLE bookaudit_app LOGIN PASSWORD '{app_password}'"))
        connection.execute(text(f"CREATE ROLE bookaudit_writer LOGIN PASSWORD '{writer_password}'"))
        database_identifier = (url.database or "").replace('"', '""')
        connection.execute(
            text(f'GRANT CONNECT ON DATABASE "{database_identifier}" TO bookaudit_app, bookaudit_writer')
        )
        connection.execute(text("GRANT USAGE ON SCHEMA public TO bookaudit_app, bookaudit_writer"))

    config = Config("alembic.ini")
    config.set_main_option("sqlalchemy.url", url.render_as_string(hide_password=False).replace("%", "%%"))
    command.downgrade(config, "6a3f83d9e621")
    command.upgrade(config, "head")

    app = create_engine(url.set(username="bookaudit_app", password=app_password))
    writer = create_engine(url.set(username="bookaudit_writer", password=writer_password))
    try:
        with app.connect() as connection:
            connection.execute(text("SELECT count(*) FROM operationledger"))
            assert connection.execute(
                text("SELECT has_table_privilege(current_user, 'pilotsession', 'SELECT,INSERT,UPDATE')")
            ).scalar_one()
            assert not connection.execute(
                text("SELECT has_table_privilege(current_user, 'pilotsession', 'DELETE')")
            ).scalar_one()
            assert connection.execute(
                text("SELECT has_table_privilege(current_user, 'operationincidentacknowledgement', 'SELECT,INSERT')")
            ).scalar_one()
            assert not connection.execute(
                text("SELECT has_table_privilege(current_user, 'operationincidentacknowledgement', 'UPDATE,DELETE')")
            ).scalar_one()
        with writer.connect() as connection:
            connection.execute(text("SELECT count(*) FROM manualauthorization"))
            assert connection.execute(
                text("SELECT has_table_privilege(current_user, 'pilotsession', 'SELECT')")
            ).scalar_one()
            assert not connection.execute(
                text("SELECT has_table_privilege(current_user, 'pilotsession', 'INSERT,UPDATE,DELETE')")
            ).scalar_one()
            assert connection.execute(
                text("SELECT has_table_privilege(current_user, 'operationincidentacknowledgement', 'SELECT')")
            ).scalar_one()
            assert not connection.execute(
                text(
                    "SELECT has_table_privilege("
                    "current_user, 'operationincidentacknowledgement', 'INSERT,UPDATE,DELETE')"
                )
            ).scalar_one()

        with pytest.raises(ProgrammingError) as denied_update, app.begin() as connection:
            connection.execute(text("UPDATE operationledger SET state = state WHERE false"))
        assert denied_update.value.orig.sqlstate == "42501"
        with pytest.raises(ProgrammingError), app.begin() as connection:
            connection.execute(
                text("INSERT INTO \"change\" (book_key, run_id, backup_opf_path) VALUES ('x', 'x', 'x')")
            )
        with pytest.raises(ProgrammingError), writer.begin() as connection:
            connection.execute(
                text(
                    "INSERT INTO manualauthorization "
                    "(authorization_id, book_key, action, created_at) "
                    "VALUES ('forged', 'x', 'approve', now())"
                )
            )
    finally:
        app.dispose()
        writer.dispose()
        with admin.begin() as connection:
            connection.execute(text("DROP OWNED BY bookaudit_app, bookaudit_writer"))
            connection.execute(text("DROP ROLE bookaudit_app, bookaudit_writer"))
        admin.dispose()


def _role_exists(connection, role: str) -> bool:
    return bool(
        connection.execute(
            text("SELECT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = :role)"),
            {"role": role},
        ).scalar_one()
    )

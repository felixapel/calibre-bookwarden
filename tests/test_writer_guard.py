from unittest.mock import MagicMock

from calibre_ai_auditor.apply.guard import acquire_writer_guard, release_writer_guard, writer_guard_is_held


def test_acquire_writer_guard_uses_session_lock() -> None:
    connection = MagicMock()
    connection.execute.return_value.scalar_one.return_value = True

    assert acquire_writer_guard(connection) is True
    statement = str(connection.execute.call_args.args[0])
    assert "pg_try_advisory_lock" in statement


def test_writer_guard_check_is_read_only_and_fails_when_session_lost() -> None:
    connection = MagicMock()
    connection.execute.return_value.scalar_one.return_value = False

    assert writer_guard_is_held(connection) is False
    statement = str(connection.execute.call_args.args[0])
    assert "pg_locks" in statement
    assert "pg_try_advisory_lock" not in statement


def test_release_writer_guard_unlocks_the_owner_session() -> None:
    connection = MagicMock()
    connection.execute.return_value.scalar_one.return_value = True

    assert release_writer_guard(connection) is True
    assert "pg_advisory_unlock" in str(connection.execute.call_args.args[0])

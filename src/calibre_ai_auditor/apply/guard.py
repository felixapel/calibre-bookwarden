"""PostgreSQL session guard for the sole external Calibre writer."""

from typing import Any

from sqlalchemy import text

WRITER_ADVISORY_LOCK_ID = 1_129_270_868


def acquire_writer_guard(connection: Any) -> bool:
    return bool(
        connection.execute(
            text("SELECT pg_try_advisory_lock(:lock_id)"),
            {"lock_id": WRITER_ADVISORY_LOCK_ID},
        ).scalar_one()
    )


def writer_guard_is_held(connection: Any) -> bool:
    """Check ownership without reacquiring a lock that PostgreSQL released."""
    return bool(
        connection.execute(
            text(
                "SELECT EXISTS ("
                "SELECT 1 FROM pg_locks "
                "WHERE locktype = 'advisory' AND pid = pg_backend_pid() "
                "AND classid = 0 AND objid = :lock_id AND objsubid = 1 AND granted"
                ")"
            ),
            {"lock_id": WRITER_ADVISORY_LOCK_ID},
        ).scalar_one()
    )


def release_writer_guard(connection: Any) -> bool:
    return bool(
        connection.execute(
            text("SELECT pg_advisory_unlock(:lock_id)"),
            {"lock_id": WRITER_ADVISORY_LOCK_ID},
        ).scalar_one()
    )


__all__ = [
    "WRITER_ADVISORY_LOCK_ID",
    "acquire_writer_guard",
    "release_writer_guard",
    "writer_guard_is_held",
]

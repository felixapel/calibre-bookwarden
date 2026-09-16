"""Deprecated ledger facade. Persistence and rollback operations are disabled.

Use the existing supervised writer and sealed artifact contract.
"""

from __future__ import annotations

import logging
import sqlite3
from collections.abc import Iterator
from contextlib import contextmanager
from dataclasses import dataclass
from pathlib import Path
from typing import Any

logger = logging.getLogger(__name__)


@dataclass
class RestorePoint:
    change_id: str
    book_id: int
    created_at: str
    operation: str
    previous_state: dict[str, Any]
    new_state: dict[str, Any]
    backup_dir: str
    sha256_seal: str


class CryptographicLedger:
    """Deprecated compatibility facade for an unsafe parallel mutation path.

    Persistent changes must go through the supervised Manifestation V2 apply
    writer.  This class remains importable for old callers, but it never
    creates a schema, restore directory, or ledger database.
    """

    def __init__(self, ledger_db_path: Path | str, restore_base_dir: Path | str | None = None):
        self.ledger_db_path = Path(ledger_db_path)
        self.restore_base_dir = (
            Path(restore_base_dir) if restore_base_dir else self.ledger_db_path.parent / "restore_points"
        )
        # Deliberately no runtime initialization: schema changes belong to Alembic.

    @contextmanager
    def connect(self) -> Iterator[sqlite3.Connection]:
        """Reject legacy database access before SQLite can create a file."""
        raise PermissionError(
            "CryptographicLedger is retired; use the supervised Manifestation V2 apply writer instead."
        )

    def _init_ledger(self) -> None:
        """Reject runtime schema creation from the retired ledger."""
        raise PermissionError(
            "CryptographicLedger is retired; use the supervised Manifestation V2 apply writer instead."
        )

    @staticmethod
    def verify_database_integrity(conn: sqlite3.Connection) -> bool:
        """Executes pre/post flight integrity barrier on Calibre metadata.db."""
        try:
            cur = conn.execute("PRAGMA integrity_check;")
            row = cur.fetchone()
            if not row or row[0] != "ok":
                logger.error(f"Integrity check failed: {row}")
                return False

            fk_cur = conn.execute("PRAGMA foreign_key_check;")
            fk_violations = fk_cur.fetchall()
            if fk_violations:
                logger.warning(f"Foreign key violations found: {len(fk_violations)}")
                return False
            return True
        except Exception as e:
            logger.error(f"Error checking database integrity: {e}")
            return False

    def create_restore_point(
        self,
        book_id: int,
        operation: str,
        actor: str,
        book_dir: Path | str,
        previous_state: dict[str, Any],
        new_state: dict[str, Any],
    ) -> str:
        """Creates an atomic sealed restore point before mutating a book."""
        raise PermissionError(
            "CryptographicLedger is retired; use the supervised Manifestation V2 apply writer instead."
        )

    def rollback(self, change_id: str, calibre_conn: sqlite3.Connection, library_base_path: Path | str) -> bool:
        """Surgically reverses a modification using its restore point."""
        raise PermissionError(
            "CryptographicLedger is retired; use the supervised Manifestation V2 apply writer instead."
        )

    def quarantine_book_files(
        self,
        book_id: int,
        book_dir: Path | str,
        reason: str,
        quarantine_base: Path | str | None = None,
    ) -> Path:
        """Safely isolates a book folder into .quarantine/ instead of destructive deletion."""
        raise PermissionError(
            "CryptographicLedger is retired; use the supervised Manifestation V2 apply writer instead."
        )

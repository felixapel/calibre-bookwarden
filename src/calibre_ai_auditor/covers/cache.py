"""Local hierarchical cache for cover audit and quality scoring results.

Maintains a local SQLite database in WAL mode indexed by (rel_path, file_size, mtime_ns).
When cover files on disk have not been modified, audit metrics (CQS, Tier, entropy,
spurious status) are retrieved instantly without re-reading image files or computing pixels.
"""

from __future__ import annotations

import json
import logging
import sqlite3
from datetime import datetime
from pathlib import Path
from typing import Any

logger = logging.getLogger(__name__)

DEFAULT_CACHE_DIR = Path.home() / ".calibre_ai_auditor"


class LocalCoverAuditCache:
    """Local SQLite-backed cache for Cover Quality Scores and Spurious Cover detections."""

    def __init__(self, db_path: Path | str | None = None):
        if db_path is None:
            DEFAULT_CACHE_DIR.mkdir(parents=True, exist_ok=True)
            self.db_path = DEFAULT_CACHE_DIR / "cover_cache.db"
        else:
            self.db_path = Path(db_path)
            self.db_path.parent.mkdir(parents=True, exist_ok=True)

        self._init_db()

    def _get_connection(self) -> sqlite3.Connection:
        conn = sqlite3.connect(str(self.db_path), timeout=15.0)
        conn.row_factory = sqlite3.Row
        c = conn.cursor()
        c.execute("PRAGMA journal_mode = WAL;")
        c.execute("PRAGMA synchronous = NORMAL;")
        c.execute("PRAGMA busy_timeout = 15000;")
        return conn

    def _init_db(self) -> None:
        conn = self._get_connection()
        try:
            conn.execute("""
                CREATE TABLE IF NOT EXISTS cover_cache (
                    rel_path TEXT PRIMARY KEY,
                    book_id INTEGER,
                    file_size INTEGER NOT NULL,
                    mtime_ns INTEGER NOT NULL,
                    cqs INTEGER NOT NULL,
                    tier TEXT NOT NULL,
                    is_spurious INTEGER NOT NULL,
                    defect_type TEXT,
                    entropy REAL DEFAULT 0.0,
                    penalties TEXT,
                    fatal_defects TEXT,
                    last_audited TIMESTAMP DEFAULT CURRENT_TIMESTAMP
                );
            """)
            conn.execute("""
                CREATE INDEX IF NOT EXISTS idx_cover_cache_mtime
                ON cover_cache(rel_path, file_size, mtime_ns);
            """)
            conn.commit()
        finally:
            conn.close()

    def get(self, rel_path: str, file_size: int, mtime_ns: int) -> dict[str, Any] | None:
        """Retrieves cached metrics if file size and mtime match disk attributes."""
        conn = self._get_connection()
        try:
            c = conn.cursor()
            c.execute(
                """
                SELECT book_id, rel_path, file_size, mtime_ns, cqs, tier,
                       is_spurious, defect_type, entropy, penalties, fatal_defects
                FROM cover_cache
                WHERE rel_path = ? AND file_size = ? AND mtime_ns = ?
                """,
                (rel_path, file_size, mtime_ns),
            )
            row = c.fetchone()
            if not row:
                return None
            return {
                "book_id": row["book_id"],
                "rel_path": row["rel_path"],
                "file_size": row["file_size"],
                "mtime_ns": row["mtime_ns"],
                "cqs": row["cqs"],
                "tier": row["tier"],
                "is_spurious": bool(row["is_spurious"]),
                "defect_type": row["defect_type"],
                "entropy": row["entropy"],
                "penalties": json.loads(row["penalties"]) if row["penalties"] else [],
                "fatal_defects": json.loads(row["fatal_defects"]) if row["fatal_defects"] else [],
            }
        finally:
            conn.close()

    def put(
        self,
        rel_path: str,
        file_size: int,
        mtime_ns: int,
        cqs: int,
        tier: str,
        is_spurious: bool,
        defect_type: str | None = None,
        entropy: float = 0.0,
        book_id: int | None = None,
        penalties: list[str] | None = None,
        fatal_defects: list[str] | None = None,
    ) -> None:
        """Saves or updates cover evaluation metrics in the cache."""
        conn = self._get_connection()
        try:
            conn.execute(
                """
                INSERT INTO cover_cache (
                    rel_path, book_id, file_size, mtime_ns, cqs, tier,
                    is_spurious, defect_type, entropy, penalties, fatal_defects, last_audited
                ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
                ON CONFLICT(rel_path) DO UPDATE SET
                    book_id = excluded.book_id,
                    file_size = excluded.file_size,
                    mtime_ns = excluded.mtime_ns,
                    cqs = excluded.cqs,
                    tier = excluded.tier,
                    is_spurious = excluded.is_spurious,
                    defect_type = excluded.defect_type,
                    entropy = excluded.entropy,
                    penalties = excluded.penalties,
                    fatal_defects = excluded.fatal_defects,
                    last_audited = excluded.last_audited
                """,
                (
                    rel_path,
                    book_id,
                    file_size,
                    mtime_ns,
                    cqs,
                    tier,
                    1 if is_spurious else 0,
                    defect_type,
                    entropy,
                    json.dumps(penalties or []),
                    json.dumps(fatal_defects or []),
                    datetime.now().isoformat(),
                ),
            )
            conn.commit()
        finally:
            conn.close()

    def clear(self) -> int:
        """Clears all cached records."""
        conn = self._get_connection()
        try:
            c = conn.cursor()
            c.execute("DELETE FROM cover_cache")
            deleted = c.rowcount
            conn.commit()
            return deleted
        finally:
            conn.close()

    def count(self) -> int:
        """Returns the number of entries in the cache."""
        conn = self._get_connection()
        try:
            c = conn.cursor()
            c.execute("SELECT COUNT(*) FROM cover_cache")
            return c.fetchone()[0]
        finally:
            conn.close()

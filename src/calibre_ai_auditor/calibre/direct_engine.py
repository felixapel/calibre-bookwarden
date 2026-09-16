"""High-performance direct SQLite engine for Calibre libraries.

Provides read-only metadata and filesystem audits plus database-only snapshots.
Direct library mutations are disabled in favor of the supervised writer. On
Windows, filesystem inspection fails closed because the available path reader
cannot anchor every parent directory against junction swaps.
"""

from __future__ import annotations

import logging
import os
import re
import sqlite3
from collections import defaultdict
from collections.abc import Iterator
from concurrent.futures import ThreadPoolExecutor
from datetime import datetime
from pathlib import Path
from typing import Any
from uuid import uuid4

from PIL import Image

from calibre_ai_auditor.rules.authority import compute_author_sort
from calibre_ai_auditor.security.files import SecurePathError, open_file_beneath

logger = logging.getLogger(__name__)

# Configure maximum pixels for cover processing once at module load
Image.MAX_IMAGE_PIXELS = 60_000_000


# Canonical leading articles to invert for sorting (English, Spanish, German, French, Italian)
LEADING_ARTICLES = [
    # English
    "the ",
    "a ",
    "an ",
    # Spanish
    "el ",
    "la ",
    "los ",
    "las ",
    "un ",
    "una ",
    "unos ",
    "unas ",
    # German
    "der ",
    "die ",
    "das ",
    "ein ",
    "eine ",
    # French
    "le ",
    "les ",
    "l'",
    "une ",
    "des ",
    # Italian
    "il ",
    "lo ",
    "i ",
    "gli ",
]


def calibre_title_sort(title: str | None) -> str:
    """Emulates Calibre's internal title_sort algorithm."""
    if not title:
        return ""
    t = title.strip()
    t_lower = t.lower()
    for art in LEADING_ARTICLES:
        if t_lower.startswith(art):
            art_len = len(art)
            clean_art = t[:art_len].strip().rstrip("'")
            rest = t[art_len:].strip()
            return f"{rest}, {clean_art}"
    return t


def calibre_author_sort(author: str | None) -> str:
    """Emulates canonical author_sort algorithm delegating to authority rules."""
    return compute_author_sort(author or "")


def _inspect_single_book(
    b: dict[str, Any],
    library_path: Path,
    dfiles: list[dict[str, Any]],
    max_image_pixels: int,
    junk_patterns: list[str],
) -> dict[str, Any]:
    bid = b["id"]
    title = b["title"] or ""
    path = b["path"]

    bad_title = None
    for pat in junk_patterns:
        if re.search(pat, title, re.IGNORECASE):
            bad_title = {"book_id": bid, "title": title, "pattern": pat}
            break
    if not bad_title and any(title.lower().endswith(ext) for ext in [".pdf", ".epub", ".mobi", ".azw3"]):
        bad_title = {"book_id": bid, "title": title, "pattern": "ends_with_extension"}

    empty_format = None
    if not dfiles:
        empty_format = {"book_id": bid, "title": title, "path": str(path) if path else ""}

    missing_data: list[dict[str, Any]] = []
    missing_cover = None
    tiny_cover = None
    huge_cover = None
    broken_cover = None

    if not path:
        if dfiles:
            missing_data.append({"book_id": bid, "title": title, "reason": "NO_PATH_IN_DB"})
        return {
            "bad_title": bad_title,
            "empty_format": empty_format,
            "missing_data": missing_data,
            "missing_cover": missing_cover,
            "tiny_cover": tiny_cover,
            "huge_cover": huge_cover,
            "broken_cover": broken_cover,
        }

    if os.name == "nt":
        if dfiles:
            missing_data.append(
                {"book_id": bid, "title": title, "reason": "UNSUPPORTED_SECURE_FILE_READ", "path": str(path)}
            )
        broken_cover = {"book_id": bid, "title": title, "error": "UNSUPPORTED_SECURE_FILE_READ"}
        return {
            "bad_title": bad_title,
            "empty_format": empty_format,
            "missing_data": missing_data,
            "missing_cover": missing_cover,
            "tiny_cover": tiny_cover,
            "huge_cover": huge_cover,
            "broken_cover": broken_cover,
        }

    folder = library_path / Path(path)
    try:
        folder.resolve().relative_to(library_path)
    except (ValueError, OSError, RuntimeError):
        if dfiles:
            missing_data.append({"book_id": bid, "title": title, "reason": "UNSAFE_BOOK_PATH", "path": str(path)})
        broken_cover = {"book_id": bid, "title": title, "error": "UNSAFE_BOOK_PATH"}
        return {
            "bad_title": bad_title,
            "empty_format": empty_format,
            "missing_data": missing_data,
            "missing_cover": missing_cover,
            "tiny_cover": tiny_cover,
            "huge_cover": huge_cover,
            "broken_cover": broken_cover,
        }

    # Format files on disk check
    for df in dfiles:
        fname = f"{df['name']}.{df['format'].lower()}"
        try:
            with open_file_beneath(library_path, folder / fname):
                pass
        except SecurePathError:
            missing_data.append({"book_id": bid, "title": title, "file": fname, "reason": "FILE_MISSING_ON_DISK"})

    # Cover checks
    try:
        with open_file_beneath(library_path, folder / "cover.jpg") as descriptor:
            size_bytes = os.fstat(descriptor).st_size
            if size_bytes < 4000:
                tiny_cover = {"book_id": bid, "title": title, "size_bytes": size_bytes}
            else:
                with os.fdopen(os.dup(descriptor), "rb") as cover_stream, Image.open(cover_stream) as im:
                    w, h = im.size
                    pixels = w * h
                    if pixels > max_image_pixels or size_bytes > 15_000_000:
                        huge_cover = {
                            "book_id": bid,
                            "title": title,
                            "dimensions": f"{w}x{h}",
                            "pixels": pixels,
                            "size_bytes": size_bytes,
                        }
                    elif w < 200 or h < 250:
                        tiny_cover = {
                            "book_id": bid,
                            "title": title,
                            "dimensions": f"{w}x{h}",
                            "size_bytes": size_bytes,
                        }
    except SecurePathError as e:
        if any(marker in str(e) for marker in ("not a regular file", "No such file", "Errno 2")):
            missing_cover = {"book_id": bid, "title": title}
        else:
            broken_cover = {"book_id": bid, "title": title, "error": "UNSAFE_COVER_PATH"}
    except Image.DecompressionBombError as e:
        huge_cover = {
            "book_id": bid,
            "title": title,
            "dimensions": "exceeds_max_pixels",
            "size_bytes": size_bytes if "size_bytes" in locals() else 0,
            "error": str(e),
        }
    except Exception as e:
        broken_cover = {"book_id": bid, "title": title, "error": str(e)}

    return {
        "bad_title": bad_title,
        "empty_format": empty_format,
        "missing_data": missing_data,
        "missing_cover": missing_cover,
        "tiny_cover": tiny_cover,
        "huge_cover": huge_cover,
        "broken_cover": broken_cover,
    }


class DirectCalibreEngine:
    def __init__(self, library_path: Path | str):
        self.library_path = Path(library_path).resolve()
        self.db_path = self.library_path / "metadata.db"
        if not self.db_path.exists():
            raise FileNotFoundError(f"Calibre metadata.db not found at: {self.db_path}")

    def get_connection(self, read_only: bool = True) -> sqlite3.Connection:
        """Return a read-only connection for direct inspection only.

        Direct library mutation is deliberately unavailable.  The supervised writer
        is the sole supported authority for changes to a Calibre library.
        """
        if read_only is not True:
            raise PermissionError(
                "Direct Calibre writes are disabled; submit the change through the supervised writer workflow."
            )
        # `mode=ro` is enforced by SQLite itself, unlike `query_only`, which
        # callers can reverse. Path.as_uri() escapes reserved path characters
        # and retains a UNC authority when the SQLite build supports it. Builds
        # without URI-authority support fail closed; never retry as read-write.
        uri = f"{self.db_path.as_uri()}?mode=ro"
        conn = sqlite3.connect(uri, uri=True, timeout=30.0)
        conn.row_factory = sqlite3.Row
        conn.create_function("title_sort", 1, calibre_title_sort)
        conn.create_function("author_sort", 1, calibre_author_sort)
        c = conn.cursor()
        c.execute("PRAGMA busy_timeout = 30000;")
        c.execute("PRAGMA cache_size = -64000;")
        c.execute("PRAGMA temp_store = MEMORY;")
        return conn

    def create_snapshot(self, backup_dir: Path | None = None) -> Path:
        """Create a verified database-only SQLite backup outside the library.

        This snapshot is limited to ``metadata.db``.  It is not a full-library
        rollback point for book files or other Calibre-managed artifacts.
        """
        target_dir = (
            Path(backup_dir).resolve()
            if backup_dir is not None
            else self.library_path.parent / ".calibre-ai-auditor-snapshots"
        )
        if target_dir == self.library_path or target_dir.is_relative_to(self.library_path):
            raise ValueError("Snapshot backup_dir must be outside the configured Calibre library")
        target_dir.mkdir(parents=True, exist_ok=True)
        ts = datetime.now().strftime("%Y%m%d_%H%M%S_%f")
        backup_path = target_dir / f"metadata.db.snapshot_{ts}_{uuid4().hex}.sqlite"
        try:
            source: sqlite3.Connection | None = None
            destination: sqlite3.Connection | None = None
            try:
                source = self.get_connection()
                destination = sqlite3.connect(str(backup_path), timeout=30.0)
                # SQLite's backup API copies a coherent view, including a source
                # database whose latest pages are still in its WAL.
                source.backup(destination)
                integrity = destination.execute("PRAGMA integrity_check").fetchone()
                if not integrity or integrity[0] != "ok":
                    raise RuntimeError("SQLite snapshot integrity check failed")
            finally:
                if destination is not None:
                    destination.close()
                if source is not None:
                    source.close()

            verification = sqlite3.connect(f"{backup_path.as_uri()}?mode=ro", uri=True, timeout=30.0)
            try:
                verification.execute("SELECT name FROM sqlite_schema LIMIT 1").fetchone()
                if verification.execute("PRAGMA integrity_check").fetchone()[0] != "ok":
                    raise RuntimeError("SQLite snapshot reopen verification failed")
            finally:
                verification.close()
        except Exception:
            backup_path.unlink(missing_ok=True)
            raise

        logger.info("Created verified database-only metadata snapshot at: %s", backup_path)
        return backup_path

    def stream_books(self, batch_size: int = 500) -> Iterator[dict[str, Any]]:
        """Keyset-based streaming iterator over books. O(1) memory consumption for 100k+ libraries."""
        conn = self.get_connection(read_only=True)
        try:
            c = conn.cursor()
            last_id = 0
            while True:
                c.execute(
                    """
                    SELECT b.id, b.title, b.author_sort, b.path, b.has_cover,
                           (SELECT GROUP_CONCAT(a.name, ' & ')
                            FROM books_authors_link bal
                            JOIN authors a ON a.id = bal.author
                            WHERE bal.book = b.id) as authors,
                           (SELECT r.rating
                            FROM books_ratings_link brl
                            JOIN ratings r ON r.id = brl.rating
                            WHERE brl.book = b.id LIMIT 1) as rating
                    FROM books b
                    WHERE b.id > ?
                    ORDER BY b.id ASC
                    LIMIT ?
                """,
                    (last_id, batch_size),
                )
                rows = c.fetchall()
                if not rows:
                    break
                for row in rows:
                    last_id = row["id"]
                    yield dict(row)
        finally:
            conn.close()

    def audit_library(self, max_image_pixels: int = 30_000_000, max_workers: int = 32) -> dict[str, Any]:
        """Runs a comprehensive 360-degree audit across SQLite, physical files, and covers."""
        conn = self.get_connection(read_only=True)
        try:
            c = conn.cursor()

            # 1. SQLite integrity
            c.execute("PRAGMA integrity_check")
            integrity = c.fetchone()[0]

            # 2. Foreign keys
            c.execute("SELECT name FROM sqlite_master WHERE type='table'")
            existing_tables = {row[0] for row in c.fetchall()}

            fk_issues: list[dict[str, Any]] = []
            for table, col in [
                ("books_authors_link", "book"),
                ("books_ratings_link", "book"),
                ("books_tags_link", "book"),
                ("books_series_link", "book"),
                ("books_publishers_link", "book"),
                ("books_languages_link", "book"),
                ("comments", "book"),
                ("identifiers", "book"),
                ("data", "book"),
            ]:
                if table in existing_tables:
                    c.execute(f"SELECT COUNT(*) FROM {table} WHERE {col} NOT IN (SELECT id FROM books)")
                    count = c.fetchone()[0]
                    if count > 0:
                        fk_issues.append({"table": table, "orphan_count": count})

            # 3. Duplicate titles
            c.execute("""
                SELECT lower(title) as norm_title, COUNT(*) as count, GROUP_CONCAT(id) as ids
                FROM books
                WHERE title IS NOT NULL AND trim(title) != ''
                GROUP BY lower(trim(title))
                HAVING count > 1
                ORDER BY count DESC
            """)
            duplicate_titles = [{"title": r["norm_title"], "count": r["count"], "ids": r["ids"]} for r in c.fetchall()]

            # 4. Unrated books & rating distribution
            c.execute("""
                SELECT COUNT(*) FROM books b
                WHERE NOT EXISTS (SELECT 1 FROM books_ratings_link brl WHERE brl.book = b.id)
            """)
            unrated_count = c.fetchone()[0]

            c.execute("""
                SELECT r.rating, COUNT(*) as count
                FROM books_ratings_link brl
                JOIN ratings r ON r.id = brl.rating
                GROUP BY r.rating
                ORDER BY r.rating DESC
            """)
            ratings_dist = {f"{r['rating'] / 2:.1f} Stars (Rating {r['rating']})": r["count"] for r in c.fetchall()}

            # 5. Author sort desyncs
            c.execute("""
                SELECT b.id, b.title, b.author_sort,
                       (
                           SELECT GROUP_CONCAT(sort_val, ' & ')
                           FROM (
                               SELECT a.sort AS sort_val
                               FROM books_authors_link bal
                               JOIN authors a ON a.id = bal.author
                               WHERE bal.book = b.id
                               ORDER BY bal.id ASC
                           )
                       ) as canon_sort
                FROM books b
                WHERE b.author_sort != (
                    SELECT GROUP_CONCAT(sort_val, ' & ')
                    FROM (
                        SELECT a.sort AS sort_val
                        FROM books_authors_link bal
                        JOIN authors a ON a.id = bal.author
                        WHERE bal.book = b.id
                        ORDER BY bal.id ASC
                    )
                )
                   OR b.author_sort IS NULL
            """)
            author_desyncs = [
                {"book_id": r["id"], "title": r["title"], "current": r["author_sort"], "canonical": r["canon_sort"]}
                for r in c.fetchall()
            ]

            # 6. Physical files & cover audit
            # Query all data files in a single atomic SQL query (eliminates N+1 queries)
            c.execute("SELECT book, name, format, uncompressed_size FROM data")
            data_by_book: dict[int, list[dict[str, Any]]] = defaultdict(list)
            for r in c.fetchall():
                data_by_book[r["book"]].append(dict(r))

            c.execute("""
                SELECT b.id, b.title, b.path, b.has_cover,
                       (
                           SELECT GROUP_CONCAT(auth_name, ' & ')
                           FROM (
                               SELECT a.name AS auth_name
                               FROM books_authors_link bal
                               JOIN authors a ON a.id = bal.author
                               WHERE bal.book = b.id
                               ORDER BY bal.id ASC
                           )
                       ) as authors
                FROM books b
            """)
            books = [dict(r) for r in c.fetchall()]
            total_books = len(books)
        finally:
            conn.close()

        junk_patterns = [r"\[welib\.org\]", r"\(z-library\)", r"_print", r"untitled", r"microsoft word", r"v\d+\.\d+"]

        missing_data_files: list[dict[str, Any]] = []
        empty_format_records: list[dict[str, Any]] = []
        broken_covers: list[dict[str, Any]] = []
        tiny_covers: list[dict[str, Any]] = []
        huge_covers: list[dict[str, Any]] = []
        missing_covers: list[dict[str, Any]] = []
        bad_titles: list[dict[str, Any]] = []

        workers = max(1, min(max_workers, len(books) or 1))
        with ThreadPoolExecutor(max_workers=workers) as executor:
            futures = [
                executor.submit(
                    _inspect_single_book,
                    b,
                    self.library_path,
                    data_by_book.get(b["id"], []),
                    max_image_pixels,
                    junk_patterns,
                )
                for b in books
            ]
            for future in futures:
                try:
                    res = future.result()
                except Exception as exc:
                    logger.error(f"Failed inspecting book during parallel audit: {exc}")
                    continue
                if res["bad_title"]:
                    bad_titles.append(res["bad_title"])
                if res["empty_format"]:
                    empty_format_records.append(res["empty_format"])
                if res["missing_data"]:
                    missing_data_files.extend(res["missing_data"])
                if res["missing_cover"]:
                    missing_covers.append(res["missing_cover"])
                if res["tiny_cover"]:
                    tiny_covers.append(res["tiny_cover"])
                if res["huge_cover"]:
                    huge_covers.append(res["huge_cover"])
                if res["broken_cover"]:
                    broken_covers.append(res["broken_cover"])

        return {
            "sqlite_integrity": integrity,
            "foreign_key_issues": len(fk_issues),
            "foreign_key_details": fk_issues,
            "total_books": total_books,
            "unrated_books": unrated_count,
            "ratings_distribution": ratings_dist,
            "author_desyncs_count": len(author_desyncs),
            "author_desyncs_sample": author_desyncs[:10],
            "duplicate_titles_count": len(duplicate_titles),
            "duplicate_titles": duplicate_titles[:15],
            "missing_data_files_count": len(missing_data_files),
            "missing_data_files": missing_data_files,
            "empty_format_records_count": len(empty_format_records),
            "empty_format_records": empty_format_records,
            "missing_covers_count": len(missing_covers),
            "broken_covers_count": len(broken_covers),
            "broken_covers": broken_covers,
            "huge_covers_count": len(huge_covers),
            "huge_covers": huge_covers,
            "tiny_covers_count": len(tiny_covers),
            "tiny_covers": tiny_covers[:15],
            "bad_titles_count": len(bad_titles),
            "bad_titles": bad_titles[:15],
        }

    def sync_all_author_sorts(self) -> int:
        """Reject legacy direct metadata synchronization."""
        raise PermissionError(
            "Direct Calibre writes are disabled; submit author-sort changes through the supervised writer workflow."
        )

    def purge_orphan_foreign_keys(self) -> dict[str, int]:
        """Reject legacy direct foreign-key cleanup."""
        raise PermissionError(
            "Direct Calibre writes are disabled; submit cleanup changes through the supervised writer workflow."
        )

    def delete_empty_format_records(self, delete_folders: bool = True) -> int:
        """Reject legacy direct record and folder deletion."""
        raise PermissionError(
            "Direct Calibre writes are disabled; submit deletion changes through the supervised writer workflow."
        )

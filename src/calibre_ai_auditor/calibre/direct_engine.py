"""High-performance direct SQLite engine for Calibre libraries.

Directly operates on metadata.db with full emulation of Calibre custom SQLite
functions (title_sort, author_sort) so triggers execute without errors.
Provides 360-degree library audits, atomic snapshots, file integrity checks,
author-sort synchronization, and foreign key saneamiento.
"""

from __future__ import annotations

import logging
import os
import re
import shutil
import sqlite3
from collections import defaultdict
from collections.abc import Iterator
from concurrent.futures import ThreadPoolExecutor
from datetime import datetime
from pathlib import Path
from typing import Any

from PIL import Image

from calibre_ai_auditor.rules.authority import compute_author_sort

logger = logging.getLogger(__name__)

# Configure maximum pixels for cover processing once at module load
Image.MAX_IMAGE_PIXELS = 60_000_000


def _safe_path(p: Path) -> Path:
    """Normalize path on Windows with extended path prefix to support paths > 260 chars."""
    s = str(p)
    if os.name == "nt" and not s.startswith("\\\\?\\"):
        if s.startswith("\\\\"):
            return Path("\\\\?\\UNC" + s[1:])
        elif len(s) >= 2 and s[1] == ":":
            return Path("\\\\?\\" + s)
    return p


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

    folder = _safe_path(library_path / path)
    try:
        entries: dict[str, os.DirEntry[str]] = {}
        with os.scandir(folder) as it:
            for d_entry in it:
                entries[d_entry.name.lower()] = d_entry
    except (FileNotFoundError, NotADirectoryError):
        if dfiles:
            missing_data.append({"book_id": bid, "title": title, "reason": "FOLDER_NOT_FOUND", "path": str(path)})
        return {
            "bad_title": bad_title,
            "empty_format": empty_format,
            "missing_data": missing_data,
            "missing_cover": missing_cover,
            "tiny_cover": tiny_cover,
            "huge_cover": huge_cover,
            "broken_cover": broken_cover,
        }
    except OSError as e:
        if dfiles:
            missing_data.append({"book_id": bid, "title": title, "reason": f"OS_ERROR: {e}", "path": str(path)})
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
        file_entry = entries.get(fname.lower())
        if file_entry is None or not file_entry.is_file():
            missing_data.append({"book_id": bid, "title": title, "file": fname, "reason": "FILE_MISSING_ON_DISK"})

    # Cover checks
    cov_entry = entries.get("cover.jpg")
    if cov_entry is None or not cov_entry.is_file():
        missing_cover = {"book_id": bid, "title": title}
    else:
        try:
            size_bytes = cov_entry.stat().st_size
            if size_bytes < 4000:
                tiny_cover = {"book_id": bid, "title": title, "size_bytes": size_bytes}
            else:
                cov_path = _safe_path(Path(cov_entry.path))
                with Image.open(cov_path) as im:
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

    def get_connection(self, read_only: bool = False) -> sqlite3.Connection:
        """Returns an optimized connection with custom functions registered for Calibre triggers."""
        # Windows UNC network paths (e.g. \\server\share) cannot use standard file: URI syntax
        is_unc = str(self.db_path).startswith(r"\\") or str(self.db_path).startswith("//")
        if read_only and not is_unc:
            uri = f"file:{self.db_path.as_posix()}?mode=ro"
            conn = sqlite3.connect(uri, uri=True, timeout=30.0)
        else:
            conn = sqlite3.connect(str(self.db_path), timeout=30.0)
        conn.row_factory = sqlite3.Row
        conn.create_function("title_sort", 1, calibre_title_sort)
        conn.create_function("author_sort", 1, calibre_author_sort)
        c = conn.cursor()
        c.execute("PRAGMA busy_timeout = 30000;")
        if read_only:
            c.execute("PRAGMA query_only = ON;")
        else:
            c.execute("PRAGMA synchronous = NORMAL;")
        c.execute("PRAGMA cache_size = -64000;")
        c.execute("PRAGMA temp_store = MEMORY;")
        return conn

    def create_snapshot(self, backup_dir: Path | None = None) -> Path:
        """Creates an atomic timestamped snapshot of metadata.db via VACUUM INTO."""
        ts = datetime.now().strftime("%Y%m%d_%H%M%S")
        target_dir = Path(backup_dir) if backup_dir else self.library_path
        target_dir.mkdir(parents=True, exist_ok=True)
        backup_path = target_dir / f"metadata.db.bak_{ts}"
        try:
            conn = self.get_connection(read_only=True)
            try:
                escaped = str(backup_path).replace("'", "''")
                conn.execute(f"VACUUM INTO '{escaped}'")
            finally:
                conn.close()
        except Exception as exc:
            logger.warning(f"VACUUM INTO snapshot failed ({exc}), falling back to file copy.")
            shutil.copyfile(self.db_path, backup_path)

        logger.info(f"Created metadata.db snapshot at: {backup_path}")
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
        """Synchronizes all authors.sort and books.author_sort across the library."""
        conn = self.get_connection()
        try:
            c = conn.cursor()

            # 1. Update authors.sort where NULL or unformatted
            c.execute("SELECT id, name, sort FROM authors")
            authors = c.fetchall()
            for a in authors:
                aid = a["id"]
                name = a["name"]
                curr_sort = a["sort"]
                canon_sort = calibre_author_sort(name)
                if not curr_sort or curr_sort != canon_sort:
                    c.execute("UPDATE authors SET sort = ? WHERE id = ?", (canon_sort, aid))

            # 2. Sync books.author_sort with deterministic ordering
            c.execute("""
                SELECT 
                    b.id,
                    (
                        SELECT GROUP_CONCAT(sort_val, ' & ')
                        FROM (
                            SELECT a.sort AS sort_val
                            FROM books_authors_link bal
                            JOIN authors a ON a.id = bal.author
                            WHERE bal.book = b.id
                            ORDER BY bal.id ASC
                        )
                    ) AS canonical_sort
                FROM books b
            """)
            updated_count = 0
            for bid, csort in c.fetchall():
                if csort:
                    c.execute(
                        "UPDATE books SET author_sort = ? WHERE id = ? AND (author_sort != ? OR author_sort IS NULL)",
                        (csort, bid, csort),
                    )
                    if c.rowcount > 0:
                        updated_count += 1

            conn.commit()
            logger.info(f"Synchronized author_sort for {updated_count} books.")
            return updated_count
        finally:
            conn.close()

    def purge_orphan_foreign_keys(self) -> dict[str, int]:
        """Cleans orphan rows in junction tables and unused entities."""
        conn = self.get_connection()
        try:
            c = conn.cursor()

            c.execute("SELECT name FROM sqlite_master WHERE type='table'")
            existing_tables = {row[0] for row in c.fetchall()}

            purged = {}
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
                    c.execute(f"DELETE FROM {table} WHERE {col} NOT IN (SELECT id FROM books)")
                    purged[table] = c.rowcount

            # Clean unused tags, series, publishers, authors
            cleanup_entities = [
                ("authors", "books_authors_link", "author"),
                ("tags", "books_tags_link", "tag"),
                ("series", "books_series_link", "series"),
                ("publishers", "books_publishers_link", "publisher"),
            ]
            for entity_table, link_table, link_col in cleanup_entities:
                if entity_table in existing_tables and link_table in existing_tables:
                    c.execute(
                        f"DELETE FROM {entity_table} WHERE id NOT IN (SELECT DISTINCT {link_col} FROM {link_table})"
                    )
                    purged[f"unused_{entity_table}"] = c.rowcount

            conn.commit()
            return purged
        finally:
            conn.close()

    def delete_empty_format_records(self, delete_folders: bool = True) -> int:
        """Removes book records that have no format files associated."""
        conn = self.get_connection()
        try:
            c = conn.cursor()

            c.execute("""
                SELECT b.id, b.title, b.path
                FROM books b
                WHERE NOT EXISTS (SELECT 1 FROM data d WHERE d.book = b.id)
            """)
            empty_books = c.fetchall()
            count = len(empty_books)

            c.execute("SELECT name FROM sqlite_master WHERE type='table'")
            existing_tables = {row[0] for row in c.fetchall()}

            link_tables = [
                "books_authors_link",
                "books_ratings_link",
                "books_tags_link",
                "books_series_link",
                "books_publishers_link",
                "books_languages_link",
                "comments",
                "identifiers",
                "data",
            ]
            # Identify any custom column link tables (e.g. books_custom_column_1_link)
            custom_link_tables = [
                tbl for tbl in existing_tables if tbl.startswith("books_custom_column_") and tbl.endswith("_link")
            ]
            all_link_tables = link_tables + custom_link_tables

            for b in empty_books:
                bid = b["id"]
                path = b["path"]
                if delete_folders and path:
                    folder = self.library_path / path
                    if folder.exists():
                        shutil.rmtree(folder, ignore_errors=True)

                for tbl in all_link_tables:
                    if tbl in existing_tables:
                        c.execute(f"DELETE FROM {tbl} WHERE book = ?", (bid,))
                c.execute("DELETE FROM books WHERE id = ?", (bid,))

            conn.commit()
            logger.info(f"Deleted {count} empty book records.")
            return count
        finally:
            conn.close()

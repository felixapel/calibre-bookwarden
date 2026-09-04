"""High-performance direct SQLite engine for Calibre libraries.

Directly operates on metadata.db with full emulation of Calibre custom SQLite
functions (title_sort, author_sort) so triggers execute without errors.
Provides 360-degree library audits, atomic snapshots, file integrity checks,
author-sort synchronization, and foreign key saneamiento.
"""

from __future__ import annotations

import logging
import re
import shutil
import sqlite3
from collections.abc import Iterator
from datetime import datetime
from pathlib import Path
from typing import Any

from PIL import Image

from calibre_ai_auditor.rules.authority import compute_author_sort

logger = logging.getLogger(__name__)

# Leading articles to invert for sorting
LEADING_ARTICLES = [
    "the ",
    "a ",
    "an ",
    "el ",
    "la ",
    "los ",
    "las ",
    "un ",
    "una ",
    "unos ",
    "unas ",
    "der ",
    "die ",
    "das ",
    "ein ",
    "eine ",
    "le ",
    "la ",
    "les ",
    "l'",
    "un ",
    "une ",
    "des ",
    "il ",
    "lo ",
    "la ",
    "i ",
    "gli ",
    "le ",
]

# Particles that remain with surname
NOBLE_PARTICLES = {"von", "van", "de", "del", "della", "de la", "de los", "da", "di", "du", "des"}


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
    return compute_author_sort(author)


class DirectCalibreEngine:
    def __init__(self, library_path: Path | str):
        self.library_path = Path(library_path).resolve()
        self.db_path = self.library_path / "metadata.db"
        if not self.db_path.exists():
            raise FileNotFoundError(f"Calibre metadata.db not found at: {self.db_path}")

    def get_connection(self, read_only: bool = False) -> sqlite3.Connection:
        """Returns an optimized connection with custom functions registered for Calibre triggers."""
        uri = f"file:{self.db_path.as_posix()}?mode=ro" if read_only else str(self.db_path)
        conn = sqlite3.connect(uri, uri=read_only, timeout=30.0)
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

    def audit_library(self, max_image_pixels: int = 30_000_000) -> dict[str, Any]:
        """Runs a comprehensive 360-degree audit across SQLite, physical files, and covers."""
        Image.MAX_IMAGE_PIXELS = 60_000_000
        conn = self.get_connection(read_only=True)
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
                c.execute(f"SELECT id, {col} FROM {table} WHERE {col} NOT IN (SELECT id FROM books)")
                for r in c.fetchall():
                    fk_issues.append({"table": table, "id": r["id"], "orphan_book_id": r[col]})

        c.execute("PRAGMA foreign_key_check")
        for r in c.fetchall():
            fk_issues.append(dict(r) if isinstance(r, sqlite3.Row) else tuple(r))

        # 3. Book count
        c.execute("SELECT COUNT(*) FROM books")
        total_books = c.fetchone()[0]

        # 4. Ratings
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
                       SELECT GROUP_CONCAT(a.sort, ' & ')
                       FROM books_authors_link bal
                       JOIN authors a ON a.id = bal.author
                       WHERE bal.book = b.id
                   ) as canon_sort
            FROM books b
            WHERE b.author_sort != (
                SELECT GROUP_CONCAT(a.sort, ' & ')
                FROM books_authors_link bal
                JOIN authors a ON a.id = bal.author
                WHERE bal.book = b.id
            )
               OR b.author_sort IS NULL
        """)
        author_desyncs = [
            {"book_id": r["id"], "title": r["title"], "current": r["author_sort"], "canonical": r["canon_sort"]}
            for r in c.fetchall()
        ]

        # 6. Physical files & cover audit
        c.execute("""
            SELECT b.id, b.title, b.path, b.has_cover,
                   (
                       SELECT GROUP_CONCAT(a.name, ' & ')
                       FROM books_authors_link bal
                       JOIN authors a ON a.id = bal.author
                       WHERE bal.book = b.id
                   ) as authors
            FROM books b
        """)
        books = c.fetchall()

        missing_data_files: list[dict[str, Any]] = []
        empty_format_records: list[dict[str, Any]] = []
        broken_covers: list[dict[str, Any]] = []
        tiny_covers: list[dict[str, Any]] = []
        huge_covers: list[dict[str, Any]] = []
        missing_covers: list[dict[str, Any]] = []
        bad_titles: list[dict[str, Any]] = []

        junk_patterns = [r"\[welib\.org\]", r"\(z-library\)", r"_print", r"untitled", r"microsoft word", r"v\d+\.\d+"]

        for b in books:
            bid = b["id"]
            title = b["title"] or ""
            path = b["path"]

            # Junk title check
            for pat in junk_patterns:
                if re.search(pat, title, re.IGNORECASE):
                    bad_titles.append({"book_id": bid, "title": title, "pattern": pat})
                    break
            if any(title.lower().endswith(ext) for ext in [".pdf", ".epub", ".mobi", ".azw3"]):
                bad_titles.append({"book_id": bid, "title": title, "pattern": "ends_with_extension"})

            # Format files check in data table
            c.execute("SELECT name, format, uncompressed_size FROM data WHERE book = ?", (bid,))
            dfiles = c.fetchall()
            if not dfiles:
                empty_format_records.append({"book_id": bid, "title": title, "path": str(path) if path else ""})

            if not path:
                if dfiles:
                    missing_data_files.append({"book_id": bid, "title": title, "reason": "NO_PATH_IN_DB"})
                continue

            folder = self.library_path / path
            if not folder.exists():
                if dfiles:
                    missing_data_files.append(
                        {"book_id": bid, "title": title, "reason": "FOLDER_NOT_FOUND", "path": str(path)}
                    )
                continue

            # Format files on disk check
            for df in dfiles:
                fname = f"{df['name']}.{df['format'].lower()}"
                fpath = folder / fname
                if not fpath.exists():
                    missing_data_files.append(
                        {"book_id": bid, "title": title, "file": fname, "reason": "FILE_MISSING_ON_DISK"}
                    )

            # Cover checks
            cov_path = folder / "cover.jpg"
            if not cov_path.exists():
                missing_covers.append({"book_id": bid, "title": title})
            else:
                size_bytes = cov_path.stat().st_size
                if size_bytes < 4000:
                    tiny_covers.append({"book_id": bid, "title": title, "size_bytes": size_bytes})
                else:
                    try:
                        with Image.open(cov_path) as im:
                            w, h = im.size
                            pixels = w * h
                            if pixels > max_image_pixels or size_bytes > 15_000_000:
                                huge_covers.append(
                                    {
                                        "book_id": bid,
                                        "title": title,
                                        "dimensions": f"{w}x{h}",
                                        "pixels": pixels,
                                        "size_bytes": size_bytes,
                                    }
                                )
                            elif w < 200 or h < 250:
                                tiny_covers.append(
                                    {"book_id": bid, "title": title, "dimensions": f"{w}x{h}", "size_bytes": size_bytes}
                                )
                    except Exception as e:
                        broken_covers.append({"book_id": bid, "title": title, "error": str(e)})

        conn.close()

        return {
            "sqlite_integrity": integrity,
            "foreign_key_issues": len(fk_issues),
            "foreign_key_details": fk_issues,
            "total_books": total_books,
            "unrated_books": unrated_count,
            "ratings_distribution": ratings_dist,
            "author_desyncs_count": len(author_desyncs),
            "author_desyncs_sample": author_desyncs[:10],
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

        # 2. Sync books.author_sort
        c.execute("""
            SELECT 
                b.id,
                (
                    SELECT GROUP_CONCAT(a.sort, ' & ')
                    FROM books_authors_link bal
                    JOIN authors a ON a.id = bal.author
                    WHERE bal.book = b.id
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
        conn.close()
        logger.info(f"Synchronized author_sort for {updated_count} books.")
        return updated_count

    def purge_orphan_foreign_keys(self) -> dict[str, int]:
        """Cleans orphan rows in junction tables and unused entities."""
        conn = self.get_connection()
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
                c.execute(f"DELETE FROM {entity_table} WHERE id NOT IN (SELECT DISTINCT {link_col} FROM {link_table})")
                purged[f"unused_{entity_table}"] = c.rowcount

        conn.commit()
        conn.close()
        return purged

    def delete_empty_format_records(self, delete_folders: bool = True) -> int:
        """Removes book records that have no format files associated."""
        conn = self.get_connection()
        c = conn.cursor()

        c.execute("""
            SELECT b.id, b.title, b.path
            FROM books b
            WHERE NOT EXISTS (SELECT 1 FROM data d WHERE d.book = b.id)
        """)
        empty_books = c.fetchall()
        count = len(empty_books)

        for b in empty_books:
            bid = b["id"]
            path = b["path"]
            if delete_folders and path:
                folder = self.library_path / path
                if folder.exists():
                    shutil.rmtree(folder, ignore_errors=True)

            c.execute("DELETE FROM books_authors_link WHERE book = ?", (bid,))
            c.execute("DELETE FROM books_ratings_link WHERE book = ?", (bid,))
            c.execute("DELETE FROM books_tags_link WHERE book = ?", (bid,))
            c.execute("DELETE FROM books_series_link WHERE book = ?", (bid,))
            c.execute("DELETE FROM books_publishers_link WHERE book = ?", (bid,))
            c.execute("DELETE FROM comments WHERE book = ?", (bid,))
            c.execute("DELETE FROM identifiers WHERE book = ?", (bid,))
            c.execute("DELETE FROM data WHERE book = ?", (bid,))
            c.execute("DELETE FROM books WHERE id = ?", (bid,))

        conn.commit()
        conn.close()
        logger.info(f"Deleted {count} empty book records.")
        return count

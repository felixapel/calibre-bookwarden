"""Fail-closed access to a stopped, read-only Calibre library.

Certificate A never imports or invokes ``CalibreCLI``.  It inventories a
stable private copy of ``metadata.db`` and materializes frozen ebook bytes
through descriptor-anchored, no-follow reads from the mounted library.
"""

from __future__ import annotations

import copy
import hashlib
import json
import os
import re
import shutil
import sqlite3
import stat
import tempfile
from collections.abc import Iterator
from contextlib import contextmanager
from dataclasses import dataclass
from pathlib import Path, PurePosixPath
from typing import Any

from calibre_ai_auditor.security.files import (
    SecurePathError,
    copy_file_beneath,
    ensure_secure_directory,
    open_file_beneath,
    sha256_file_beneath,
)

CALIBRE_APPLICATION_ID = 0x63616C69
# Calibre increments ``user_version`` after every schema upgrade.  Versions 25
# and 26 predate its application-id marker; the upgrade to version 27 is the
# first one that stamps ``0x63616c69``.  Keep the pairs explicit so a generic
# SQLite database cannot pass by combining a known version with the wrong
# marker, and so future Calibre schemas fail closed until their read contract is
# reviewed.
SUPPORTED_SCHEMA_MARKERS = frozenset(
    {
        (25, 0),
        (26, 0),
        (27, CALIBRE_APPLICATION_ID),
    }
)
SUPPORTED_SCHEMA_VERSIONS = frozenset(version for version, _application_id in SUPPORTED_SCHEMA_MARKERS)
MAX_METADATA_DB_BYTES = 4 * 1024 * 1024 * 1024
MAX_EBOOK_BYTES = 2 * 1024 * 1024 * 1024
_SIDECARS = ("metadata.db-wal", "metadata.db-shm", "metadata.db-journal")
_FORMAT_RE = re.compile(r"^[A-Z0-9]{1,16}$")
_REQUIRED_COLUMNS: dict[str, frozenset[str]] = {
    "books": frozenset(
        {
            "id",
            "title",
            "timestamp",
            "pubdate",
            "series_index",
            "author_sort",
            "path",
            "uuid",
            "has_cover",
            "last_modified",
        }
    ),
    "authors": frozenset({"id", "name"}),
    "books_authors_link": frozenset({"id", "book", "author"}),
    "publishers": frozenset({"id", "name"}),
    "books_publishers_link": frozenset({"book", "publisher"}),
    "series": frozenset({"id", "name"}),
    "books_series_link": frozenset({"book", "series"}),
    "tags": frozenset({"id", "name"}),
    "books_tags_link": frozenset({"id", "book", "tag"}),
    "languages": frozenset({"id", "lang_code"}),
    "books_languages_link": frozenset({"id", "book", "lang_code", "item_order"}),
    "identifiers": frozenset({"id", "book", "type", "val"}),
    "comments": frozenset({"book", "text"}),
    "data": frozenset({"id", "book", "format", "uncompressed_size", "name"}),
}


class OfflineCalibreError(RuntimeError):
    """The configured folder cannot be proven safe for an offline audit."""


class OfflineLibraryChangedError(OfflineCalibreError):
    """The live read-only mount no longer matches the frozen inventory."""


@dataclass(frozen=True)
class _FrozenFile:
    relative_path: str
    format: str
    size: int
    device: int
    inode: int
    mtime_ns: int
    ctime_ns: int
    sha256: str

    def manifest(self) -> dict[str, Any]:
        return {
            "relative_path": self.relative_path,
            "format": self.format,
            "size": self.size,
            "device": self.device,
            "inode": self.inode,
            "mtime_ns": self.mtime_ns,
            "ctime_ns": self.ctime_ns,
            "sha256": self.sha256,
        }


def _absolute_lexical(path: Path) -> Path:
    return Path(os.path.normpath(os.path.abspath(os.fspath(path))))


def _canonical_hash(value: object) -> str:
    payload = json.dumps(value, sort_keys=True, separators=(",", ":"), ensure_ascii=True).encode()
    return hashlib.sha256(payload).hexdigest()


def _single_text(value: object, *, field: str, allow_empty: bool = True) -> str | None:
    if value is None:
        return None
    if not isinstance(value, str) or "\x00" in value or any(ord(character) < 32 for character in value):
        raise OfflineCalibreError(f"Calibre {field} contains invalid text")
    if not value and not allow_empty:
        raise OfflineCalibreError(f"Calibre {field} is empty")
    return value


def _safe_relative_path(value: object, *, field: str) -> PurePosixPath:
    text = _single_text(value, field=field, allow_empty=False)
    assert text is not None
    candidate = PurePosixPath(text)
    if (
        candidate.is_absolute()
        or not candidate.parts
        or any(part in {"", ".", ".."} for part in candidate.parts)
        or "\\" in text
    ):
        raise OfflineCalibreError(f"Calibre {field} is not a safe relative path")
    return candidate


def _sidecar_exists(root: Path, name: str) -> bool:
    try:
        (root / name).lstat()
    except FileNotFoundError:
        return False
    return True


class OfflineCalibreSource:
    """Immutable inventory and materializer for one stopped Calibre library."""

    source_kind = "offline_calibre_snapshot"

    def __init__(
        self,
        library_path: Path,
        *,
        snapshot_root: Path,
        confirm_calibre_stopped: bool,
    ) -> None:
        if confirm_calibre_stopped is not True:
            raise OfflineCalibreError("explicit Calibre-stopped confirmation is required")

        self.library_path = _absolute_lexical(library_path)
        self._snapshot_parent = ensure_secure_directory(snapshot_root)
        self._temporary_root: Path | None = None
        self._connection: sqlite3.Connection | None = None
        self._metadata: dict[int, dict[str, Any]] = {}
        self._formats: dict[int, tuple[_FrozenFile, ...]] = {}
        self._metadata_sha256 = ""
        self._schema_signature = ""
        self.application_id = 0
        self.schema_version = 0
        self.fingerprint = ""

        try:
            self._reject_sidecars()
            temporary = Path(tempfile.mkdtemp(prefix="bookaudit-offline-", dir=self._snapshot_parent))
            os.chmod(temporary, 0o700, follow_symlinks=False)
            self._temporary_root = temporary
            snapshot = temporary / "metadata.snapshot.db"
            try:
                self._metadata_sha256 = copy_file_beneath(
                    self.library_path,
                    self.library_path / "metadata.db",
                    snapshot,
                    max_bytes=MAX_METADATA_DB_BYTES,
                )
            except (OSError, SecurePathError) as exc:
                raise OfflineCalibreError("offline Calibre source is unsafe or unreadable") from exc
            self._reject_sidecars()
            self._connection = self._open_snapshot(snapshot)
            self._validate_schema()
            self._load_inventory()
            self.fingerprint = _canonical_hash(
                {
                    "kind": self.source_kind,
                    "library_root_sha256": hashlib.sha256(str(self.library_path).encode()).hexdigest(),
                    "metadata_sha256": self._metadata_sha256,
                    "schema_version": self.schema_version,
                    "schema_signature": self._schema_signature,
                    "books": self._book_manifest(),
                }
            )
            self.assert_unchanged()
        except Exception:
            self.close()
            raise

    def __enter__(self) -> OfflineCalibreSource:
        return self

    def __exit__(self, _exc_type: object, _exc: object, _traceback: object) -> None:
        self.close()

    def close(self) -> None:
        if self._connection is not None:
            self._connection.close()
            self._connection = None
        if self._temporary_root is not None:
            shutil.rmtree(self._temporary_root, ignore_errors=False)
            self._temporary_root = None

    def _reject_sidecars(self) -> None:
        if any(_sidecar_exists(self.library_path, name) for name in _SIDECARS):
            raise OfflineCalibreError("Calibre SQLite sidecar state is present; the library is not offline")

    @staticmethod
    def _open_snapshot(snapshot: Path) -> sqlite3.Connection:
        try:
            connection = sqlite3.connect(f"{snapshot.as_uri()}?mode=ro&immutable=1", uri=True)
            connection.row_factory = sqlite3.Row
            connection.execute("PRAGMA trusted_schema=OFF")
            connection.execute("PRAGMA query_only=ON")
            if int(connection.execute("PRAGMA query_only").fetchone()[0]) != 1:
                raise OfflineCalibreError("Calibre snapshot could not be forced into query-only mode")
            quick_check = connection.execute("PRAGMA quick_check(1)").fetchone()
            if quick_check is None or quick_check[0] != "ok":
                raise OfflineCalibreError("Calibre metadata snapshot failed SQLite integrity checks")
            return connection
        except (sqlite3.DatabaseError, OSError) as exc:
            raise OfflineCalibreError("Calibre metadata snapshot is not a readable SQLite database") from exc

    @property
    def _db(self) -> sqlite3.Connection:
        if self._connection is None:
            raise OfflineCalibreError("offline Calibre source is closed")
        return self._connection

    def _validate_schema(self) -> None:
        self.application_id = int(self._db.execute("PRAGMA application_id").fetchone()[0])
        self.schema_version = int(self._db.execute("PRAGMA user_version").fetchone()[0])
        if self.schema_version not in SUPPORTED_SCHEMA_VERSIONS:
            expected = ", ".join(str(version) for version in sorted(SUPPORTED_SCHEMA_VERSIONS))
            raise OfflineCalibreError(
                f"unsupported Calibre schema version {self.schema_version}; supported versions are {expected}"
            )
        if (self.schema_version, self.application_id) not in SUPPORTED_SCHEMA_MARKERS:
            raise OfflineCalibreError(f"Calibre schema version {self.schema_version} has an unexpected application id")

        tables = {
            str(row[0]) for row in self._db.execute("SELECT name FROM sqlite_schema WHERE type = 'table'").fetchall()
        }
        missing_tables = sorted(set(_REQUIRED_COLUMNS) - tables)
        if missing_tables:
            raise OfflineCalibreError("Calibre metadata schema is missing required tables")

        signature: dict[str, list[str]] = {}
        for table, required in _REQUIRED_COLUMNS.items():
            columns = {str(row[1]) for row in self._db.execute(f'PRAGMA table_info("{table}")').fetchall()}
            if not required.issubset(columns):
                raise OfflineCalibreError(f"Calibre metadata table {table} is missing required columns")
            signature[table] = sorted(columns)
        self._schema_signature = _canonical_hash(signature)

    def _load_inventory(self) -> None:
        rows = self._db.execute(
            """
            SELECT id, title, timestamp, pubdate, series_index, author_sort,
                   path, uuid, has_cover, last_modified
            FROM books
            ORDER BY id
            """
        ).fetchall()
        for row in rows:
            book_id = row["id"]
            if isinstance(book_id, bool) or not isinstance(book_id, int) or book_id <= 0 or book_id in self._metadata:
                raise OfflineCalibreError("Calibre metadata contains an invalid or duplicate book id")
            book_path = _safe_relative_path(row["path"], field="book path")
            frozen_formats = self._load_formats(book_id, book_path)
            self._formats[book_id] = tuple(frozen_formats)

            identifiers: dict[str, str] = {}
            for identifier in self._db.execute(
                "SELECT type, val FROM identifiers WHERE book = ? ORDER BY lower(type), id",
                (book_id,),
            ):
                kind = (_single_text(identifier["type"], field="identifier type", allow_empty=False) or "").lower()
                value = _single_text(identifier["val"], field="identifier value", allow_empty=False) or ""
                if kind in identifiers:
                    raise OfflineCalibreError("Calibre metadata contains duplicate identifier types")
                identifiers[kind] = value

            has_cover = bool(row["has_cover"])
            cover = str(self.library_path.joinpath(*book_path.parts, "cover.jpg")) if has_cover else None
            metadata = {
                "id": book_id,
                "title": _single_text(row["title"], field="title", allow_empty=False),
                "authors": self._many_names(
                    "SELECT a.name FROM books_authors_link AS link "
                    "JOIN authors AS a ON a.id = link.author WHERE link.book = ? ORDER BY link.id",
                    book_id,
                    "author",
                ),
                "author_sort": _single_text(row["author_sort"], field="author sort"),
                "publisher": self._one_name(
                    "SELECT p.name FROM books_publishers_link AS link "
                    "JOIN publishers AS p ON p.id = link.publisher WHERE link.book = ? ORDER BY link.id",
                    book_id,
                    "publisher",
                ),
                "pubdate": _single_text(row["pubdate"], field="publication date"),
                "series": self._one_name(
                    "SELECT s.name FROM books_series_link AS link "
                    "JOIN series AS s ON s.id = link.series WHERE link.book = ? ORDER BY link.id",
                    book_id,
                    "series",
                ),
                "series_index": float(row["series_index"]),
                "identifiers": identifiers,
                "languages": self._many_names(
                    "SELECT l.lang_code AS name FROM books_languages_link AS link "
                    "JOIN languages AS l ON l.id = link.lang_code WHERE link.book = ? "
                    "ORDER BY link.item_order, link.id",
                    book_id,
                    "language",
                ),
                "tags": self._many_names(
                    "SELECT t.name FROM books_tags_link AS link "
                    "JOIN tags AS t ON t.id = link.tag WHERE link.book = ? ORDER BY link.id",
                    book_id,
                    "tag",
                ),
                "comments": self._one_name(
                    "SELECT text AS name FROM comments WHERE book = ? ORDER BY id",
                    book_id,
                    "comment",
                ),
                "formats": [
                    str(self.library_path.joinpath(*PurePosixPath(item.relative_path).parts)) for item in frozen_formats
                ],
                "cover": cover,
                "timestamp": _single_text(row["timestamp"], field="timestamp"),
                "uuid": _single_text(row["uuid"], field="uuid"),
                "last_modified": _single_text(row["last_modified"], field="last modified"),
            }
            self._metadata[book_id] = metadata

    def _load_formats(self, book_id: int, book_path: PurePosixPath) -> list[_FrozenFile]:
        formats: list[_FrozenFile] = []
        seen: set[str] = set()
        rows = self._db.execute(
            "SELECT format, name FROM data WHERE book = ? ORDER BY upper(format), id",
            (book_id,),
        ).fetchall()
        for row in rows:
            canonical = (_single_text(row["format"], field="format", allow_empty=False) or "").upper()
            name = _single_text(row["name"], field="format filename", allow_empty=False) or ""
            if not _FORMAT_RE.fullmatch(canonical) or "/" in name or "\\" in name or name in {".", ".."}:
                raise OfflineCalibreError("Calibre metadata contains an unsafe format filename")
            if canonical in seen:
                raise OfflineCalibreError("Calibre metadata contains duplicate book formats")
            seen.add(canonical)
            relative = book_path / f"{name}.{canonical.lower()}"
            formats.append(self._describe_file(relative.as_posix(), canonical))
        return formats

    def _describe_file(self, relative_path: str, canonical_format: str) -> _FrozenFile:
        candidate = self.library_path.joinpath(*PurePosixPath(relative_path).parts)
        try:
            with open_file_beneath(self.library_path, candidate, max_bytes=MAX_EBOOK_BYTES) as descriptor:
                before = os.fstat(descriptor)
                digest = hashlib.sha256()
                while block := os.read(descriptor, 1024 * 1024):
                    digest.update(block)
                after = os.fstat(descriptor)
        except (OSError, SecurePathError) as exc:
            raise OfflineCalibreError("Calibre format path is unsafe or unreadable") from exc
        identity_before = (
            before.st_dev,
            before.st_ino,
            before.st_size,
            before.st_mtime_ns,
            before.st_ctime_ns,
        )
        identity_after = (
            after.st_dev,
            after.st_ino,
            after.st_size,
            after.st_mtime_ns,
            after.st_ctime_ns,
        )
        if identity_before != identity_after or not stat.S_ISREG(before.st_mode):
            raise OfflineCalibreError("Calibre format changed during inventory")
        return _FrozenFile(
            relative_path=relative_path,
            format=canonical_format,
            size=before.st_size,
            device=before.st_dev,
            inode=before.st_ino,
            mtime_ns=before.st_mtime_ns,
            ctime_ns=before.st_ctime_ns,
            sha256=digest.hexdigest(),
        )

    def _many_names(self, query: str, book_id: int, field: str) -> list[str]:
        return [
            _single_text(row["name"], field=field, allow_empty=False) or ""
            for row in self._db.execute(query, (book_id,)).fetchall()
        ]

    def _one_name(self, query: str, book_id: int, field: str) -> str | None:
        rows = self._db.execute(query, (book_id,)).fetchall()
        if len(rows) > 1:
            raise OfflineCalibreError(f"Calibre metadata contains multiple {field} values")
        return None if not rows else _single_text(rows[0]["name"], field=field, allow_empty=False)

    def _book_manifest(self) -> list[dict[str, Any]]:
        return [
            {
                "book_id": book_id,
                "formats": [item.manifest() for item in self._formats[book_id]],
            }
            for book_id in sorted(self._formats)
        ]

    @property
    def snapshot_manifest(self) -> dict[str, Any]:
        formats = sum(len(items) for items in self._formats.values())
        return {
            "kind": self.source_kind,
            "fingerprint": self.fingerprint,
            "metadata_sha256": self._metadata_sha256,
            "application_id": self.application_id,
            "schema_version": self.schema_version,
            "schema_signature": self._schema_signature,
            "book_count": len(self._metadata),
            "format_count": formats,
            "books": self._book_manifest(),
        }

    def list_books(self) -> list[dict[str, Any]]:
        return [copy.deepcopy(self._metadata[book_id]) for book_id in sorted(self._metadata)]

    def show_metadata(self, book_id: int) -> dict[str, Any]:
        try:
            return copy.deepcopy(self._metadata[book_id])
        except KeyError:
            raise OfflineCalibreError("Calibre book is outside the frozen inventory") from None

    def format_references(self, book_id: int, raw_formats: object) -> list[str]:
        expected = self.show_metadata(book_id)["formats"]
        if raw_formats != expected:
            raise OfflineLibraryChangedError("Calibre format membership no longer matches the frozen inventory")
        return [f"calibre-offline:{self.fingerprint}:{book_id}:{item.format}" for item in self._formats[book_id]]

    def format_from_reference(self, reference: str) -> str:
        prefix = f"calibre-offline:{self.fingerprint}:"
        if not reference.startswith(prefix):
            raise ValueError("offline Calibre format reference is invalid")
        remainder = reference.removeprefix(prefix)
        book_id_text, separator, canonical = remainder.partition(":")
        if not separator or not book_id_text.isdigit() or not _FORMAT_RE.fullmatch(canonical):
            raise ValueError("offline Calibre format reference is invalid")
        book_id = int(book_id_text)
        if canonical not in {item.format for item in self._formats.get(book_id, ())}:
            raise ValueError("offline Calibre format reference is outside the frozen inventory")
        return canonical

    def _frozen_format(self, book_id: int, canonical_format: str) -> _FrozenFile:
        if isinstance(book_id, bool) or not isinstance(book_id, int) or book_id <= 0:
            raise ValueError("book id is invalid")
        canonical = canonical_format.strip().upper()
        for item in self._formats.get(book_id, ()):
            if item.format == canonical:
                return item
        raise ValueError("format is outside the frozen inventory")

    def _assert_file(self, frozen: _FrozenFile) -> None:
        candidate = self.library_path.joinpath(*PurePosixPath(frozen.relative_path).parts)
        try:
            current = self._describe_file(frozen.relative_path, frozen.format)
        except OfflineCalibreError as exc:
            raise OfflineLibraryChangedError("Calibre format is no longer safely readable") from exc
        if current != frozen:
            raise OfflineLibraryChangedError("Calibre format changed after inventory")
        # Keep the candidate construction above explicit: all comparisons are
        # anchored beneath the original lexical library root.
        del candidate

    def assert_unchanged(self, book_id: int | None = None) -> None:
        try:
            self._reject_sidecars()
        except OfflineCalibreError as exc:
            raise OfflineLibraryChangedError("Calibre SQLite sidecar state appeared after inventory") from exc
        try:
            current_metadata = sha256_file_beneath(
                self.library_path,
                self.library_path / "metadata.db",
                max_bytes=MAX_METADATA_DB_BYTES,
            )
        except (OSError, SecurePathError) as exc:
            raise OfflineLibraryChangedError("Calibre metadata is no longer safely readable") from exc
        if current_metadata != self._metadata_sha256:
            raise OfflineLibraryChangedError("Calibre metadata changed after inventory")
        selected = self._formats.items() if book_id is None else ((book_id, self._formats.get(book_id, ())),)
        for _selected_book_id, formats in selected:
            for frozen in formats:
                self._assert_file(frozen)

    @contextmanager
    def export_format(
        self,
        book_id: int,
        format_name: str,
        *,
        scratch_root: Path,
    ) -> Iterator[Path]:
        frozen = self._frozen_format(book_id, format_name)
        try:
            self._reject_sidecars()
        except OfflineCalibreError as exc:
            raise OfflineLibraryChangedError("Calibre SQLite sidecar state appeared after inventory") from exc
        self._assert_file(frozen)
        root = ensure_secure_directory(scratch_root)
        temporary = Path(tempfile.mkdtemp(prefix="bookaudit-format-", dir=root))
        os.chmod(temporary, 0o700, follow_symlinks=False)
        exported = temporary / f"source.{frozen.format.lower()}"
        source = self.library_path.joinpath(*PurePosixPath(frozen.relative_path).parts)
        try:
            try:
                copy_file_beneath(
                    self.library_path,
                    source,
                    exported,
                    max_bytes=MAX_EBOOK_BYTES,
                    expected_sha256=frozen.sha256,
                )
            except (OSError, SecurePathError) as exc:
                raise OfflineLibraryChangedError("Calibre format changed while it was materialized") from exc
            self._assert_file(frozen)
            yield exported
            self._assert_file(frozen)
        finally:
            shutil.rmtree(temporary, ignore_errors=False)

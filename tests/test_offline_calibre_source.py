from __future__ import annotations

import sqlite3
from pathlib import Path

import pytest

from calibre_ai_auditor.calibre.offline import (
    OfflineCalibreError,
    OfflineCalibreSource,
    OfflineLibraryChangedError,
)
from calibre_ai_auditor.extractors.multiformat import FormatInspection
from calibre_ai_auditor.verification.identity_v2 import FormatEvidence, FormatEvidenceStatus
from calibre_ai_auditor.verification.pipeline_v2 import (
    BookAuditState,
    BookSourceDescriptor,
    LibraryAuditPipeline,
)


def _create_calibre_fixture(root: Path) -> Path:
    root.mkdir()
    book_dir = root / "Ada Author" / "Exact Book (1)"
    book_dir.mkdir(parents=True)
    (book_dir / "Exact Book - Ada Author.epub").write_bytes(b"fixture-epub")
    (book_dir / "Exact Book - Ada Author.pdf").write_bytes(b"fixture-pdf")
    (book_dir / "cover.jpg").write_bytes(b"fixture-cover")

    database = root / "metadata.db"
    with sqlite3.connect(database) as connection:
        connection.executescript(
            """
            PRAGMA application_id = 0x63616c69;
            PRAGMA user_version = 27;
            CREATE TABLE books (
                id INTEGER PRIMARY KEY,
                title TEXT NOT NULL,
                sort TEXT,
                timestamp TIMESTAMP,
                pubdate TIMESTAMP,
                series_index REAL NOT NULL,
                author_sort TEXT,
                path TEXT NOT NULL,
                uuid TEXT,
                has_cover BOOL,
                last_modified TIMESTAMP NOT NULL
            );
            CREATE TABLE authors (id INTEGER PRIMARY KEY, name TEXT NOT NULL, sort TEXT, link TEXT NOT NULL);
            CREATE TABLE books_authors_link (
                id INTEGER PRIMARY KEY, book INTEGER NOT NULL, author INTEGER NOT NULL
            );
            CREATE TABLE publishers (id INTEGER PRIMARY KEY, name TEXT NOT NULL, sort TEXT, link TEXT NOT NULL);
            CREATE TABLE books_publishers_link (
                id INTEGER PRIMARY KEY, book INTEGER NOT NULL, publisher INTEGER NOT NULL
            );
            CREATE TABLE series (id INTEGER PRIMARY KEY, name TEXT NOT NULL, sort TEXT, link TEXT NOT NULL);
            CREATE TABLE books_series_link (
                id INTEGER PRIMARY KEY, book INTEGER NOT NULL, series INTEGER NOT NULL
            );
            CREATE TABLE tags (id INTEGER PRIMARY KEY, name TEXT NOT NULL, link TEXT NOT NULL);
            CREATE TABLE books_tags_link (id INTEGER PRIMARY KEY, book INTEGER NOT NULL, tag INTEGER NOT NULL);
            CREATE TABLE languages (id INTEGER PRIMARY KEY, lang_code TEXT NOT NULL, link TEXT NOT NULL);
            CREATE TABLE books_languages_link (
                id INTEGER PRIMARY KEY,
                book INTEGER NOT NULL,
                lang_code INTEGER NOT NULL,
                item_order INTEGER NOT NULL
            );
            CREATE TABLE identifiers (
                id INTEGER PRIMARY KEY, book INTEGER NOT NULL, type TEXT NOT NULL, val TEXT NOT NULL
            );
            CREATE TABLE comments (id INTEGER PRIMARY KEY, book INTEGER NOT NULL, text TEXT NOT NULL);
            CREATE TABLE data (
                id INTEGER PRIMARY KEY,
                book INTEGER NOT NULL,
                format TEXT NOT NULL,
                uncompressed_size INTEGER NOT NULL,
                name TEXT NOT NULL
            );

            INSERT INTO books VALUES (
                1, 'Exact Book', 'Exact Book', '2026-01-01 10:00:00+00:00',
                '2020-05-06 00:00:00+00:00', 2.0, 'Author, Ada',
                'Ada Author/Exact Book (1)', 'fixture-uuid', 1,
                '2026-01-02 10:00:00+00:00'
            );
            INSERT INTO authors VALUES (1, 'Ada Author', 'Author, Ada', '');
            INSERT INTO books_authors_link VALUES (1, 1, 1);
            INSERT INTO publishers VALUES (1, 'Fixture Press', 'Fixture Press', '');
            INSERT INTO books_publishers_link VALUES (1, 1, 1);
            INSERT INTO series VALUES (1, 'Fixture Series', 'Fixture Series', '');
            INSERT INTO books_series_link VALUES (1, 1, 1);
            INSERT INTO tags VALUES (1, 'reference', '');
            INSERT INTO books_tags_link VALUES (1, 1, 1);
            INSERT INTO languages VALUES (1, 'eng', '');
            INSERT INTO books_languages_link VALUES (1, 1, 1, 0);
            INSERT INTO identifiers VALUES (1, 1, 'isbn', '9780306406157');
            INSERT INTO comments VALUES (1, 1, 'Fixture comment');
            INSERT INTO data VALUES (1, 1, 'EPUB', 12, 'Exact Book - Ada Author');
            INSERT INTO data VALUES (2, 1, 'PDF', 11, 'Exact Book - Ada Author');
            """
        )
    return database


def test_offline_source_requires_explicit_stopped_confirmation(tmp_path: Path) -> None:
    library = tmp_path / "library"
    _create_calibre_fixture(library)

    with pytest.raises(OfflineCalibreError, match="stopped confirmation"):
        OfflineCalibreSource(
            library,
            snapshot_root=tmp_path / "scratch",
            confirm_calibre_stopped=False,
        )


@pytest.mark.parametrize("sidecar", ["metadata.db-wal", "metadata.db-shm", "metadata.db-journal"])
def test_offline_source_rejects_sqlite_sidecars(tmp_path: Path, sidecar: str) -> None:
    library = tmp_path / "library"
    _create_calibre_fixture(library)
    (library / sidecar).write_bytes(b"active")

    with pytest.raises(OfflineCalibreError, match="SQLite sidecar"):
        OfflineCalibreSource(
            library,
            snapshot_root=tmp_path / "scratch",
            confirm_calibre_stopped=True,
        )


def test_offline_source_rejects_symlinked_library_or_metadata(tmp_path: Path) -> None:
    real_library = tmp_path / "real-library"
    _create_calibre_fixture(real_library)
    linked_library = tmp_path / "linked-library"
    linked_library.symlink_to(real_library, target_is_directory=True)

    with pytest.raises(OfflineCalibreError, match="unsafe"):
        OfflineCalibreSource(
            linked_library,
            snapshot_root=tmp_path / "scratch-a",
            confirm_calibre_stopped=True,
        )

    safe_library = tmp_path / "safe-library"
    safe_library.mkdir()
    (safe_library / "metadata.db").symlink_to(real_library / "metadata.db")
    with pytest.raises(OfflineCalibreError, match="unsafe"):
        OfflineCalibreSource(
            safe_library,
            snapshot_root=tmp_path / "scratch-b",
            confirm_calibre_stopped=True,
        )


def test_offline_source_reads_pinned_calibre_27_field_contract(tmp_path: Path) -> None:
    library = tmp_path / "library"
    _create_calibre_fixture(library)

    with OfflineCalibreSource(
        library,
        snapshot_root=tmp_path / "scratch",
        confirm_calibre_stopped=True,
    ) as source:
        books = source.list_books()
        metadata = source.show_metadata(1)

        assert books == [metadata]
        assert metadata == {
            "id": 1,
            "title": "Exact Book",
            "authors": ["Ada Author"],
            "author_sort": "Author, Ada",
            "publisher": "Fixture Press",
            "pubdate": "2020-05-06 00:00:00+00:00",
            "series": "Fixture Series",
            "series_index": 2.0,
            "identifiers": {"isbn": "9780306406157"},
            "languages": ["eng"],
            "tags": ["reference"],
            "comments": "Fixture comment",
            "formats": [
                str(library / "Ada Author" / "Exact Book (1)" / "Exact Book - Ada Author.epub"),
                str(library / "Ada Author" / "Exact Book (1)" / "Exact Book - Ada Author.pdf"),
            ],
            "cover": str(library / "Ada Author" / "Exact Book (1)" / "cover.jpg"),
            "timestamp": "2026-01-01 10:00:00+00:00",
            "uuid": "fixture-uuid",
            "last_modified": "2026-01-02 10:00:00+00:00",
        }
        assert source.schema_version == 27
        assert source.snapshot_manifest["book_count"] == 1
        assert source.snapshot_manifest["format_count"] == 2
        assert source.snapshot_manifest["metadata_sha256"]
        assert source.fingerprint


def test_offline_source_rejects_wrong_calibre_schema(tmp_path: Path) -> None:
    library = tmp_path / "library"
    database = _create_calibre_fixture(library)
    with sqlite3.connect(database) as connection:
        connection.execute("PRAGMA user_version = 26")

    with pytest.raises(OfflineCalibreError, match="schema version"):
        OfflineCalibreSource(
            library,
            snapshot_root=tmp_path / "scratch",
            confirm_calibre_stopped=True,
        )


def test_offline_source_materializes_only_frozen_formats_and_detects_drift(tmp_path: Path) -> None:
    library = tmp_path / "library"
    _create_calibre_fixture(library)
    scratch = tmp_path / "scratch"

    with OfflineCalibreSource(
        library,
        snapshot_root=scratch,
        confirm_calibre_stopped=True,
    ) as source:
        raw_formats = source.show_metadata(1)["formats"]
        references = source.format_references(1, raw_formats)
        assert [source.format_from_reference(item) for item in references] == ["EPUB", "PDF"]

        with source.export_format(1, "EPUB", scratch_root=scratch) as exported:
            assert exported.read_bytes() == b"fixture-epub"
            assert exported.parent != library
        assert not exported.exists()

        epub = library / "Ada Author" / "Exact Book (1)" / "Exact Book - Ada Author.epub"
        epub.write_bytes(b"changed")
        with pytest.raises(OfflineLibraryChangedError), source.export_format(1, "EPUB", scratch_root=scratch):
            pass


def test_offline_source_detects_metadata_drift_after_inventory(tmp_path: Path) -> None:
    library = tmp_path / "library"
    database = _create_calibre_fixture(library)

    with OfflineCalibreSource(
        library,
        snapshot_root=tmp_path / "scratch",
        confirm_calibre_stopped=True,
    ) as source:
        with sqlite3.connect(database) as connection:
            connection.execute("UPDATE books SET title = 'Changed' WHERE id = 1")
        with pytest.raises(OfflineLibraryChangedError):
            source.assert_unchanged()


@pytest.mark.asyncio
async def test_offline_source_pipeline_seals_logical_references(tmp_path: Path) -> None:
    library = tmp_path / "library"
    _create_calibre_fixture(library)
    scratch = tmp_path / "scratch"

    def inspect(path: Path) -> FormatInspection:
        return FormatInspection(
            format_evidence=FormatEvidence(
                path=str(path),
                format=path.suffix.lstrip(".").upper(),
                sha256="a" * 64,
                status=FormatEvidenceStatus.unsupported,
            )
        )

    with OfflineCalibreSource(
        library,
        snapshot_root=scratch,
        confirm_calibre_stopped=True,
    ) as source:
        result = await LibraryAuditPipeline(
            cli=source,
            inspector=inspect,
            scratch_root=scratch,
        ).run(run_id="offline-fixture")

        package = result.packages[0]
        expected_key = f"calibre-offline:{source.fingerprint}:1"
        assert package.book_key == expected_key
        assert package.snapshot.source == BookSourceDescriptor(
            kind="offline_calibre_snapshot",
            fingerprint=source.fingerprint,
            access_mode="read_only",
        )
        assert package.snapshot.files == [f"{expected_key}:EPUB", f"{expected_key}:PDF"]
        assert package.state is BookAuditState.review
        assert package.verify_seal()

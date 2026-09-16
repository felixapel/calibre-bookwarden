from __future__ import annotations

import sqlite3
import struct
import zipfile
from pathlib import Path
from types import SimpleNamespace
from unittest.mock import patch

import numpy as np
import pytest
from PIL import Image

from calibre_ai_auditor.calibre.streaming_engine import KeysetStreamingEngine
from calibre_ai_auditor.curation.entity_cleanser import EntityCleanser
from calibre_ai_auditor.curation.series_topology import (
    BookNode,
    DuplicateType,
    FRBRDeduplicationEngine,
    SeriesTopologyEngine,
)
from calibre_ai_auditor.extractors.container_archaeology import (
    BookFormat,
    ContainerArchaeologyPipeline,
    PathTraversalError,
    ZipBombError,
    read_zip_member_safely,
)
from calibre_ai_auditor.ocr.edge_vision import EdgeVisionEngine, OnnxModelContract
from calibre_ai_auditor.providers.consensus_engine import (
    ActionDecision,
    ConsensusEngine,
    ExternalCandidate,
    IdentityTier,
)


def _jpeg_bytes(tmp_path: Path) -> bytes:
    image_path = tmp_path / "cover.jpg"
    Image.new("RGB", (600, 900), (12, 80, 160)).save(image_path, "JPEG")
    return image_path.read_bytes()


def _write_epub(tmp_path: Path, name: str, opf: str, cover_page: str) -> Path:
    epub_path = tmp_path / name
    with zipfile.ZipFile(epub_path, "w") as archive:
        archive.writestr(
            "META-INF/container.xml",
            '<container><rootfiles><rootfile full-path="OEBPS/content.opf"/></rootfiles></container>',
        )
        archive.writestr("OEBPS/content.opf", opf)
        archive.writestr("OEBPS/cover.xhtml", cover_page)
        archive.writestr("OEBPS/images/cover.jpg", _jpeg_bytes(tmp_path))
    return epub_path


def test_epub2_cover_metadata_and_epub3_svg_leaf_cover_are_extracted(tmp_path: Path) -> None:
    epub2 = _write_epub(
        tmp_path,
        "epub2.epub",
        "<package><metadata><meta name='cover' content='cover-image'/></metadata>"
        "<manifest><item id='cover-image' href='images/cover.jpg' media-type='image/jpeg'/></manifest></package>",
        "<html/>",
    )
    epub3_svg = _write_epub(
        tmp_path,
        "epub3-svg.epub",
        "<package><metadata/><manifest><item id='cover-page' href='cover.xhtml' "
        "media-type='application/xhtml+xml'/></manifest><guide><reference type='cover' "
        "href='cover.xhtml'/></guide></package>",
        "<html xmlns='http://www.w3.org/1999/xhtml'><body><svg "
        "xmlns='http://www.w3.org/2000/svg'><image href='images/cover.jpg'/></svg></body></html>",
    )

    for epub in (epub2, epub3_svg):
        report = ContainerArchaeologyPipeline.inspect(epub)
        assert report.format is BookFormat.EPUB
        assert report.cover is not None
        assert report.cover.mime_type == "image/jpeg"


def test_archive_reader_rejects_traversal_member_and_preflight_zip_bomb(tmp_path: Path) -> None:
    traversal = tmp_path / "traversal.epub"
    with zipfile.ZipFile(traversal, "w") as archive:
        archive.writestr("../outside.txt", b"no")
    with zipfile.ZipFile(traversal) as archive, pytest.raises(PathTraversalError):
        read_zip_member_safely(archive, "../outside.txt")

    oversized = tmp_path / "oversized.epub"
    with zipfile.ZipFile(oversized, "w") as archive:
        archive.writestr("payload.bin", b"x" * 1025)
    with zipfile.ZipFile(oversized) as archive, pytest.raises(ZipBombError):
        read_zip_member_safely(archive, "payload.bin", max_bytes=1024)


@pytest.mark.parametrize(
    ("name", "eocd_fields", "reason"),
    [
        ("too-many", (0, 0, 10_001, 10_001, 0, 0, 0), "member limit"),
        ("large-central-directory", (0, 0, 0, 0, 8 * 1024 * 1024 + 1, 0, 0), "central directory"),
        ("malformed-comment", (0, 0, 0, 0, 0, 0, 1), "comment bounds"),
        ("zip64", (0, 0, 0xFFFF, 0xFFFF, 0, 0, 0), "ZIP64"),
    ],
)
def test_epub_preflight_rejects_unsafe_eocd_before_zipfile_constructor(
    tmp_path: Path,
    name: str,
    eocd_fields: tuple[int, int, int, int, int, int, int],
    reason: str,
) -> None:
    epub_path = tmp_path / f"{name}.epub"
    epub_path.write_bytes(struct.pack("<4s4H2LH", b"PK\x05\x06", *eocd_fields))

    with (
        patch("calibre_ai_auditor.extractors.container_archaeology.zipfile.ZipFile") as zip_file,
        pytest.raises(ZipBombError, match=reason),
    ):
        ContainerArchaeologyPipeline.inspect(epub_path)

    zip_file.assert_not_called()


def test_cbr_is_explicitly_unsupported(tmp_path: Path) -> None:
    path = tmp_path / "comic.cbr"
    path.write_bytes(b"not a rar archive")
    report = ContainerArchaeologyPipeline.inspect(path)
    assert report.format is BookFormat.CBR
    assert report.cover is None
    assert report.warnings == ["CBR is unsupported; no extraction attempted"]


def _book(book_id: int, hashes: list[str] | None = None) -> BookNode:
    return BookNode(
        book_id=book_id,
        title="Same Work",
        cleaned_title="Same Work",
        author="Author Example",
        author_sort="Example, Author",
        isbn="9780471958697",
        formats=["EPUB"],
        paths=[f"Author/Same Work ({book_id})"],
        file_hashes=hashes or [],
    )


def test_duplicate_labels_require_complete_hashes_for_all_transitive_members() -> None:
    digest = "a" * 64
    mixed_cluster = FRBRDeduplicationEngine.cluster_duplicates([_book(1, [digest]), _book(2, [digest]), _book(3)])
    assert mixed_cluster[0].cluster_type is DuplicateType.EDITION
    assert "Review" in mixed_cluster[0].recommendation

    complete_cluster = FRBRDeduplicationEngine.cluster_duplicates([_book(1, [digest]), _book(2, [digest])])
    assert complete_cluster[0].cluster_type is DuplicateType.CLONE


def test_author_authority_preserves_anonymous_and_does_not_split_spanish_y() -> None:
    assert EntityCleanser.resolve_author("Anonymous").is_valid
    assert EntityCleanser.resolve_author("Gabriel García Márquez").author_sort == "García Márquez, Gabriel"
    assert len(EntityCleanser.split_multi_authors("Ana y Garcia")) == 1


@pytest.mark.parametrize(
    ("name", "expected"),
    [
        ("Ludwig van der Waals", "van der Waals, Ludwig"),
        ("Jan der Meer", "der Meer, Jan"),
        ("Hans den Hartog", "den Hartog, Hans"),
        ("Naguib el Mahfouz", "el Mahfouz, Naguib"),
        ("Ibn al Haytham", "al Haytham, Ibn"),
        ("John Fitz Gerald", "Fitz Gerald, John"),
        ("Mary Mac Donald", "Mac Donald, Mary"),
        ("John Mc Donald", "Mc Donald, John"),
    ],
)
def test_author_authority_preserves_documented_particles(name: str, expected: str) -> None:
    assert EntityCleanser.resolve_author(name).author_sort == expected


@pytest.mark.parametrize(("name", "expected"), [("Al Gore", "Gore, Al"), ("Van Morrison", "Morrison, Van")])
def test_author_authority_does_not_mistake_leading_given_names_for_particles(name: str, expected: str) -> None:
    assert EntityCleanser.resolve_author(name).author_sort == expected


def test_consensus_is_review_evidence_and_rejects_valid_isbn_conflicts() -> None:
    matching = ExternalCandidate("test", "1", "Example", ["Author"], isbn13="9780471958697")
    result = ConsensusEngine.resolve_consensus("1", "Example", "Author", "9780471958697", [matching])
    assert result.tier is IdentityTier.TIER_C
    assert result.action is ActionDecision.REVIEW_RECOMMENDED

    conflict = ExternalCandidate("test", "2", "Different", ["Other"], isbn13="9780735211292")
    rejected = ConsensusEngine.resolve_consensus("1", "Example", "Author", "9780471958697", [conflict])
    assert rejected.best_candidate is None
    assert rejected.action is ActionDecision.KEEP_LOCAL
    assert rejected.scores.reasons == ["candidate_rejected_valid_isbn_conflict"]


class _FakeSession:
    def __init__(self, output: np.ndarray) -> None:
        self.output = output

    def get_inputs(self) -> list[SimpleNamespace]:
        return [SimpleNamespace(name="image")]

    def get_outputs(self) -> list[SimpleNamespace]:
        return [SimpleNamespace(name="scores")]

    def run(self, output_names: list[str], inputs: dict[str, np.ndarray]) -> list[np.ndarray]:
        assert output_names == ["scores"]
        assert inputs["image"].shape == (1, 3, 224, 224)
        return [self.output]


def test_edge_vision_requires_declared_contract_and_valid_finite_output(tmp_path: Path) -> None:
    image_path = tmp_path / "cover.png"
    pixels = np.random.default_rng(7).integers(0, 255, (900, 600, 3), dtype=np.uint8)
    Image.fromarray(pixels, "RGB").save(image_path)
    contract = OnnxModelContract(("commercial_cover", "text_interior_page"), "scores", "logits", "image")
    engine = EdgeVisionEngine(model_contract=contract)
    engine._is_onnx_loaded = True
    engine._onnx_session = _FakeSession(np.array([[3.0, 1.0]], dtype=np.float32))
    assert engine.classify_cover(image_path).predicted_class == "commercial_cover"

    engine._onnx_session = _FakeSession(np.array([[float("nan"), 1.0]], dtype=np.float32))
    invalid = engine.classify_cover(image_path)
    assert invalid.predicted_class == "unknown"
    assert invalid.is_commercial_cover is False


def test_streaming_uses_positive_batches_and_escaped_read_only_uri(tmp_path: Path) -> None:
    with pytest.raises(ValueError, match="positive integer"):
        KeysetStreamingEngine(tmp_path, batch_size=0)

    library = tmp_path / "library with spaces"
    library.mkdir()
    conn = sqlite3.connect(library / "metadata.db")
    conn.execute("CREATE TABLE books (id INTEGER PRIMARY KEY, title, author_sort, path, has_cover)")
    conn.execute("CREATE TABLE identifiers (book, type, val)")
    conn.execute("CREATE TABLE authors (id INTEGER PRIMARY KEY, name)")
    conn.execute("CREATE TABLE books_authors_link (book, author)")
    conn.execute("CREATE TABLE data (book, format)")
    conn.execute("INSERT INTO books VALUES (1, 'Title', 'Author', 'path', 0)")
    conn.commit()
    conn.close()
    assert [item.book_id for item in KeysetStreamingEngine(library, 1).stream_books()] == [1]


def test_series_gap_ignores_nonfinite_indices_and_bounds_spans() -> None:
    conn = sqlite3.connect(":memory:")
    conn.executescript(
        """
        CREATE TABLE series (id INTEGER, name TEXT);
        CREATE TABLE books_series_link (series INTEGER, book INTEGER);
        CREATE TABLE books (id INTEGER, title TEXT, series_index TEXT);
        CREATE TABLE books_authors_link (book INTEGER, author INTEGER);
        CREATE TABLE authors (id INTEGER, name TEXT);
        INSERT INTO series VALUES (1, 'Bounded');
        INSERT INTO books VALUES (1, 'One', '1'), (2, 'Huge', '999999'), (3, 'Bad', 'nan');
        INSERT INTO books_series_link VALUES (1, 1), (1, 2), (1, 3);
        """
    )
    assert SeriesTopologyEngine.analyze_series_gaps(conn) == []

"""Comprehensive test suite for the Master Forensic Pipeline (10 Pillars).

Tests:
1. Cover Forensics (HPP, FFT Harmonic Energy Ratio, Autocorrelation, Reading Page Detection)
2. Spurious Cover Detector integration with Forensics
3. Metadata Authority & Entity Cleanser (Garbage authors, noble particles, title casing, ISBNs)
4. Multi-Provider Consensus & Semantic Tripwire (Poisoned ISBN prevention)
5. Keyset Streaming & Tri-Tier Hashing
6. FRBR Deduplication & Fractional Series Gaps
7. Cooperative Advisory Lock & Homelab Bridge
8. Cryptographic Ledger & Atomic Reversible Restore Points
"""

import tempfile
from pathlib import Path

from PIL import Image, ImageDraw

from calibre_ai_auditor.calibre.streaming_engine import TriTierHasher
from calibre_ai_auditor.covers.forensics import CoverForensicsEngine
from calibre_ai_auditor.covers.spurious_detector import SpuriousCoverDetector
from calibre_ai_auditor.curation.entity_cleanser import EntityCleanser
from calibre_ai_auditor.curation.series_topology import (
    BookNode,
    DuplicateType,
    FRBRDeduplicationEngine,
)
from calibre_ai_auditor.providers.consensus_engine import (
    ActionDecision,
    ConsensusEngine,
    ExternalCandidate,
    HDCoverResolver,
    IdentityTier,
    SemanticTripwire,
)


def create_simulated_reading_page(output_path: Path, lines: int = 28) -> None:
    """Creates a synthetic beige scanned page with dense periodic text lines (like page 329)."""
    w, h = 600, 900
    # Warm cream / beige book paper (245, 240, 225)
    img = Image.new("RGB", (w, h), color=(245, 240, 225))
    draw = ImageDraw.Draw(img)

    # Marginal folio (top page number)
    draw.text((w // 2 - 10, 30), "329", fill=(30, 30, 30))

    # Dense regular horizontal paragraph lines
    y_start = 80
    line_spacing = 26
    for i in range(lines):
        y = y_start + (i * line_spacing)
        # Draw dotted/segmented black line simulating printed text
        for x in range(60, 540, 6):
            if (x // 6) % 7 != 0:  # simulate word spaces
                draw.line([(x, y), (x + 4, y)], fill=(35, 35, 35), width=3)

    img.save(output_path, "JPEG")


def create_simulated_commercial_cover(output_path: Path) -> None:
    """Creates a synthetic colorful commercial book cover with rich art and large title."""
    w, h = 600, 900
    img = Image.new("RGB", (w, h), color=(20, 35, 60))
    draw = ImageDraw.Draw(img)

    # Vibrant colored art blocks
    draw.ellipse([80, 150, 520, 590], fill=(220, 100, 30), outline=(255, 200, 50), width=6)
    draw.rectangle([120, 650, 480, 800], fill=(40, 80, 160))
    # Big title block
    draw.rectangle([100, 80, 500, 140], fill=(240, 240, 240))

    img.save(output_path, "JPEG")


# ==============================================================================
# 1. TEST COVER FORENSICS & SPURIOUS DETECTOR
# ==============================================================================


def test_forensics_detects_reading_page_scan():
    with tempfile.TemporaryDirectory() as tmpdir:
        page_path = Path(tmpdir) / "page_329.jpg"
        create_simulated_reading_page(page_path)

        engine = CoverForensicsEngine()
        metrics = engine.analyze(page_path, ocr_text="CHAPTER XII\nPage 329")

        assert metrics.is_reading_page is True
        assert metrics.her_score > 4.0
        assert metrics.acf_prominence > 0.20
        assert metrics.folio_detected is True
        assert metrics.defect_confidence >= 0.85


def test_forensics_approves_commercial_cover():
    with tempfile.TemporaryDirectory() as tmpdir:
        cover_path = Path(tmpdir) / "commercial.jpg"
        create_simulated_commercial_cover(cover_path)

        engine = CoverForensicsEngine()
        metrics = engine.analyze(cover_path)

        assert metrics.is_reading_page is False
        assert metrics.is_monochrome_placeholder is False
        assert metrics.defect_confidence < 0.50


def test_spurious_detector_with_forensics_integration():
    with tempfile.TemporaryDirectory() as tmpdir:
        page_path = Path(tmpdir) / "page_329.jpg"
        create_simulated_reading_page(page_path)

        detector = SpuriousCoverDetector()
        res = detector.inspect(page_path, ocr_text="329\nKing of Capital text")

        assert res.is_spurious is True
        assert res.defect_type == "interior_page_scan"
        assert res.confidence >= 0.85


# ==============================================================================
# 2. TEST METADATA AUTHORITY & ENTITY CLEANSER
# ==============================================================================


def test_entity_cleanser_eradicates_bogus_authors():
    bogus = ["calibre", "calibre-web", "CHAPTER ONE", "Unknown", "epublibre", "Autor"]
    for b in bogus:
        assert EntityCleanser.clean_author_name(b) is None
        auth = EntityCleanser.resolve_author(b)
        assert auth.is_valid is False

    # Valid authors with messy punctuation
    cleaned = EntityCleanser.clean_author_name("Roger Scruton;/")
    assert cleaned == "Roger Scruton"
    assert EntityCleanser.resolve_author("Anonymous").is_valid is True


def test_entity_cleanser_noble_particles_and_sort():
    # Ludwig van Beethoven -> van Beethoven, Ludwig (matches authority.py rule)
    res1 = EntityCleanser.resolve_author("Ludwig van Beethoven")
    assert res1.name == "Ludwig van Beethoven"
    assert res1.author_sort == "van Beethoven, Ludwig"

    # Mario Vargas Llosa -> Vargas Llosa, Mario
    res2 = EntityCleanser.resolve_author("Mario Vargas Llosa")
    assert res2.author_sort == "Vargas Llosa, Mario"

    res3 = EntityCleanser.resolve_author("Gabriel Garcia Marquez")
    assert res3.author_sort == "García Márquez, Gabriel"


def test_entity_cleanser_title_casing_and_acronyms():
    # ALL CAPS with acronym and minor words
    raw = "THE IMPACT OF AI AND LLM IN MODERN COMPUTING [RETAIL]"
    title_res = EntityCleanser.clean_title(raw)

    assert "retail" not in title_res.cleaned.lower()
    assert "AI" in title_res.cleaned
    assert "LLM" in title_res.cleaned
    assert "of" in title_res.cleaned
    assert "and" in title_res.cleaned
    assert "in" in title_res.cleaned
    assert title_res.was_modified is True


def test_entity_cleanser_isbn_math():
    # Valid ISBN-10 to ISBN-13
    # "0471958697" -> "9780471958697"
    isbn13 = EntityCleanser.validate_and_convert_isbn("0-471-95869-7")
    assert isbn13 == "9780471958697"

    # Invalid ISBN checksum
    assert EntityCleanser.validate_and_convert_isbn("9780471958691") is None


# ==============================================================================
# 3. TEST CONSENSUS ENGINE & SEMANTIC TRIPWIRE
# ==============================================================================


def test_semantic_tripwire_triggers_on_poisoned_isbn():
    # Local book: "La fatal ignorancia" by "Axel Kaiser"
    # External candidate from recycled template ISBN: "Atomic Habits" by "James Clear"
    passed, reason = SemanticTripwire.evaluate(
        local_title="La fatal ignorancia",
        local_author="Axel Kaiser",
        candidate_title="Atomic Habits: An Easy & Proven Way to Build Good Habits",
        candidate_authors=["James Clear"],
    )
    assert passed is False
    assert "Poisoned ISBN" in reason


def test_consensus_engine_quarantines_poisoned_isbn():
    candidate = ExternalCandidate(
        provider="google_books",
        provider_id="atomic_123",
        title="Atomic Habits",
        authors=["James Clear"],
        isbn13="9780735211292",
        cover_url="http://books.google.com/cover.jpg",
        is_hd_cover=True,
    )

    result = ConsensusEngine.resolve_consensus(
        book_id="4149",
        declared_title="La fatal ignorancia",
        declared_author="Axel Kaiser",
        declared_isbn="9780735211292",
        candidates=[candidate],
    )

    assert result.action == ActionDecision.KEEP_LOCAL
    assert result.tier == IdentityTier.TIER_C
    assert "candidate_isbn" in result.quarantined_identifiers


def test_hd_cover_resolver():
    # Google books URL transform
    thumb = "http://books.google.com/books/content?id=xyz&printsec=frontcover&img=1&zoom=1&edge=curl&source=gbs_api"
    hd = HDCoverResolver.transform_google_books_cover(thumb)
    assert "https://" in hd
    assert "edge=curl" not in hd
    assert "zoom=0" in hd
    assert "fife=w1600-h2400" in hd

    # OpenLibrary default=false
    ol = HDCoverResolver.get_openlibrary_hd_cover("9780471958697")
    assert "?default=false" in ol
    assert "-L.jpg" in ol


# ==============================================================================
# 4. TEST STREAMING & TRI-TIER HASHING
# ==============================================================================


def test_tri_tier_hasher():
    with tempfile.TemporaryDirectory() as tmpdir:
        dummy_file = Path(tmpdir) / "dummy.epub"
        dummy_file.write_bytes(b"X" * (128 * 1024))

        sparse_hash = TriTierHasher.compute_sparse_hash(dummy_file)
        full_hash = TriTierHasher.compute_full_hash(dummy_file)

        assert sparse_hash is not None
        assert "131072:" in sparse_hash
        assert full_hash is not None
        assert len(full_hash) == 64


# ==============================================================================
# 5. TEST FRBR DEDUPLICATION & ADVANCED SERIES
# ==============================================================================


def test_frbr_deduplication_multiformat():
    books = [
        BookNode(
            book_id=101,
            title="Free to Choose",
            cleaned_title="Free to Choose",
            author="Milton Friedman",
            author_sort="Friedman, Milton",
            isbn="9780156334600",
            formats=["EPUB"],
            paths=["Milton Friedman/Free to Choose (101)"],
        ),
        BookNode(
            book_id=102,
            title="Free to Choose",
            cleaned_title="Free to Choose",
            author="Milton Friedman",
            author_sort="Friedman, Milton",
            isbn="9780156334600",
            formats=["PDF"],
            paths=["Milton Friedman/Free to Choose (102)"],
        ),
    ]

    clusters = FRBRDeduplicationEngine.cluster_duplicates(books)
    assert len(clusters) == 1
    assert clusters[0].cluster_type == DuplicateType.MULTIFORMAT
    assert clusters[0].canonical_book_id == 101  # Prefers EPUB

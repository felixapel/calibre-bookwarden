import zipfile
from pathlib import Path

from PIL import Image

from calibre_ai_auditor.covers.extractor import (
    UnifiedCoverExtractor,
    extract_cover_from_epub,
    extract_native_cover,
)


def _make_dummy_image(path: Path, width: int = 400, height: int = 600, color: str = "blue") -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    img = Image.new("RGB", (width, height), color=color)
    img.save(path, format="JPEG")


def test_unified_extractor_epub(tmp_path: Path):
    epub_path = tmp_path / "sample.epub"
    cover_dest = tmp_path / "extracted_cover.jpg"

    # Build a valid EPUB ZIP archive with OPF & cover-image
    dummy_img = tmp_path / "raw_cover.jpg"
    _make_dummy_image(dummy_img, 600, 900, "darkred")

    container_xml = """<?xml version="1.0"?>
    <container version="1.0" xmlns="urn:oasis:names:tc:opendocument:xmlns:container">
        <rootfiles>
            <rootfile full-path="OEBPS/content.opf" media-type="application/oebps-package+xml"/>
        </rootfiles>
    </container>"""

    content_opf = """<?xml version="1.0" encoding="UTF-8"?>
    <package version="3.0" xmlns="http://www.idpf.org/2007/opf" unique-identifier="pub-id">
        <metadata xmlns:dc="http://purl.org/dc/elements/1.1/">
            <dc:title>Test Book</dc:title>
        </metadata>
        <manifest>
            <item id="cover-image" href="images/front.jpg" media-type="image/jpeg" properties="cover-image"/>
        </manifest>
        <spine></spine>
    </package>"""

    with zipfile.ZipFile(epub_path, "w") as z:
        z.writestr("META-INF/container.xml", container_xml)
        z.writestr("OEBPS/content.opf", content_opf)
        z.write(dummy_img, "OEBPS/images/front.jpg")

    # 1. Via classmethod
    success = UnifiedCoverExtractor.extract_from_epub(epub_path, cover_dest)
    assert success is True
    assert cover_dest.exists()
    assert cover_dest.stat().st_size > 0

    # 2. Via convenience wrapper
    cover_dest2 = tmp_path / "extracted_cover2.jpg"
    success2 = extract_cover_from_epub(epub_path, cover_dest2)
    assert success2 is True
    assert cover_dest2.exists()


def test_unified_extractor_comic_archive(tmp_path: Path):
    cbz_path = tmp_path / "comic.cbz"
    cover_dest = tmp_path / "extracted_comic_cover.jpg"

    dummy_page1 = tmp_path / "p001.jpg"
    _make_dummy_image(dummy_page1, 800, 1200, "green")
    dummy_page2 = tmp_path / "p002.jpg"
    _make_dummy_image(dummy_page2, 800, 1200, "white")

    with zipfile.ZipFile(cbz_path, "w") as z:
        z.write(dummy_page1, "001_cover.jpg")
        z.write(dummy_page2, "002_page.jpg")

    success = UnifiedCoverExtractor.extract_from_archive(cbz_path, cover_dest)
    assert success is True
    assert cover_dest.exists()
    assert cover_dest.stat().st_size > 0

    # Test top-level router
    cover_dest_router = tmp_path / "router_comic_cover.jpg"
    assert extract_native_cover(cbz_path, cover_dest_router) is True
    assert cover_dest_router.exists()


def test_unified_extractor_unsupported_format(tmp_path: Path):
    txt_path = tmp_path / "notes.txt"
    txt_path.write_text("plain text file")
    target = tmp_path / "dest.jpg"
    assert UnifiedCoverExtractor.extract(txt_path, target) is False
    assert not target.exists()

"""Disposable real Calibre and Tesseract gates for Certificate A."""

from __future__ import annotations

import shutil
import subprocess
from pathlib import Path
from zipfile import ZIP_DEFLATED, ZIP_STORED, ZipFile

import pytest
from PIL import Image, ImageDraw, ImageFont

from calibre_ai_auditor.calibre.offline import OfflineCalibreSource
from calibre_ai_auditor.verification.ocr_router import TesseractProvider

pytestmark = [
    pytest.mark.ocr_live,
    pytest.mark.skipif(
        shutil.which("calibredb") is None or shutil.which("tesseract") is None,
        reason="calibredb and tesseract are required",
    ),
]


def _epub(path: Path) -> None:
    with ZipFile(path, "w") as archive:
        archive.writestr("mimetype", "application/epub+zip", compress_type=ZIP_STORED)
        archive.writestr(
            "META-INF/container.xml",
            """<?xml version="1.0"?>
<container version="1.0" xmlns="urn:oasis:names:tc:opendocument:xmlns:container">
  <rootfiles><rootfile full-path="content.opf" media-type="application/oebps-package+xml"/></rootfiles>
</container>
""",
            compress_type=ZIP_DEFLATED,
        )
        archive.writestr(
            "content.opf",
            """<?xml version="1.0" encoding="UTF-8"?>
<package version="3.0" xmlns="http://www.idpf.org/2007/opf" unique-identifier="book-id">
  <metadata xmlns:dc="http://purl.org/dc/elements/1.1/">
    <dc:identifier id="book-id">urn:isbn:9780306406157</dc:identifier>
    <dc:title>Certificate A Fixture</dc:title><dc:creator>Ada Auditor</dc:creator>
    <dc:language>en</dc:language>
  </metadata>
  <manifest><item id="chapter" href="chapter.xhtml" media-type="application/xhtml+xml"/></manifest>
  <spine><itemref idref="chapter"/></spine>
</package>
""",
            compress_type=ZIP_DEFLATED,
        )
        archive.writestr(
            "chapter.xhtml",
            """<html xmlns="http://www.w3.org/1999/xhtml"><body>
<h1>Certificate A Fixture</h1><p>ISBN 9780306406157</p></body></html>""",
            compress_type=ZIP_DEFLATED,
        )


def test_real_calibre_library_is_read_through_the_offline_snapshot(tmp_path: Path) -> None:
    library = tmp_path / "library"
    epub = tmp_path / "fixture.epub"
    _epub(epub)
    result = subprocess.run(
        ["calibredb", "add", str(epub), "--with-library", str(library)],
        check=False,
        capture_output=True,
        text=True,
        timeout=30,
    )
    assert result.returncode == 0, result.stderr

    with OfflineCalibreSource(
        library,
        snapshot_root=tmp_path / "scratch",
        confirm_calibre_stopped=True,
    ) as source:
        books = source.list_books()
        assert source.schema_version == 27
        assert len(books) == 1
        assert books[0]["title"] == "Certificate A Fixture"
        assert books[0]["identifiers"] == {"isbn": "9780306406157"}
        assert source.snapshot_manifest["book_count"] == 1


@pytest.mark.asyncio
async def test_real_tesseract_reads_a_bounded_raster_pdf(tmp_path: Path) -> None:
    image = Image.new("RGB", (1800, 900), "white")
    font = ImageFont.truetype(
        "/usr/share/fonts/truetype/dejavu/DejaVuSans.ttf",
        72,
    )
    ImageDraw.Draw(image).multiline_text(
        (90, 120),
        "CERTIFICATE A BOOK AUDIT\nISBN 9780306406157\nREAD ONLY EVIDENCE",
        fill="black",
        font=font,
        spacing=40,
    )
    pdf = tmp_path / "raster.pdf"
    image.save(pdf, "PDF", resolution=200.0)

    results = await TesseractProvider(timeout_seconds=60).ocr_pdf_pages(
        pdf,
        page_range="1",
        language="en",
    )

    text = " ".join(result.text for result in results).replace(" ", "")
    assert "9780306406157" in text

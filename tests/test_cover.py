import tempfile
from pathlib import Path

import fitz  # PyMuPDF

from calibre_ai_auditor.extractors.cover import extract_pdf_cover


def test_extract_pdf_cover() -> None:
    with tempfile.TemporaryDirectory() as tmp_dir:
        # Create a dummy PDF
        pdf_path = Path(tmp_dir) / "dummy.pdf"
        doc = fitz.open()
        page = doc.new_page()
        page.insert_text((50, 100), "My Book Title", fontsize=20)
        page.insert_text((50, 150), "By Author Name", fontsize=14)
        doc.save(str(pdf_path))
        doc.close()

        # Extract cover
        output_jpg = Path(tmp_dir) / "cover.jpg"
        result = extract_pdf_cover(pdf_path, output_jpg)

        assert result is True
        assert output_jpg.exists()
        assert output_jpg.stat().st_size > 0

        # Try with a non-existent PDF
        result_fail = extract_pdf_cover(Path(tmp_dir) / "doesnotexist.pdf", output_jpg)
        assert result_fail is False


def test_extract_zip_cover() -> None:
    import zipfile

    from calibre_ai_auditor.extractors.cover import extract_zip_cover

    with tempfile.TemporaryDirectory() as tmp_dir:
        # Create a dummy zip (representing a .cbz)
        zip_path = Path(tmp_dir) / "dummy.cbz"
        with zipfile.ZipFile(zip_path, "w") as z:
            z.writestr("page_001.jpg", b"fake_jpeg_bytes")
            z.writestr("page_002.jpg", b"more_fake_jpeg_bytes")

        output_jpg = Path(tmp_dir) / "extracted_cover.jpg"
        result = extract_zip_cover(zip_path, output_jpg)

        assert result is True
        assert output_jpg.exists()
        assert output_jpg.read_bytes() == b"fake_jpeg_bytes"

        # Try with a non-existent ZIP file
        result_fail = extract_zip_cover(Path(tmp_dir) / "doesnotexist.cbz", output_jpg)
        assert result_fail is False

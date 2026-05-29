import tempfile
import zipfile
from pathlib import Path
from unittest.mock import patch

import pytest

from calibre_ai_auditor.extractors.comics import extract_comic_info_xml, parse_comic_info_xml_data


def test_parse_comic_info_xml_data() -> None:
    xml_data = b"""<?xml version="1.0" encoding="utf-8"?>
<ComicInfo xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance" xmlns:xsd="http://www.w3.org/2001/XMLSchema">
  <Title>Test Title</Title>
  <Series>Test Series</Series>
  <Number>3</Number>
  <Writer>Writer One, Writer Two</Writer>
  <Publisher>Test Publisher</Publisher>
  <Year>2020</Year>
  <Month>5</Month>
  <Genre>Action, Adventure</Genre>
</ComicInfo>
"""
    metadata = parse_comic_info_xml_data(xml_data)
    assert metadata is not None
    assert metadata["title"] == "Test Title"
    assert metadata["series"] == "Test Series"
    assert metadata["series_index"] == 3.0
    assert metadata["authors"] == ["Writer One", "Writer Two"]
    assert metadata["publisher"] == "Test Publisher"
    assert metadata["published_date"] == "2020-05-01"
    assert metadata["tags"] == ["Action", "Adventure"]


def test_extract_comic_info_xml() -> None:
    with tempfile.TemporaryDirectory() as tmp_dir:
        cbz_path = Path(tmp_dir) / "test.cbz"
        xml_data = b"""<?xml version="1.0" encoding="utf-8"?>
<ComicInfo>
  <Title>Zipped Manga</Title>
  <Writer>Manga Artist</Writer>
</ComicInfo>
"""
        with zipfile.ZipFile(cbz_path, "w") as z:
            z.writestr("ComicInfo.xml", xml_data)
            z.writestr("page1.jpg", b"fake image")

        metadata = extract_comic_info_xml(cbz_path)
        assert metadata is not None
        assert metadata["title"] == "Zipped Manga"
        assert metadata["authors"] == ["Manga Artist"]


@pytest.mark.asyncio
async def test_comic_evidence_builder() -> None:
    from calibre_ai_auditor.config.settings import Settings
    from calibre_ai_auditor.evidence.builder import build_evidence_package
    from calibre_ai_auditor.storage.models import BookRecord

    settings = Settings()

    with tempfile.TemporaryDirectory() as tmp_dir:
        settings.storage.artifacts_dir = Path(tmp_dir) / "artifacts"

        # Create a dummy cbz
        cbz_path = Path(tmp_dir) / "my_manga.cbz"
        xml_data = b"""<?xml version="1.0" encoding="utf-8"?>
<ComicInfo>
  <Title>My Manga Volume 1</Title>
  <Writer>Manga Writer</Writer>
  <Number>1</Number>
  <Publisher>Shonen Jump</Publisher>
</ComicInfo>
"""
        with zipfile.ZipFile(cbz_path, "w") as z:
            z.writestr("ComicInfo.xml", xml_data)
            z.writestr("page001.jpg", b"fake image bytes")

        book = BookRecord(
            book_key="path:my_manga.cbz",
            run_id="test_run",
            source="direct_path",
            files=[{"path": str(cbz_path), "format": "cbz"}],
            current_metadata={"title": "Original Title"},
        )

        # Mock out LLM call for cover verification since we just want to verify extraction
        with patch("calibre_ai_auditor.ocr.vision.VisionVerifier.verify_cover", return_value=None):
            evidence = await build_evidence_package(book, settings)

        # Extract title from candidates or extracted
        extracted_items = evidence.extracted
        assert extracted_items.get("title") == "My Manga Volume 1"
        assert extracted_items.get("authors") == ["Manga Writer"]

        # Verify cover was extracted
        assert evidence.cover is not None
        cover_path = Path(evidence.cover["embedded_cover_path"])
        assert cover_path.exists()
        assert cover_path.read_bytes() == b"fake image bytes"


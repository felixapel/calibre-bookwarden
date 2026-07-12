import tempfile
import zipfile
from pathlib import Path

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

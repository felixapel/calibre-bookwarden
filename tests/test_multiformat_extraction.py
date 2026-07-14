from __future__ import annotations

import hashlib
import zipfile
from pathlib import Path

import fitz

from calibre_ai_auditor.extractors.multiformat import FormatConverter, inspect_format
from calibre_ai_auditor.verification.identity_v2 import EvidenceSourceKind, FormatEvidenceStatus

ISBN = "9780306406157"


def _write_epub(path: Path) -> None:
    container = """<?xml version="1.0"?>
<container xmlns="urn:oasis:names:tc:opendocument:xmlns:container" version="1.0">
  <rootfiles><rootfile full-path="OPS/package.opf" media-type="application/oebps-package+xml"/></rootfiles>
</container>
"""
    opf = f"""<?xml version="1.0"?>
<package xmlns="http://www.idpf.org/2007/opf" unique-identifier="book-id" version="3.0">
  <metadata xmlns:dc="http://purl.org/dc/elements/1.1/">
    <dc:identifier id="book-id">urn:isbn:{ISBN}</dc:identifier>
    <dc:title>The Exact Book</dc:title>
    <dc:creator>Ada Author</dc:creator>
    <dc:language>eng</dc:language>
    <dc:publisher>Correct Press</dc:publisher>
    <dc:date>2024-05-06</dc:date>
  </metadata>
  <manifest>
    <item id="title" href="z-title.xhtml" media-type="application/xhtml+xml"/>
    <item id="body" href="a-body.xhtml" media-type="application/xhtml+xml"/>
  </manifest>
  <spine><itemref idref="title"/><itemref idref="body"/></spine>
</package>
"""
    title_page = """<html><body><h1>The Exact Book</h1><p>Ada Author</p></body></html>"""
    body = """<html><body><h1>Bibliography</h1><p>Another book ISBN 9783161484100.</p></body></html>"""
    with zipfile.ZipFile(path, "w") as archive:
        archive.writestr("mimetype", "application/epub+zip", compress_type=zipfile.ZIP_STORED)
        archive.writestr("META-INF/container.xml", container)
        archive.writestr("OPS/package.opf", opf)
        archive.writestr("OPS/z-title.xhtml", title_page)
        archive.writestr("OPS/a-body.xhtml", body)


def test_epub_inspection_uses_package_and_spine_without_accepting_bibliography_isbn(tmp_path: Path) -> None:
    epub = tmp_path / "book.epub"
    _write_epub(epub)
    before = hashlib.sha256(epub.read_bytes()).hexdigest()

    inspection = inspect_format(epub)

    assert inspection.format_evidence.status is FormatEvidenceStatus.readable
    assert inspection.format_evidence.identifiers == {"isbn": ISBN}
    assert inspection.format_evidence.title == "The Exact Book"
    assert inspection.format_evidence.authors == ["Ada Author"]
    assert inspection.format_evidence.languages == ["eng"]
    assert inspection.metadata["publisher"] == "Correct Press"
    assert inspection.metadata["pubdate"] == "2024-05-06"
    assert inspection.snippets[0].locator.endswith("z-title.xhtml")
    assert all("9783161484100" not in str(item.value) for item in inspection.evidence)
    identifier = next(item for item in inspection.evidence if item.field == "identifiers")
    assert identifier.source_kind is EvidenceSourceKind.embedded_metadata
    assert identifier.manifestation_ids == {"isbn": ISBN}
    assert hashlib.sha256(epub.read_bytes()).hexdigest() == before


def test_epub_prefers_unique_front_matter_isbn_as_content_native_identity(tmp_path: Path) -> None:
    epub = tmp_path / "edition.epub"
    wrong_embedded_isbn = "9783161484100"
    container = """<?xml version="1.0"?>
<container xmlns="urn:oasis:names:tc:opendocument:xmlns:container" version="1.0">
  <rootfiles><rootfile full-path="OPS/package.opf" media-type="application/oebps-package+xml"/></rootfiles>
</container>
"""
    opf = f"""<?xml version="1.0"?>
<package xmlns="http://www.idpf.org/2007/opf" version="3.0">
  <metadata xmlns:dc="http://purl.org/dc/elements/1.1/">
    <dc:identifier>{wrong_embedded_isbn}</dc:identifier><dc:title>The Exact Book</dc:title>
  </metadata>
  <manifest><item id="copyright" href="copyright.xhtml" media-type="application/xhtml+xml"/></manifest>
  <spine><itemref idref="copyright"/></spine>
</package>
"""
    with zipfile.ZipFile(epub, "w") as archive:
        archive.writestr("META-INF/container.xml", container)
        archive.writestr("OPS/package.opf", opf)
        archive.writestr(
            "OPS/copyright.xhtml",
            f"<html><body>Copyright 2024. First edition. ISBN {ISBN}</body></html>",
        )

    inspection = inspect_format(epub)

    assert inspection.format_evidence.identifiers == {"isbn": ISBN}
    content_identifier = next(
        item
        for item in inspection.evidence
        if item.field == "identifiers" and item.source_kind is EvidenceSourceKind.content_native
    )
    assert content_identifier.value == {"isbn": ISBN}
    embedded_identifier = next(
        item
        for item in inspection.evidence
        if item.field == "identifiers" and item.source_kind is EvidenceSourceKind.embedded_metadata
    )
    assert embedded_identifier.value == {"isbn": wrong_embedded_isbn}


def test_corrupt_epub_becomes_explicit_corrupt_evidence(tmp_path: Path) -> None:
    epub = tmp_path / "broken.epub"
    epub.write_bytes(b"not a zip")

    inspection = inspect_format(epub)

    assert inspection.format_evidence.status is FormatEvidenceStatus.corrupt
    assert inspection.evidence == []
    assert inspection.format_evidence.error


def test_unknown_format_is_reported_as_unsupported_without_mutation(tmp_path: Path) -> None:
    book = tmp_path / "book.txt"
    book.write_text("The Exact Book")
    before = book.read_bytes()

    inspection = inspect_format(book)

    assert inspection.format_evidence.status is FormatEvidenceStatus.unsupported
    assert inspection.format_evidence.sha256 == hashlib.sha256(before).hexdigest()
    assert book.read_bytes() == before


def test_pdf_inspection_anchors_isbn_to_copyright_page_and_preserves_file(tmp_path: Path) -> None:
    pdf = tmp_path / "book.pdf"
    document = fitz.open()
    title_page = document.new_page()
    title_page.insert_text((72, 72), "The Exact Book\nAda Author")
    copyright_page = document.new_page()
    copyright_page.insert_text((72, 72), f"Copyright 2024 Correct Press\nISBN {ISBN}")
    document.set_metadata({"title": "The Exact Book", "author": "Ada Author"})
    document.save(pdf)
    document.close()
    before = hashlib.sha256(pdf.read_bytes()).hexdigest()

    inspection = inspect_format(pdf)

    assert inspection.format_evidence.status is FormatEvidenceStatus.readable
    assert inspection.format_evidence.identifiers == {"isbn": ISBN}
    assert inspection.format_evidence.title == "The Exact Book"
    assert inspection.format_evidence.authors == ["Ada Author"]
    identifier = next(item for item in inspection.evidence if item.field == "identifiers")
    assert identifier.source_kind is EvidenceSourceKind.content_native
    assert identifier.locator == "page:2"
    assert hashlib.sha256(pdf.read_bytes()).hexdigest() == before


def test_cbz_inspection_reads_comicinfo_without_extracting_archive(tmp_path: Path) -> None:
    cbz = tmp_path / "book.cbz"
    comic_info = f"""<?xml version="1.0"?>
<ComicInfo>
  <Title>The Exact Book</Title>
  <Writer>Ada Author</Writer>
  <LanguageISO>eng</LanguageISO>
  <GTIN>{ISBN}</GTIN>
</ComicInfo>
"""
    with zipfile.ZipFile(cbz, "w") as archive:
        archive.writestr("ComicInfo.xml", comic_info)
        archive.writestr("001.jpg", b"not-decoded-by-metadata-inspection")
    before = cbz.read_bytes()

    inspection = inspect_format(cbz)

    assert inspection.format_evidence.status is FormatEvidenceStatus.readable
    assert inspection.format_evidence.identifiers == {"isbn": ISBN}
    assert inspection.format_evidence.title == "The Exact Book"
    assert inspection.format_evidence.authors == ["Ada Author"]
    assert cbz.read_bytes() == before


class _FakeConverter(FormatConverter):
    def convert(self, source: Path, target: Path) -> None:
        assert source.suffix == ".mobi"
        _write_epub(target)


def test_mobi_is_converted_in_temporary_space_and_original_is_unchanged(tmp_path: Path) -> None:
    mobi = tmp_path / "book.mobi"
    mobi.write_bytes(b"fake mobi source bytes")
    before = mobi.read_bytes()

    inspection = inspect_format(mobi, converter=_FakeConverter())

    assert inspection.format_evidence.status is FormatEvidenceStatus.readable
    assert inspection.format_evidence.format == "MOBI"
    assert inspection.format_evidence.path == str(mobi)
    assert inspection.format_evidence.identifiers == {"isbn": ISBN}
    assert mobi.read_bytes() == before

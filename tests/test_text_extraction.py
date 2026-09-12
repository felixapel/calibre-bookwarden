"""EPUB snippet path must cap zip members (zip-bomb guard)."""

import zipfile
from pathlib import Path

from calibre_ai_auditor.extractors.text import extract_snippets


def _write_epub(path: Path, members: dict[str, bytes]) -> None:
    with zipfile.ZipFile(path, "w", compression=zipfile.ZIP_DEFLATED) as z:
        z.writestr("mimetype", "application/epub+zip")
        for name, data in members.items():
            z.writestr(name, data)


def test_zip_bomb_member_skipped_without_oom(tmp_path: Path) -> None:
    epub = tmp_path / "bomb.epub"
    _write_epub(
        epub,
        {
            "OEBPS/bomb.xhtml": b"<html><body>" + b"0" * (20 * 1024 * 1024) + b"</body></html>",
            "OEBPS/real.xhtml": b"<html><body><p>Actual book text here.</p></body></html>",
        },
    )

    snippets = extract_snippets(epub)

    assert snippets, "readable member must still yield snippets"
    combined = " ".join(s.text for s in snippets)
    assert "Actual book text here." in combined
    assert "0" * 100 not in combined


def test_normal_epub_still_extracts(tmp_path: Path) -> None:
    epub = tmp_path / "normal.epub"
    _write_epub(epub, {"OEBPS/ch1.xhtml": b"<html><body><p>Hello world.</p></body></html>"})

    snippets = extract_snippets(epub)

    assert len(snippets) == 1
    assert "Hello world." in snippets[0].text

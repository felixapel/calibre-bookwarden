from pathlib import Path
from unittest.mock import MagicMock

from calibre_ai_auditor.calibre.cli import CalibreCLI


def test_set_metadata_clears_optional_fields_absent_from_full_opf(tmp_path: Path) -> None:
    opf = tmp_path / "original.opf"
    opf.write_text(
        """<?xml version="1.0"?>
<package xmlns="http://www.idpf.org/2007/opf">
  <metadata xmlns:dc="http://purl.org/dc/elements/1.1/">
    <dc:title>Original Title</dc:title>
  </metadata>
</package>
"""
    )
    cli = CalibreCLI(tmp_path / "library")
    cli._run_command = MagicMock()  # type: ignore[method-assign]

    cli.set_metadata(1, opf)

    commands = [call.args[0] for call in cli._run_command.call_args_list]
    assert commands[0][:3] == ["calibredb", "set_metadata", "1"]
    assert "publisher:" in commands[0]
    assert "pubdate:" in commands[0]
    assert "languages:" in commands[0]
    assert "series:" in commands[0]
    assert "identifiers:" in commands[0]
    assert commands[1] == [
        "calibredb",
        "set_metadata",
        "1",
        str(opf),
        "--with-library",
        str(tmp_path / "library"),
    ]

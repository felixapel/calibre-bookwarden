import os
from pathlib import Path
from unittest.mock import MagicMock

import pytest

from calibre_ai_auditor.calibre.cli import CalibreCLI, CalibreCLIError


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


def test_custom_column_and_cover_commands_are_capability_checked(tmp_path: Path) -> None:
    cli = CalibreCLI(tmp_path / "library")
    cli._run_command = MagicMock()  # type: ignore[method-assign]
    cli._run_command.side_effect = [
        MagicMock(stdout="edition (label: edition)\n"),
        MagicMock(stdout=""),
        MagicMock(stdout="title\nauthors\ncover\n"),
        MagicMock(stdout=""),
    ]
    cover = tmp_path / "cover.jpg"
    cover.write_bytes(b"image")

    cli.set_custom(7, "#edition", "First edition")
    cli.set_cover(7, cover)

    commands = [call.args[0] for call in cli._run_command.call_args_list]
    assert commands[0] == [
        "calibredb",
        "custom_columns",
        "--with-library",
        str(tmp_path / "library"),
    ]
    assert commands[1] == [
        "calibredb",
        "set_custom",
        "edition",
        "7",
        "First edition",
        "--with-library",
        str(tmp_path / "library"),
    ]
    assert commands[2][:3] == ["calibredb", "set_metadata", "--list-fields"]
    assert commands[3][0:4] == ["calibredb", "set_metadata", "7", "--field"]
    assert commands[3][4] == f"cover:{cover}"


def test_missing_custom_column_is_rejected(tmp_path: Path) -> None:
    cli = CalibreCLI(tmp_path / "library")
    cli._run_command = MagicMock(return_value=MagicMock(stdout="rating (label: rating)\n"))  # type: ignore[method-assign]

    with pytest.raises(CalibreCLIError, match="edition"):
        cli.set_custom(7, "#edition", "First edition")


def test_descriptor_metadata_write_passes_only_the_verified_fd_to_calibre(tmp_path: Path) -> None:
    opf = tmp_path / "verified.opf"
    opf.write_text(
        """<package xmlns="http://www.idpf.org/2007/opf">
  <metadata xmlns:dc="http://purl.org/dc/elements/1.1/"><dc:title>Verified</dc:title></metadata>
</package>"""
    )
    cli = CalibreCLI(tmp_path / "library")
    cli._run_command = MagicMock()  # type: ignore[method-assign]
    descriptor = os.open(opf, os.O_RDONLY)
    try:
        cli.set_metadata_from_fd(3, descriptor)
    finally:
        os.close(descriptor)

    final_call = cli._run_command.call_args_list[-1]
    assert final_call.args[0][3] == f"/proc/self/fd/{descriptor}"
    assert final_call.kwargs["pass_fds"] == (descriptor,)

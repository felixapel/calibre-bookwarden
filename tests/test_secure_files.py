from __future__ import annotations

import hashlib
import os
import subprocess
from pathlib import Path

import pytest

from calibre_ai_auditor.apply.artifacts import set_metadata_from_artifact
from calibre_ai_auditor.calibre.cli import CalibreCLI
from calibre_ai_auditor.security.files import (
    SecurePathError,
    ensure_secure_directory,
    open_file_beneath,
    sealed_file_beneath,
)

pytestmark = pytest.mark.skipif(
    os.name == "nt",
    reason="POSIX-specific /proc/self/fd file sealing and unprivileged symlinks not available on Windows",
)


def test_open_file_beneath_rejects_symlinked_parent(tmp_path: Path) -> None:
    root = tmp_path / "library"
    outside = tmp_path / "outside"
    root.mkdir()
    outside.mkdir()
    (outside / "book.epub").write_bytes(b"wrong manifestation")
    (root / "swapped").symlink_to(outside, target_is_directory=True)

    with (
        pytest.raises(SecurePathError, match="symlink|directory component"),
        open_file_beneath(root, root / "swapped" / "book.epub"),
    ):
        pass


def test_ensure_secure_directory_rejects_symlinked_component(tmp_path: Path) -> None:
    artifacts = tmp_path / "artifacts"
    outside = tmp_path / "outside"
    artifacts.mkdir()
    outside.mkdir()
    (artifacts / "backups").symlink_to(outside, target_is_directory=True)

    with pytest.raises(SecurePathError, match="symlink|directory component"):
        ensure_secure_directory(artifacts / "backups" / "1")


def test_sealed_file_keeps_verified_bytes_after_path_replacement(tmp_path: Path) -> None:
    artifacts = tmp_path / "artifacts"
    artifacts.mkdir()
    artifact = artifacts / "before.opf"
    original = b"verified rollback bytes"
    artifact.write_bytes(original)

    with sealed_file_beneath(
        artifacts,
        artifact,
        expected_sha256=hashlib.sha256(original).hexdigest(),
    ) as descriptor:
        artifact.unlink()
        artifact.write_bytes(b"replacement after verification")
        assert os.read(descriptor, len(original) + 1) == original
        with pytest.raises(OSError, match="Operation not permitted"):
            os.write(descriptor, b"tamper")


def test_calibre_handoff_reads_sealed_bytes_after_artifact_name_is_replaced(tmp_path: Path) -> None:
    artifacts = tmp_path / "artifacts"
    artifacts.mkdir()
    artifact = artifacts / "target.opf"
    original = b"""<package xmlns="http://www.idpf.org/2007/opf">
  <metadata xmlns:dc="http://purl.org/dc/elements/1.1/"><dc:title>Verified</dc:title></metadata>
</package>"""
    artifact.write_bytes(original)
    observed: list[bytes] = []
    cli = CalibreCLI(tmp_path / "library")

    def run_command(
        cmd: list[str],
        capture_output: bool = True,
        timeout: float = 30.0,
        pass_fds: tuple[int, ...] = (),
    ) -> subprocess.CompletedProcess[str]:
        del capture_output, timeout, pass_fds
        if len(cmd) > 3 and cmd[3].startswith("/proc/self/fd/"):
            artifact.write_bytes(b"replacement after hash")
            observed.append(Path(cmd[3]).read_bytes())
        return subprocess.CompletedProcess(cmd, 0, "", "")

    cli._run_command = run_command  # type: ignore[method-assign]
    set_metadata_from_artifact(
        cli,
        1,
        artifacts,
        artifact,
        hashlib.sha256(original).hexdigest(),
    )

    assert observed == [original]
    assert artifact.read_bytes() == b"replacement after hash"

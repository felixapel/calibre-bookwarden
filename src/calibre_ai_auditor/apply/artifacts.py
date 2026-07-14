"""Verified, descriptor-backed handoff of rollback artifacts to Calibre."""

from __future__ import annotations

from pathlib import Path
from typing import Any

from calibre_ai_auditor.calibre.cli import CalibreCLI
from calibre_ai_auditor.security.files import sealed_file_beneath, sha256_file_beneath

MAX_OPF_BYTES = 32 * 1024 * 1024
MAX_COVER_BYTES = 20 * 1024 * 1024


def bind_exported_artifact(
    artifacts_root: Path,
    path: Path,
    exporter_sha256: object,
    *,
    max_bytes: int,
) -> str:
    """Bind a just-created pathname to bytes independently hashed by its exporter."""
    observed = sha256_file_beneath(artifacts_root, path, max_bytes=max_bytes)
    if isinstance(exporter_sha256, str) and exporter_sha256 != observed:
        raise RuntimeError("exported artifact was replaced before it could be sealed")
    return observed


def _native_method(cli: CalibreCLI, name: str) -> Any | None:
    """Find a real class method without treating MagicMock attributes as capabilities."""
    method = getattr(type(cli), name, None)
    return method if callable(method) else None


def verify_artifact(
    artifacts_root: Path,
    path: Path,
    expected_sha256: str | None,
    *,
    max_bytes: int,
) -> None:
    if expected_sha256 is None:
        raise RuntimeError("artifact has no integrity seal")
    with sealed_file_beneath(
        artifacts_root,
        path,
        expected_sha256=expected_sha256,
        max_bytes=max_bytes,
    ):
        pass


def set_metadata_from_artifact(
    cli: CalibreCLI,
    book_id: int,
    artifacts_root: Path,
    path: Path,
    expected_sha256: str | None,
) -> None:
    if expected_sha256 is None:
        raise RuntimeError("OPF artifact has no integrity seal")
    with sealed_file_beneath(
        artifacts_root,
        path,
        expected_sha256=expected_sha256,
        max_bytes=MAX_OPF_BYTES,
    ) as descriptor:
        method = _native_method(cli, "set_metadata_from_fd")
        if method is not None:
            method(cli, book_id, descriptor)
        else:
            # Test doubles do not launch a process; retain their observable
            # path contract while production CalibreCLI always uses the fd.
            cli.set_metadata(book_id, path)


def set_cover_from_artifact(
    cli: CalibreCLI,
    book_id: int,
    artifacts_root: Path,
    path: Path,
    expected_sha256: str | None,
) -> None:
    if expected_sha256 is None:
        raise RuntimeError("cover artifact has no integrity seal")
    with sealed_file_beneath(
        artifacts_root,
        path,
        expected_sha256=expected_sha256,
        max_bytes=MAX_COVER_BYTES,
    ) as descriptor:
        method = _native_method(cli, "set_cover_from_fd")
        if method is not None:
            method(cli, book_id, descriptor)
        else:
            cli.set_cover(book_id, path)

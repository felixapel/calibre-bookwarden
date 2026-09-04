"""Descriptor-anchored file access for untrusted library and artifact paths.

``Path.resolve()`` plus ``O_NOFOLLOW`` on the final component still leaves a
race in every parent directory.  These helpers walk from an already-open root
with ``openat(2)`` semantics and reject symlinks at every component on Linux.
On Windows/non-Linux platforms, cross-platform containment and path validation
are used.
"""

from __future__ import annotations

import ctypes
import hashlib
import os
import stat
import sys
import tempfile
from collections.abc import Iterator
from contextlib import contextmanager, suppress
from pathlib import Path
from uuid import uuid4

try:
    import fcntl
except ImportError:
    fcntl = None  # type: ignore[assignment]


class SecurePathError(RuntimeError):
    """A path escaped its root or could not be opened without following links."""


_MFD_CLOEXEC = 0x0001
_MFD_ALLOW_SEALING = 0x0002
_F_ADD_SEALS = 1033
_F_SEAL_SEAL = 0x0001
_F_SEAL_SHRINK = 0x0002
_F_SEAL_GROW = 0x0004
_F_SEAL_WRITE = 0x0008


def _create_sealable_memfd() -> int:
    native = getattr(os, "memfd_create", None)
    if native is not None:
        return int(native("bookaudit-verified-artifact", _MFD_CLOEXEC | _MFD_ALLOW_SEALING))
    if sys.platform != "win32":
        try:
            libc = ctypes.CDLL(None, use_errno=True)
            create = getattr(libc, "memfd_create", None)
            if create is not None:
                create.argtypes = [ctypes.c_char_p, ctypes.c_uint]
                create.restype = ctypes.c_int
                descriptor = int(create(b"bookaudit-verified-artifact", _MFD_CLOEXEC | _MFD_ALLOW_SEALING))
                if descriptor >= 0:
                    return descriptor
        except Exception:
            pass
    # Windows or non-Linux fallback: anonymous temp file descriptor
    tmp = tempfile.TemporaryFile()
    return os.dup(tmp.fileno())


def _absolute_lexical(path: Path) -> Path:
    return Path(os.path.normpath(os.path.abspath(os.fspath(path))))


def _directory_flags() -> int:
    return os.O_RDONLY | getattr(os, "O_DIRECTORY", 0) | getattr(os, "O_CLOEXEC", 0) | getattr(os, "O_NOFOLLOW", 0)


def _open_directory_from_root(path: Path, *, create: bool = False, mode: int = 0o700) -> int:
    absolute = _absolute_lexical(path)
    if sys.platform == "win32":
        if create:
            os.makedirs(absolute, exist_ok=True)
        flags = os.O_RDONLY | getattr(os, "O_CLOEXEC", 0)
        return os.open(str(absolute), flags)

    descriptor = os.open("/", _directory_flags())
    try:
        for component in absolute.parts[1:]:
            try:
                child = os.open(component, _directory_flags(), dir_fd=descriptor)
            except FileNotFoundError:
                if not create:
                    raise
                with suppress(FileExistsError):
                    os.mkdir(component, mode=mode, dir_fd=descriptor)
                child = os.open(component, _directory_flags(), dir_fd=descriptor)
            os.close(descriptor)
            descriptor = child
    except OSError as exc:
        os.close(descriptor)
        raise SecurePathError(f"unsafe or symlinked directory component in {absolute}: {exc}") from exc
    return descriptor


def ensure_secure_directory(path: Path, *, mode: int = 0o700) -> Path:
    """Create/open a directory without following a symlink in any component."""
    absolute = _absolute_lexical(path)
    descriptor = _open_directory_from_root(absolute, create=True, mode=mode)
    os.close(descriptor)
    return absolute


def _relative_candidate(root: Path, candidate: Path) -> tuple[Path, Path, Path]:
    absolute_root = _absolute_lexical(root)
    absolute_candidate = _absolute_lexical(candidate)
    try:
        relative = absolute_candidate.relative_to(absolute_root)
    except ValueError as exc:
        raise SecurePathError(f"path is outside its configured root: {absolute_candidate}") from exc
    if relative == Path(".") or not relative.parts:
        raise SecurePathError("a directory root cannot be consumed as a regular file")
    return absolute_root, absolute_candidate, relative


def _open_parent_beneath(root: Path, candidate: Path) -> tuple[int, Path, str]:
    absolute_root, absolute_candidate, relative = _relative_candidate(root, candidate)
    if sys.platform == "win32":
        parent = absolute_candidate.parent
        os.makedirs(parent, exist_ok=True)
        flags = os.O_RDONLY | getattr(os, "O_CLOEXEC", 0)
        directory = os.open(str(parent), flags)
        return directory, absolute_candidate, relative.parts[-1]

    directory = _open_directory_from_root(absolute_root)
    try:
        for component in relative.parts[:-1]:
            child = os.open(component, _directory_flags(), dir_fd=directory)
            os.close(directory)
            directory = child
    except OSError as exc:
        os.close(directory)
        raise SecurePathError(f"unsafe or symlinked parent for {absolute_candidate}: {exc}") from exc
    return directory, absolute_candidate, relative.parts[-1]


@contextmanager
def open_file_beneath(
    root: Path,
    candidate: Path,
    *,
    max_bytes: int | None = None,
) -> Iterator[int]:
    """Open one regular file beneath ``root`` using no-follow ``openat`` steps on POSIX."""
    absolute_root, absolute_candidate, relative = _relative_candidate(root, candidate)
    if sys.platform == "win32":
        if not absolute_candidate.is_file():
            raise SecurePathError(f"path is not a regular file: {absolute_candidate}")
        if absolute_candidate.is_symlink():
            raise SecurePathError(f"unsafe or symlinked file path {absolute_candidate}")
        size = absolute_candidate.stat().st_size
        if max_bytes is not None and size > max_bytes:
            raise SecurePathError(f"file exceeds the permitted size: {absolute_candidate}")
        descriptor = os.open(str(absolute_candidate), os.O_RDONLY | getattr(os, "O_BINARY", 0))
        try:
            yield descriptor
        finally:
            os.close(descriptor)
        return

    directory = _open_directory_from_root(absolute_root)
    descriptor: int | None = None
    try:
        for component in relative.parts[:-1]:
            child = os.open(component, _directory_flags(), dir_fd=directory)
            os.close(directory)
            directory = child
        flags = os.O_RDONLY | getattr(os, "O_CLOEXEC", 0) | getattr(os, "O_NOFOLLOW", 0) | getattr(os, "O_NONBLOCK", 0)
        descriptor = os.open(relative.parts[-1], flags, dir_fd=directory)
        opened = os.fstat(descriptor)
        if not stat.S_ISREG(opened.st_mode):
            raise SecurePathError(f"path is not a regular file: {absolute_candidate}")
        if max_bytes is not None and opened.st_size > max_bytes:
            raise SecurePathError(f"file exceeds the permitted size: {absolute_candidate}")
    except OSError as exc:
        if descriptor is not None:
            os.close(descriptor)
            descriptor = None
        raise SecurePathError(f"unsafe or symlinked file path {absolute_candidate}: {exc}") from exc
    except Exception:
        if descriptor is not None:
            os.close(descriptor)
            descriptor = None
        raise
    finally:
        os.close(directory)
    try:
        assert descriptor is not None
        yield descriptor
    finally:
        if descriptor is not None:
            os.close(descriptor)


@contextmanager
def create_file_beneath(
    root: Path,
    candidate: Path,
    *,
    mode: int = 0o600,
) -> Iterator[int]:
    """Exclusively create a regular file beneath a securely opened root."""
    absolute_root, absolute_candidate, relative = _relative_candidate(root, candidate)
    if sys.platform == "win32":
        parent = absolute_candidate.parent
        os.makedirs(parent, exist_ok=True)
        flags = os.O_WRONLY | os.O_CREAT | os.O_EXCL | getattr(os, "O_BINARY", 0)
        descriptor = os.open(str(absolute_candidate), flags, mode)
        try:
            yield descriptor
        finally:
            os.close(descriptor)
        return

    directory = _open_directory_from_root(absolute_root)
    descriptor: int | None = None
    try:
        for component in relative.parts[:-1]:
            child = os.open(component, _directory_flags(), dir_fd=directory)
            os.close(directory)
            directory = child
        flags = os.O_WRONLY | os.O_CREAT | os.O_EXCL | getattr(os, "O_CLOEXEC", 0) | getattr(os, "O_NOFOLLOW", 0)
        descriptor = os.open(relative.parts[-1], flags, mode, dir_fd=directory)
        if not stat.S_ISREG(os.fstat(descriptor).st_mode):
            raise SecurePathError(f"created path is not a regular file: {absolute_candidate}")
    except OSError as exc:
        if descriptor is not None:
            os.close(descriptor)
            descriptor = None
        raise SecurePathError(f"could not securely create {absolute_candidate}: {exc}") from exc
    except Exception:
        if descriptor is not None:
            os.close(descriptor)
            descriptor = None
        raise
    finally:
        os.close(directory)
    try:
        assert descriptor is not None
        yield descriptor
    finally:
        if descriptor is not None:
            os.close(descriptor)


def _identity(descriptor: int) -> tuple[int, int, int, int, int]:
    metadata = os.fstat(descriptor)
    return (
        metadata.st_dev,
        metadata.st_ino,
        metadata.st_size,
        getattr(metadata, "st_mtime_ns", int(metadata.st_mtime * 1e9)),
        getattr(metadata, "st_ctime_ns", int(metadata.st_ctime * 1e9)),
    )


def _write_all(descriptor: int, payload: bytes) -> None:
    offset = 0
    while offset < len(payload):
        written = os.write(descriptor, payload[offset:])
        if written <= 0:
            raise SecurePathError("short write while materializing a protected file")
        offset += written


def copy_file_beneath(
    root: Path,
    candidate: Path,
    target: Path,
    *,
    max_bytes: int | None = None,
    expected_sha256: str | None = None,
    target_root: Path | None = None,
) -> str:
    """Copy a stable rooted source to a private destination and return its hash."""
    try:
        with open_file_beneath(root, candidate, max_bytes=max_bytes) as source:
            before = _identity(source)
            digest = hashlib.sha256()
            copied = 0
            if target_root is None:
                output_descriptor = os.open(
                    str(target),
                    os.O_WRONLY | os.O_CREAT | os.O_EXCL | getattr(os, "O_CLOEXEC", 0) | getattr(os, "O_BINARY", 0),
                    0o600,
                )
                output_context = None
            else:
                output_context = create_file_beneath(target_root, target)
                output_descriptor = output_context.__enter__()
            try:
                while chunk := os.read(source, 1024 * 1024):
                    copied += len(chunk)
                    if max_bytes is not None and copied > max_bytes:
                        raise SecurePathError("file grew beyond the permitted size while being copied")
                    digest.update(chunk)
                    _write_all(output_descriptor, chunk)
            finally:
                if output_context is None:
                    os.close(output_descriptor)
                else:
                    output_context.__exit__(None, None, None)
            if _identity(source) != before:
                raise SecurePathError("file changed while creating its stable copy")
            actual = digest.hexdigest()
            if expected_sha256 is not None and actual != expected_sha256:
                raise SecurePathError("file hash does not match the sealed evidence")
            return actual
    except Exception:
        if target_root is None:
            target.unlink(missing_ok=True)
        raise


def read_file_beneath(
    root: Path,
    candidate: Path,
    *,
    max_bytes: int | None = None,
    expected_sha256: str | None = None,
) -> bytes:
    """Read stable bytes through a rooted descriptor, optionally checking a seal."""
    with open_file_beneath(root, candidate, max_bytes=max_bytes) as descriptor:
        before = _identity(descriptor)
        payload = bytearray()
        digest = hashlib.sha256()
        while chunk := os.read(descriptor, 1024 * 1024):
            payload.extend(chunk)
            digest.update(chunk)
            if max_bytes is not None and len(payload) > max_bytes:
                raise SecurePathError("file grew beyond the permitted size while being read")
        if _identity(descriptor) != before:
            raise SecurePathError("file changed while it was being read")
        if expected_sha256 is not None and digest.hexdigest() != expected_sha256:
            raise SecurePathError("file hash does not match the sealed evidence")
        return bytes(payload)


def write_bytes_beneath(root: Path, candidate: Path, payload: bytes, *, mode: int = 0o600) -> None:
    """Exclusively create a rooted file and write all bytes through its descriptor."""
    with create_file_beneath(root, candidate, mode=mode) as descriptor:
        _write_all(descriptor, payload)
        os.fsync(descriptor)


def replace_bytes_beneath(root: Path, candidate: Path, payload: bytes, *, mode: int = 0o600) -> None:
    """Atomically replace a rooted file without resolving either parent by name."""
    if sys.platform == "win32":
        absolute_root, absolute_candidate, relative = _relative_candidate(root, candidate)
        temporary = absolute_candidate.parent / f".{absolute_candidate.name}.tmp-{uuid4().hex}"
        flags = os.O_WRONLY | os.O_CREAT | os.O_EXCL | getattr(os, "O_BINARY", 0)
        descriptor = os.open(str(temporary), flags, mode)
        try:
            _write_all(descriptor, payload)
            os.fsync(descriptor)
            os.close(descriptor)
            descriptor = -1
            os.replace(str(temporary), str(absolute_candidate))
        finally:
            if descriptor != -1:
                os.close(descriptor)
            temporary.unlink(missing_ok=True)
        return

    directory, absolute_candidate, name = _open_parent_beneath(root, candidate)
    temporary = f".{name}.tmp-{uuid4().hex}"
    descriptor: int | None = None
    try:
        flags = os.O_WRONLY | os.O_CREAT | os.O_EXCL | getattr(os, "O_CLOEXEC", 0) | getattr(os, "O_NOFOLLOW", 0)
        descriptor = os.open(temporary, flags, mode, dir_fd=directory)
        _write_all(descriptor, payload)
        os.fsync(descriptor)
        os.close(descriptor)
        descriptor = None
        os.replace(temporary, name, src_dir_fd=directory, dst_dir_fd=directory)
        os.fsync(directory)
    except OSError as exc:
        if descriptor is not None:
            os.close(descriptor)
        with suppress(FileNotFoundError):
            os.unlink(temporary, dir_fd=directory)
        raise SecurePathError(f"could not securely replace {absolute_candidate}: {exc}") from exc
    finally:
        os.close(directory)


def copy_file_replacing_beneath(
    source_root: Path,
    source_path: Path,
    target_root: Path,
    target_path: Path,
    *,
    max_bytes: int | None = None,
) -> str:
    """Stream a rooted source into an atomic rooted replacement."""
    if sys.platform == "win32":
        _, absolute_target, _ = _relative_candidate(target_root, target_path)
        temporary = absolute_target.parent / f".{absolute_target.name}.tmp-{uuid4().hex}"
        flags = os.O_WRONLY | os.O_CREAT | os.O_EXCL | getattr(os, "O_BINARY", 0)
        output = os.open(str(temporary), flags, 0o600)
        try:
            with open_file_beneath(source_root, source_path, max_bytes=max_bytes) as source:
                before = _identity(source)
                digest = hashlib.sha256()
                copied = 0
                while chunk := os.read(source, 1024 * 1024):
                    copied += len(chunk)
                    if max_bytes is not None and copied > max_bytes:
                        raise SecurePathError("source grew beyond the permitted size")
                    digest.update(chunk)
                    _write_all(output, chunk)
                if _identity(source) != before:
                    raise SecurePathError("source changed while it was being copied")
            os.fsync(output)
            os.close(output)
            output = -1
            os.replace(str(temporary), str(absolute_target))
            return digest.hexdigest()
        finally:
            if output != -1:
                os.close(output)
            temporary.unlink(missing_ok=True)

    directory, absolute_target, name = _open_parent_beneath(target_root, target_path)
    temporary = f".{name}.tmp-{uuid4().hex}"
    output: int | None = None
    try:
        flags = os.O_WRONLY | os.O_CREAT | os.O_EXCL | getattr(os, "O_CLOEXEC", 0) | getattr(os, "O_NOFOLLOW", 0)
        output = os.open(temporary, flags, 0o600, dir_fd=directory)
        with open_file_beneath(source_root, source_path, max_bytes=max_bytes) as source:
            before = _identity(source)
            digest = hashlib.sha256()
            copied = 0
            while chunk := os.read(source, 1024 * 1024):
                copied += len(chunk)
                if max_bytes is not None and copied > max_bytes:
                    raise SecurePathError("source grew beyond the permitted size")
                digest.update(chunk)
                _write_all(output, chunk)
            if _identity(source) != before:
                raise SecurePathError("source changed while it was being copied")
        os.fsync(output)
        os.close(output)
        output = None
        os.replace(temporary, name, src_dir_fd=directory, dst_dir_fd=directory)
        os.fsync(directory)
        return digest.hexdigest()
    except OSError as exc:
        raise SecurePathError(f"could not securely copy to {absolute_target}: {exc}") from exc
    finally:
        if output is not None:
            os.close(output)
        with suppress(FileNotFoundError):
            os.unlink(temporary, dir_fd=directory)
        os.close(directory)


def sha256_file_beneath(
    root: Path,
    candidate: Path,
    *,
    max_bytes: int | None = None,
) -> str:
    """Hash a rooted regular file and reject in-place changes during the read."""
    with open_file_beneath(root, candidate, max_bytes=max_bytes) as descriptor:
        before = _identity(descriptor)
        digest = hashlib.sha256()
        while chunk := os.read(descriptor, 1024 * 1024):
            digest.update(chunk)
        if _identity(descriptor) != before:
            raise SecurePathError("file changed while its hash was being verified")
        return digest.hexdigest()


@contextmanager
def sealed_file_beneath(
    root: Path,
    candidate: Path,
    *,
    expected_sha256: str,
    max_bytes: int | None = None,
) -> Iterator[int]:
    """Yield an immutable descriptor containing exactly the verified artifact bytes."""
    sealed = _create_sealable_memfd()
    try:
        with open_file_beneath(root, candidate, max_bytes=max_bytes) as source:
            before = _identity(source)
            digest = hashlib.sha256()
            copied = 0
            while chunk := os.read(source, 1024 * 1024):
                copied += len(chunk)
                if max_bytes is not None and copied > max_bytes:
                    raise SecurePathError("artifact grew beyond the permitted size")
                digest.update(chunk)
                _write_all(sealed, chunk)
            if _identity(source) != before:
                raise SecurePathError("artifact changed while it was being sealed")
        if digest.hexdigest() != expected_sha256:
            raise SecurePathError("artifact hash mismatch")
        if fcntl is not None and hasattr(fcntl, "fcntl"):
            seals = _F_SEAL_SEAL | _F_SEAL_SHRINK | _F_SEAL_GROW | _F_SEAL_WRITE
            fcntl.fcntl(sealed, _F_ADD_SEALS, seals)
        os.lseek(sealed, 0, os.SEEK_SET)
        yield sealed
    finally:
        os.close(sealed)

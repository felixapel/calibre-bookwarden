"""Fail-closed, read-only access to a Calibre Content Server.

This module intentionally does not inherit from :class:`CalibreCLI`: exposing
metadata mutators on a remote live-library object would make the security
boundary depend on every caller behaving correctly.
"""

from __future__ import annotations

import hashlib
import json
import os
import re
import shutil
import stat
import subprocess
import sys
import tempfile
from collections.abc import Callable, Iterator, Sequence
from contextlib import contextmanager, suppress
from datetime import UTC, datetime
from pathlib import Path
from typing import Any
from urllib.parse import quote, urlsplit

try:
    import resource
except ImportError:
    resource = None  # type: ignore[assignment]

from pydantic import BaseModel, ConfigDict, Field, field_validator

from calibre_ai_auditor.security.files import ensure_secure_directory
from calibre_ai_auditor.verification.identity_v2 import validate_isbn

MAX_EXPORTED_FORMAT_BYTES = 2 * 1024 * 1024 * 1024
MAX_MACHINE_OUTPUT_BYTES = 256 * 1024 * 1024
DEFAULT_NETWORK_TIMEOUT_SECONDS = 120.0
EXPORT_TIMEOUT_SECONDS = 15 * 60.0
_SHA256_RE = re.compile(r"^[0-9a-f]{64}$")
_FORMAT_RE = re.compile(r"^[A-Z0-9]{1,16}$")
_LANGUAGE_RE = re.compile(r"^[A-Z]{2,3}$")
_INVENTORY_FORMATS = frozenset(
    {
        "AZW",
        "AZW3",
        "CBR",
        "CBZ",
        "DJVU",
        "DOCX",
        "EPUB",
        "FB2",
        "HTM",
        "HTML",
        "KFX",
        "LIT",
        "LRF",
        "MOBI",
        "ODT",
        "PDB",
        "PDF",
        "PRC",
        "RTF",
        "TCR",
        "TPZ",
        "TXT",
    }
)


class ContentServerError(RuntimeError):
    """A remote read failed closed without exposing remote output or secrets."""


CommandRunner = Callable[[list[str], str, float], subprocess.CompletedProcess[str]]


class InventoryCounts(BaseModel):
    model_config = ConfigDict(extra="forbid")

    books: int = Field(ge=0)
    formats: int = Field(ge=0)
    multi_format: int = Field(ge=0)
    no_format: int = Field(ge=0)


class ISBNCounts(BaseModel):
    model_config = ConfigDict(extra="forbid")

    invalid: int = Field(ge=0)
    missing: int = Field(ge=0)
    valid: int = Field(ge=0)


class LastModifiedRange(BaseModel):
    model_config = ConfigDict(extra="forbid")

    minimum: datetime | None = None
    maximum: datetime | None = None
    missing: int = Field(ge=0)
    invalid: int = Field(ge=0)


class InventoryReport(BaseModel):
    """Aggregate-only inventory contract; it cannot contain per-book metadata."""

    model_config = ConfigDict(extra="forbid")

    schema_version: int = 1
    captured_at: datetime
    source_fingerprint: str
    manifest_sha256: str
    counts: InventoryCounts
    formats: dict[str, int]
    languages: dict[str, int]
    isbn: ISBNCounts
    incomplete_fields: dict[str, int]
    last_modified: LastModifiedRange

    @field_validator("source_fingerprint", "manifest_sha256")
    @classmethod
    def _valid_digest(cls, value: str) -> str:
        lowered = value.lower()
        if not _SHA256_RE.fullmatch(lowered):
            raise ValueError("inventory fingerprints must be SHA-256 values")
        return lowered


def _set_file_limit(max_bytes: int) -> None:
    if resource is not None and hasattr(resource, "RLIMIT_FSIZE"):
        resource.setrlimit(resource.RLIMIT_FSIZE, (max_bytes, max_bytes))


def _subprocess_runner(command: list[str], password: str, timeout: float) -> subprocess.CompletedProcess[str]:
    try:
        is_machine_read = len(command) > 1 and command[1] == "list"
        max_file_bytes = MAX_MACHINE_OUTPUT_BYTES if is_machine_read else MAX_EXPORTED_FORMAT_BYTES
        preexec = (lambda: _set_file_limit(max_file_bytes)) if (os.name != "nt" and resource is not None) else None
        kwargs: dict[str, Any] = {}
        if preexec is not None:
            kwargs["preexec_fn"] = preexec

        cmd = list(command)
        if sys.platform == "win32" and cmd:
            target_bin = Path(cmd[0])
            if target_bin.is_file() and not target_bin.suffix:
                cmd = [sys.executable, str(target_bin)] + cmd[1:]

        with tempfile.TemporaryFile(mode="w+b") as output:
            result = subprocess.run(
                cmd,
                input=(password + "\n").encode(),
                stdout=output if is_machine_read else subprocess.DEVNULL,
                stderr=subprocess.DEVNULL,
                text=False,
                check=False,
                timeout=timeout,
                **kwargs,
            )
            if not is_machine_read:
                return subprocess.CompletedProcess(command, result.returncode, stdout="", stderr="")
            size = os.fstat(output.fileno()).st_size
            if size >= MAX_MACHINE_OUTPUT_BYTES:
                raise ContentServerError("Calibre list response exceeded the permitted size")
            output.seek(0)
            stdout = output.read(MAX_MACHINE_OUTPUT_BYTES).decode("utf-8")
            return subprocess.CompletedProcess(command, result.returncode, stdout=stdout, stderr="")
    except ContentServerError:
        raise
    except (OSError, UnicodeDecodeError, subprocess.TimeoutExpired):
        raise ContentServerError("Calibre Content Server read did not complete") from None


def _validate_text(value: str, *, field: str, max_length: int = 256) -> str:
    stripped = value.strip()
    if not stripped or len(stripped) > max_length or any(ord(char) < 32 for char in stripped):
        raise ValueError(f"{field} is invalid")
    return stripped


def _loopback_base_url(raw: str) -> str:
    try:
        parsed = urlsplit(raw)
        port = parsed.port
    except ValueError as exc:
        raise ValueError("content server must use an explicit loopback tunnel") from exc
    if (
        parsed.scheme != "http"
        or parsed.hostname != "127.0.0.1"
        or port is None
        or parsed.username is not None
        or parsed.password is not None
        or parsed.path not in {"", "/"}
        or parsed.query
        or parsed.fragment
    ):
        raise ValueError("content server must use an explicit loopback tunnel")
    return f"http://127.0.0.1:{port}"


def _validated_books(payload: object) -> list[dict[str, Any]]:
    if not isinstance(payload, list):
        raise ContentServerError("Calibre did not return a valid JSON book list")
    books: list[dict[str, Any]] = []
    seen: set[int] = set()
    for raw in payload:
        if not isinstance(raw, dict):
            raise ContentServerError("Calibre did not return a valid JSON book list")
        book_id = raw.get("id")
        if isinstance(book_id, bool) or not isinstance(book_id, int) or book_id <= 0:
            raise ContentServerError("Calibre returned an invalid book id")
        if book_id in seen:
            raise ContentServerError("Calibre returned a duplicate book id")
        seen.add(book_id)
        books.append(dict(raw))
    return sorted(books, key=lambda book: int(book["id"]))


class ContentServerSource:
    """Read-only calibredb adapter restricted to one loopback Content Server."""

    source_kind = "calibre_content_server"

    def __init__(
        self,
        base_url: str,
        *,
        library_id: str,
        username: str,
        source_identity: str,
        password: str,
        runner: CommandRunner | None = None,
    ) -> None:
        self.base_url = _loopback_base_url(base_url)
        self.library_id = _validate_text(library_id, field="library id")
        self.username = _validate_text(username, field="username", max_length=128)
        self.source_identity = _validate_text(source_identity, field="source identity")
        if not password or any(character in password for character in ("\x00", "\r", "\n")):
            raise ValueError("password is empty or invalid")
        self._password = password
        self._runner = runner or _subprocess_runner
        self._command_history: list[list[str]] = []
        identity = f"calibre-content-server\0{self.source_identity}\0{self.library_id}\0{self.username}".encode()
        self.fingerprint = hashlib.sha256(identity).hexdigest()

    @property
    def command_history(self) -> list[list[str]]:
        return [list(command) for command in self._command_history]

    @property
    def library_url(self) -> str:
        return f"{self.base_url}/#{quote(self.library_id, safe='')}"

    def _authenticated_command(self, operation: str, arguments: Sequence[str]) -> list[str]:
        if operation not in {"list", "export"}:
            raise ContentServerError("Calibre operation is not allowed by the read-only adapter")
        return [
            "calibredb",
            operation,
            *arguments,
            "--with-library",
            self.library_url,
            "--username",
            self.username,
            "--password",
            "<stdin>",
        ]

    def _run(self, operation: str, arguments: Sequence[str], *, timeout: float) -> subprocess.CompletedProcess[str]:
        command = self._authenticated_command(operation, arguments)
        self._command_history.append(list(command))
        result: subprocess.CompletedProcess[str] | None = None
        with suppress(Exception):
            result = self._runner(command, self._password, timeout)
        if result is None:
            # Raise outside the handler so a custom runner cannot attach a
            # sensitive exception chain to this trust boundary.
            raise ContentServerError(f"Calibre {operation} read failed")
        if result.returncode != 0:
            raise ContentServerError(f"Calibre {operation} read failed with exit code {result.returncode}")
        if len(result.stdout.encode("utf-8")) > MAX_MACHINE_OUTPUT_BYTES:
            raise ContentServerError(f"Calibre {operation} response exceeded the permitted size")
        return result

    def list_books(self) -> list[dict[str, Any]]:
        result = self._run(
            "list",
            ["--for-machine", "--fields", "all"],
            timeout=DEFAULT_NETWORK_TIMEOUT_SECONDS,
        )
        try:
            payload = json.loads(result.stdout)
        except (json.JSONDecodeError, TypeError):
            raise ContentServerError("Calibre did not return a valid JSON book list") from None
        return _validated_books(payload)

    def show_metadata(self, book_id: int) -> dict[str, Any]:
        if isinstance(book_id, bool) or not isinstance(book_id, int) or book_id <= 0:
            raise ValueError("book id is invalid")
        result = self._run(
            "list",
            ["--for-machine", "--fields", "all", "--search", f"id:{book_id}"],
            timeout=DEFAULT_NETWORK_TIMEOUT_SECONDS,
        )
        try:
            books = _validated_books(json.loads(result.stdout))
        except (json.JSONDecodeError, TypeError):
            raise ContentServerError("Calibre did not return a valid JSON book list") from None
        if len(books) != 1 or int(books[0]["id"]) != book_id:
            raise ContentServerError("Calibre did not return the exact book requested")
        return books[0]

    def format_references(self, book_id: int, raw_formats: object) -> list[str]:
        """Return stable, non-sensitive references for a book's remote formats."""
        if isinstance(book_id, bool) or not isinstance(book_id, int) or book_id <= 0:
            raise ValueError("book id is invalid")
        formats = _formats(raw_formats)
        if "UNKNOWN" in formats or len(formats) != len(set(formats)):
            raise ContentServerError("Calibre returned invalid or duplicate format metadata")
        return [f"calibre-server:{self.fingerprint}:{book_id}:{item}" for item in formats]

    def format_from_reference(self, reference: str) -> str:
        """Validate a logical reference and recover its canonical format name."""
        prefix = f"calibre-server:{self.fingerprint}:"
        if not isinstance(reference, str) or not reference.startswith(prefix):
            raise ValueError("remote format reference is invalid")
        remainder = reference.removeprefix(prefix)
        book_id, separator, canonical_format = remainder.partition(":")
        if not separator or not book_id.isdigit() or int(book_id) <= 0 or not _FORMAT_RE.fullmatch(canonical_format):
            raise ValueError("remote format reference is invalid")
        return canonical_format

    @contextmanager
    def export_format(
        self,
        book_id: int,
        format_name: str,
        *,
        scratch_root: Path,
    ) -> Iterator[Path]:
        if isinstance(book_id, bool) or not isinstance(book_id, int) or book_id <= 0:
            raise ValueError("book id is invalid")
        canonical_format = format_name.strip().upper()
        if not _FORMAT_RE.fullmatch(canonical_format):
            raise ValueError("format is invalid")
        root = ensure_secure_directory(scratch_root)
        temporary = Path(tempfile.mkdtemp(prefix="bookaudit-content-", dir=root))
        os.chmod(temporary, 0o700, follow_symlinks=False)
        try:
            self._run(
                "export",
                [
                    str(book_id),
                    "--to-dir",
                    str(temporary),
                    "--formats",
                    canonical_format,
                    "--dont-update-metadata",
                    "--dont-save-cover",
                    "--dont-save-extra-files",
                    "--dont-write-opf",
                    "--single-dir",
                ],
                timeout=EXPORT_TIMEOUT_SECONDS,
            )
            exported = self._single_exported_file(temporary, canonical_format)
            yield exported
        finally:
            shutil.rmtree(temporary, ignore_errors=False)

    @staticmethod
    def _single_exported_file(temporary: Path, canonical_format: str) -> Path:
        regular_files: list[Path] = []
        for root, directories, files in os.walk(temporary, followlinks=False):
            root_path = Path(root)
            for name in [*directories, *files]:
                candidate = root_path / name
                metadata = candidate.lstat()
                if stat.S_ISLNK(metadata.st_mode):
                    raise ContentServerError("Calibre export did not contain exactly one regular file")
                if stat.S_ISREG(metadata.st_mode):
                    regular_files.append(candidate)
        if len(regular_files) != 1:
            raise ContentServerError("Calibre export did not contain exactly one regular file")
        exported = regular_files[0]
        metadata = exported.stat(follow_symlinks=False)
        if (
            metadata.st_nlink != 1
            or metadata.st_size > MAX_EXPORTED_FORMAT_BYTES
            or exported.suffix.upper().lstrip(".") != canonical_format
        ):
            raise ContentServerError("Calibre export did not contain the requested regular file")
        return exported


def _formats(raw: object) -> list[str]:
    if raw is None:
        return []
    values = raw if isinstance(raw, list) else [raw]
    formats: list[str] = []
    for value in values:
        candidate: str | None = None
        if isinstance(value, str):
            suffix = Path(value).suffix
            candidate = suffix[1:] if suffix else value
        elif isinstance(value, dict):
            raw_format = value.get("format")
            if isinstance(raw_format, str):
                candidate = raw_format
            elif isinstance(value.get("path"), str):
                candidate = Path(value["path"]).suffix[1:]
        canonical = candidate.strip().upper() if candidate else ""
        formats.append(canonical if _FORMAT_RE.fullmatch(canonical) else "UNKNOWN")
    return formats


def _nonempty(raw: object) -> bool:
    if isinstance(raw, str):
        return bool(raw.strip())
    if isinstance(raw, list):
        return any(_nonempty(item) for item in raw)
    return raw is not None


def _isbn_state(book: dict[str, Any]) -> str:
    candidates: list[str] = []
    identifiers = book.get("identifiers")
    if isinstance(identifiers, dict) and isinstance(identifiers.get("isbn"), str):
        candidates.append(identifiers["isbn"])
    if isinstance(book.get("isbn"), str):
        candidates.append(book["isbn"])
    presented = [value for value in candidates if value.strip()]
    if not presented:
        return "missing"
    return "valid" if any(validate_isbn(value) for value in presented) else "invalid"


def _last_modified(raw: object) -> datetime | None:
    if not isinstance(raw, str) or not raw.strip():
        return None
    parsed = datetime.fromisoformat(raw.strip().replace("Z", "+00:00"))
    if parsed.tzinfo is None:
        parsed = parsed.replace(tzinfo=UTC)
    return parsed.astimezone(UTC)


def aggregate_inventory(
    books: Sequence[dict[str, Any]],
    *,
    source_fingerprint: str,
    captured_at: datetime | None = None,
) -> InventoryReport:
    """Reduce sensitive per-book records to a deterministic aggregate report."""
    validated = _validated_books(list(books))
    format_counts: dict[str, int] = {}
    language_counts: dict[str, int] = {}
    isbn_counts = {"invalid": 0, "missing": 0, "valid": 0}
    incomplete = {"authors": 0, "languages": 0, "publisher": 0, "pubdate": 0, "title": 0}
    modified: list[datetime] = []
    invalid_modified = 0
    missing_modified = 0
    total_formats = 0
    multi_format = 0
    no_format = 0
    manifest_items: list[dict[str, Any]] = []

    for book in validated:
        book_formats = _formats(book.get("formats"))
        total_formats += len(book_formats)
        multi_format += int(len(book_formats) > 1)
        no_format += int(not book_formats)
        for format_name in book_formats:
            aggregate_format = format_name if format_name in _INVENTORY_FORMATS else "OTHER"
            format_counts[aggregate_format] = format_counts.get(aggregate_format, 0) + 1

        raw_languages = book.get("languages")
        languages = raw_languages if isinstance(raw_languages, list) else [raw_languages]
        presented_languages = {value.strip().upper() for value in languages if isinstance(value, str) and value.strip()}
        normalized_languages = sorted(
            {language if _LANGUAGE_RE.fullmatch(language) else "OTHER" for language in presented_languages}
        )
        for language in normalized_languages:
            language_counts[language] = language_counts.get(language, 0) + 1

        for field in incomplete:
            value = normalized_languages if field == "languages" else book.get(field)
            incomplete[field] += int(not _nonempty(value))

        isbn_counts[_isbn_state(book)] += 1
        raw_modified = book.get("last_modified")
        if raw_modified is None or (isinstance(raw_modified, str) and not raw_modified.strip()):
            missing_modified += 1
            canonical_modified = None
        else:
            try:
                parsed_modified = _last_modified(raw_modified)
            except (TypeError, ValueError):
                invalid_modified += 1
                canonical_modified = None
            else:
                if parsed_modified is None:
                    invalid_modified += 1
                    canonical_modified = None
                else:
                    modified.append(parsed_modified)
                    canonical_modified = parsed_modified.isoformat()
        manifest_items.append({"id": int(book["id"]), "formats": book_formats, "last_modified": canonical_modified})

    manifest_encoded = json.dumps(manifest_items, sort_keys=True, separators=(",", ":"), ensure_ascii=True).encode()
    return InventoryReport(
        captured_at=captured_at or datetime.now(UTC),
        source_fingerprint=source_fingerprint,
        manifest_sha256=hashlib.sha256(manifest_encoded).hexdigest(),
        counts=InventoryCounts(
            books=len(validated),
            formats=total_formats,
            multi_format=multi_format,
            no_format=no_format,
        ),
        formats=dict(sorted(format_counts.items())),
        languages=dict(sorted(language_counts.items())),
        isbn=ISBNCounts(**isbn_counts),
        incomplete_fields=incomplete,
        last_modified=LastModifiedRange(
            minimum=min(modified) if modified else None,
            maximum=max(modified) if modified else None,
            missing=missing_modified,
            invalid=invalid_modified,
        ),
    )

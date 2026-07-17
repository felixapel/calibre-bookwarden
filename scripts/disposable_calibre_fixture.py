#!/usr/bin/env python3
"""Build and attest the generated CC0 library used by the disposable lab."""

from __future__ import annotations

import argparse
import asyncio
import hashlib
import html
import json
import os
import re
import secrets
import stat
import subprocess
import sys
import tempfile
import zipfile
from collections.abc import Sequence
from pathlib import Path

import httpx
from PIL import Image, ImageDraw, ImageFont

from calibre_ai_auditor.providers.evidence_v2 import (
    GoogleBooksEvidenceProvider,
    OpenLibraryEvidenceProvider,
    OutboundRequestError,
    SafeHttpClient,
)

CALIBREDB = Path("/opt/calibre/calibredb")
CALIBRE_SERVER = Path("/opt/calibre/calibre-server")
EBOOK_CONVERT = Path("/opt/calibre/ebook-convert")
LIBRARY = Path("/lab-library/Calibre_Lab")
SERVER_LIBRARY = Path("/library/Calibre_Lab")
AUTH_DB = Path("/lab-auth/users.sqlite")
CREDENTIAL = Path("/credentials/password")
STATE = Path("/state")
BASELINE = STATE / "library-baseline.json"
COMMAND_LOG = STATE / "calibredb-commands.jsonl"
SCRATCH = STATE / "scratch"
ACL_CONTROL = STATE / "acl-control-ok"
USERNAME = "auditor"
LIBRARY_ID = "Calibre_Lab"
SERVER_URL = f"http://127.0.0.1:8086/#{LIBRARY_ID}"
SOURCE_IDENTITY = "disposable-calibre-9.11.0"
WEB_ISBN = "9781912729289"

FIXTURE_MANIFEST: tuple[dict[str, str], ...] = (
    {"kind": "correct_epub", "title": "The Clockwork Garden", "author": "Ada Verity"},
    {"kind": "mismatched_epub", "title": "Signals at Dawn", "author": "Mira North"},
    {"kind": "multi_format", "title": "Binary Tides", "author": "Theo Quill"},
    {"kind": "no_format", "title": "Missing Manifestation", "author": "Lab Fixture"},
    {"kind": "image_only_pdf", "title": "Raster Observatory", "author": "Iris Vale"},
)


class FixtureError(RuntimeError):
    pass


def synthetic_isbn(index: int) -> str:
    if index not in range(3):
        raise ValueError("only the three offline fixture ISBNs are defined")
    stem = f"97800000000{index}"
    checksum = (10 - sum((1 if position % 2 == 0 else 3) * int(digit) for position, digit in enumerate(stem)) % 10) % 10
    return f"{stem}{checksum}"


def _new_password() -> str:
    # The manage-users CLI receives the password as a positional argument. A
    # fixed alphabetic prefix prevents an otherwise valid random value that
    # starts with "-" from being parsed as an option.
    return f"L{secrets.token_urlsafe(32)}"


def _run(
    command: Sequence[str],
    *,
    password: str | None = None,
    timeout: float = 300,
) -> subprocess.CompletedProcess[str]:
    try:
        return subprocess.run(
            list(command),
            input=f"{password}\n" if password is not None else None,
            text=True,
            capture_output=True,
            check=False,
            timeout=timeout,
        )
    except (OSError, subprocess.TimeoutExpired) as exc:
        raise FixtureError("fixture command could not be executed") from exc


def _require_success(command: Sequence[str], *, timeout: float = 300) -> str:
    result = _run(command, timeout=timeout)
    if result.returncode != 0:
        executable = Path(command[0]).name if command else "unknown"
        operation = command[1] if executable == "calibredb" and len(command) > 1 else None
        label = f"{executable} {operation}" if operation is not None else executable
        raise FixtureError(f"fixture {label} failed with exit code {result.returncode}")
    return result.stdout


def _epub(path: Path, *, title: str, author: str, isbn: str, text: str) -> None:
    escaped_title = html.escape(title)
    escaped_author = html.escape(author)
    escaped_text = html.escape(text)
    container = """<?xml version="1.0" encoding="UTF-8"?>
<container version="1.0" xmlns="urn:oasis:names:tc:opendocument:xmlns:container">
  <rootfiles><rootfile full-path="OEBPS/content.opf" media-type="application/oebps-package+xml"/></rootfiles>
</container>
"""
    opf = f"""<?xml version="1.0" encoding="UTF-8"?>
<package version="3.0" unique-identifier="bookid" xmlns="http://www.idpf.org/2007/opf">
  <metadata xmlns:dc="http://purl.org/dc/elements/1.1/">
    <dc:identifier id="bookid">urn:isbn:{isbn}</dc:identifier>
    <dc:title>{escaped_title}</dc:title><dc:creator>{escaped_author}</dc:creator><dc:language>en</dc:language>
    <meta property="dcterms:modified">2026-07-16T00:00:00Z</meta>
  </metadata>
  <manifest>
    <item id="nav" href="nav.xhtml" media-type="application/xhtml+xml" properties="nav"/>
    <item id="chapter" href="chapter.xhtml" media-type="application/xhtml+xml"/>
  </manifest>
  <spine><itemref idref="chapter"/></spine>
</package>
"""
    nav = """<?xml version="1.0" encoding="UTF-8"?>
<html xmlns="http://www.w3.org/1999/xhtml"><head><title>Contents</title></head>
<body><nav epub:type="toc" xmlns:epub="http://www.idpf.org/2007/ops">
<ol><li><a href="chapter.xhtml">Book</a></li></ol></nav></body></html>
"""
    chapter = f"""<?xml version="1.0" encoding="UTF-8"?>
<html xmlns="http://www.w3.org/1999/xhtml"><head><title>{escaped_title}</title></head>
<body><h1>{escaped_title}</h1><p>by {escaped_author}</p><p>ISBN {isbn}</p><p>{escaped_text}</p>
<p>This generated fixture is dedicated to the public domain under CC0 1.0.</p></body></html>
"""
    with zipfile.ZipFile(path, "w") as archive:
        archive.writestr("mimetype", "application/epub+zip", compress_type=zipfile.ZIP_STORED)
        archive.writestr("META-INF/container.xml", container)
        archive.writestr("OEBPS/content.opf", opf)
        archive.writestr("OEBPS/nav.xhtml", nav)
        archive.writestr("OEBPS/chapter.xhtml", chapter)


def _image_only_pdf(path: Path) -> None:
    image = Image.new("RGB", (1654, 2339), "white")
    drawing = ImageDraw.Draw(image)
    try:
        font = ImageFont.load_default(size=72)
    except TypeError:
        font = ImageFont.load_default()
    drawing.multiline_text(
        (140, 300),
        "RASTER OBSERVATORY\n\nIris Vale\n\nGenerated CC0 test edition",
        fill="black",
        font=font,
        spacing=36,
    )
    image.save(path, "PDF", resolution=150.0)


def _book_id(output: str) -> int:
    match = re.search(r"Added book ids?:\s*([1-9][0-9]*)", output, flags=re.IGNORECASE)
    if match is None:
        raise FixtureError("Calibre did not return the created fixture id")
    return int(match.group(1))


def _add(path: Path, *, title: str, author: str, language: str, isbn: str | None) -> int:
    command = [
        str(CALIBREDB),
        "add",
        str(path),
        "--with-library",
        str(LIBRARY),
        "--title",
        title,
        "--authors",
        author,
        "--languages",
        language,
    ]
    if isbn is not None:
        command.extend(["--isbn", isbn])
    return _book_id(_require_success(command))


def _hash_tree(root: Path) -> tuple[str, list[dict[str, object]]]:
    if not root.is_dir():
        raise FixtureError("fixture library is unavailable")
    entries: list[dict[str, object]] = []
    for candidate in sorted(root.rglob("*"), key=lambda item: item.relative_to(root).as_posix()):
        metadata = candidate.lstat()
        if stat.S_ISLNK(metadata.st_mode):
            raise FixtureError("fixture library contains a symbolic link")
        if not stat.S_ISREG(metadata.st_mode):
            continue
        relative = candidate.relative_to(root).as_posix()
        digest = hashlib.sha256()
        with candidate.open("rb") as source:
            for chunk in iter(lambda: source.read(1024 * 1024), b""):
                digest.update(chunk)
        entries.append({"path": relative, "size": metadata.st_size, "sha256": digest.hexdigest()})
    encoded = json.dumps(entries, sort_keys=True, separators=(",", ":")).encode()
    return hashlib.sha256(encoded).hexdigest(), entries


def build() -> None:
    if os.getuid() != 10001 or os.getgid() != 10001:
        raise FixtureError("fixture builder must run as the unprivileged bookaudit user")
    for directory in (LIBRARY.parent, AUTH_DB.parent, CREDENTIAL.parent, STATE):
        directory.mkdir(parents=True, exist_ok=True)
    if any(LIBRARY.parent.iterdir()) or any(AUTH_DB.parent.iterdir()) or any(CREDENTIAL.parent.iterdir()):
        raise FixtureError("disposable volumes were not empty")
    source = Path("/tmp/disposable-calibre-fixtures")
    source.mkdir(mode=0o700)

    first = source / "clockwork.epub"
    _epub(
        first,
        title=FIXTURE_MANIFEST[0]["title"],
        author=FIXTURE_MANIFEST[0]["author"],
        isbn=synthetic_isbn(0),
        text="A deterministic garden mechanism is inspected one gear at a time.",
    )
    _add(
        first,
        title=FIXTURE_MANIFEST[0]["title"],
        author=FIXTURE_MANIFEST[0]["author"],
        language="en",
        isbn=synthetic_isbn(0),
    )

    second = source / "signals.epub"
    _epub(
        second,
        title=FIXTURE_MANIFEST[1]["title"],
        author=FIXTURE_MANIFEST[1]["author"],
        isbn=synthetic_isbn(1),
        text="A lighthouse log identifies the precise edition inside the file.",
    )
    _add(second, title="Incorrect Catalogue Title", author="Wrong Author", language="fr", isbn=synthetic_isbn(1))

    third = source / "binary-tides.epub"
    _epub(
        third,
        title=FIXTURE_MANIFEST[2]["title"],
        author=FIXTURE_MANIFEST[2]["author"],
        isbn=synthetic_isbn(2),
        text="Two formats carry the same generated manifestation evidence.",
    )
    third_id = _add(
        third,
        title=FIXTURE_MANIFEST[2]["title"],
        author=FIXTURE_MANIFEST[2]["author"],
        language="en",
        isbn=synthetic_isbn(2),
    )
    azw3 = source / "binary-tides.azw3"
    _require_success([str(EBOOK_CONVERT), str(third), str(azw3)], timeout=600)
    _require_success([str(CALIBREDB), "add_format", str(third_id), str(azw3), "--with-library", str(LIBRARY)])

    _book_id(
        _require_success(
            [
                str(CALIBREDB),
                "add",
                "--empty",
                "--title",
                FIXTURE_MANIFEST[3]["title"],
                "--authors",
                FIXTURE_MANIFEST[3]["author"],
                "--with-library",
                str(LIBRARY),
            ]
        )
    )

    scanned = source / "raster-observatory.pdf"
    _image_only_pdf(scanned)
    _add(
        scanned,
        title=FIXTURE_MANIFEST[4]["title"],
        author=FIXTURE_MANIFEST[4]["author"],
        language="en",
        isbn=None,
    )

    with tempfile.TemporaryDirectory(prefix="bookaudit-acl-control-", dir="/tmp") as raw_control:
        control = Path(raw_control)
        _require_success(
            [
                str(CALIBREDB),
                "add",
                "--empty",
                "--title",
                "ACL Control",
                "--with-library",
                str(control),
            ]
        )
        _require_success(
            [
                str(CALIBREDB),
                "set_metadata",
                "1",
                "--field",
                "title:ACL Control Updated",
                "--with-library",
                str(control),
            ]
        )
    ACL_CONTROL.write_text("ok\n")
    os.chmod(ACL_CONTROL, 0o600, follow_symlinks=False)

    password = _new_password()
    CREDENTIAL.write_text(f"{password}\n")
    os.chmod(CREDENTIAL, 0o600, follow_symlinks=False)
    _require_success(
        [
            str(CALIBRE_SERVER),
            "--userdb",
            str(AUTH_DB),
            "--manage-users",
            "--",
            "add",
            USERNAME,
            password,
            "--readonly",
        ]
    )
    os.chmod(AUTH_DB, 0o600, follow_symlinks=False)

    SCRATCH.mkdir(mode=0o700)
    COMMAND_LOG.touch(mode=0o600)
    digest, entries = _hash_tree(LIBRARY)
    baseline = {
        "schema_version": 1,
        "library_sha256": digest,
        "entries": entries,
        "expected_counts": {"books": 5, "formats": 5, "multi_format": 1, "no_format": 1},
        "credential_files": {"password_mode": "0600"},
    }
    BASELINE.write_text(json.dumps(baseline, indent=2, sort_keys=True) + "\n")
    os.chmod(BASELINE, 0o600, follow_symlinks=False)
    print(json.dumps({"books": 5, "formats": 5, "library_sha256": digest}, sort_keys=True))


def _password() -> str:
    if stat.S_IMODE(CREDENTIAL.stat(follow_symlinks=False).st_mode) != 0o600:
        raise FixtureError("credential file mode is unsafe")
    value = CREDENTIAL.read_text().rstrip("\r\n")
    if not value or "\n" in value or "\r" in value:
        raise FixtureError("credential file is invalid")
    return value


def _bookaudit(action: str) -> None:
    common = [
        "--content-server",
        "http://127.0.0.1:8086",
        "--library-id",
        LIBRARY_ID,
        "--username",
        USERNAME,
        "--source-identity",
        SOURCE_IDENTITY,
        "--password-stdin",
    ]
    if action == "inventory":
        command = ["bookaudit", "inventory", *common]
    elif action == "verify":
        command = [
            "bookaudit",
            "verify-content-server",
            *common,
            "--scratch-root",
            str(SCRATCH),
            "--limit",
            "0",
            "--format",
            "json",
            "--no-llm",
            "--no-vision",
            "--deny-remote-text",
            "--deny-remote-images",
            "--deny-public-providers",
        ]
    else:
        raise FixtureError("unknown auditor action")
    result = _run(command, password=_password(), timeout=1200)
    if result.returncode != 0:
        raise FixtureError(f"{action} failed with exit code {result.returncode}")
    sys.stdout.write(result.stdout)


def _version() -> None:
    result = _run(["calibredb", "--version"])
    if result.returncode != 0:
        raise FixtureError("calibredb version check failed")
    match = re.search(r"calibre\s+([0-9]+\.[0-9]+(?:\.[0-9]+)?)", result.stdout, flags=re.IGNORECASE)
    if match is None:
        raise FixtureError("calibredb returned an unknown version format")
    version = match.group(1)
    print(f"{version}.0" if version.count(".") == 1 else version)


def _migrate() -> None:
    result = _run(["bookaudit", "migrate"])
    if result.returncode != 0:
        raise FixtureError("disposable migration failed")


def _is_authorization_denial(stderr: str) -> bool:
    lowered = stderr.lower()
    denial = any(
        marker in lowered
        for marker in (
            "not allowed to make changes",
            "not have permission",
            "permission denied",
            "read-only user",
            "readonly user",
        )
    )
    unrelated_failure = any(
        marker in lowered
        for marker in (
            "authentication",
            "incorrect password",
            "not found",
            "connection refused",
            "timed out",
            "unknown option",
            "usage:",
        )
    )
    lines = [line.strip().lower() for line in stderr.splitlines() if line.strip()]
    return (bool(lines) and lines[-1] == "forbidden") or (denial and not unrelated_failure)


def acl_probe() -> None:
    if ACL_CONTROL.read_text() != "ok\n" or stat.S_IMODE(ACL_CONTROL.stat(follow_symlinks=False).st_mode) != 0o600:
        raise FixtureError("ACL control marker is invalid")
    result = _run(
        [
            str(CALIBREDB),
            "set_metadata",
            "1",
            "--field",
            "title:FORBIDDEN LAB WRITE",
            "--with-library",
            SERVER_URL,
            "--username",
            USERNAME,
            "--password",
            "<stdin>",
        ],
        password=_password(),
    )
    print(
        json.dumps(
            {
                "control_succeeded": True,
                "write_rejected": result.returncode != 0 and _is_authorization_denial(result.stderr),
            },
            sort_keys=True,
        )
    )


def attest() -> None:
    baseline = json.loads(BASELINE.read_text())
    current_digest, current_entries = _hash_tree(SERVER_LIBRARY)
    operations: list[str] = []
    for line in COMMAND_LOG.read_text().splitlines():
        try:
            record = json.loads(line)
            operation = record["operation"]
        except (json.JSONDecodeError, KeyError, TypeError) as exc:
            raise FixtureError("calibredb command log is invalid") from exc
        if not isinstance(operation, str):
            raise FixtureError("calibredb command log operation is invalid")
        operations.append(operation)
    scratch_empty = SCRATCH.is_dir() and not any(SCRATCH.iterdir())
    print(
        json.dumps(
            {
                "identical": baseline["entries"] == current_entries,
                "baseline_sha256": baseline["library_sha256"],
                "current_sha256": current_digest,
                "operations": operations,
                "scratch_empty": scratch_empty,
                "credential_files": baseline["credential_files"],
            },
            sort_keys=True,
        )
    )


async def _web_smoke() -> dict[str, object]:
    providers = {
        "google_books": GoogleBooksEvidenceProvider(SafeHttpClient(allowed_hosts={"www.googleapis.com"})),
        "openlibrary": OpenLibraryEvidenceProvider(SafeHttpClient(allowed_hosts={"openlibrary.org"})),
    }
    results: dict[str, object] = {}
    for name, provider in providers.items():
        try:
            evidence = await provider.fetch_by_isbn(WEB_ISBN)
            results[name] = {"status": "ok", "evidence_count": len(evidence)}
        except (OutboundRequestError, httpx.HTTPError, TimeoutError, ValueError):
            results[name] = {"status": "unavailable", "evidence_count": 0}
    return {"isbn": WEB_ISBN, "providers": results, "gating": False}


def main(argv: Sequence[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument(
        "action",
        choices=("build", "calibredb-version", "migrate", "inventory", "verify", "acl-probe", "attest", "web-smoke"),
    )
    args = parser.parse_args(argv)
    try:
        if args.action == "build":
            build()
        elif args.action == "calibredb-version":
            _version()
        elif args.action == "migrate":
            _migrate()
        elif args.action in {"inventory", "verify"}:
            _bookaudit(str(args.action))
        elif args.action == "acl-probe":
            acl_probe()
        elif args.action == "attest":
            attest()
        else:
            print(json.dumps(asyncio.run(_web_smoke()), sort_keys=True))
    except (FixtureError, OSError, ValueError, json.JSONDecodeError) as exc:
        print(f"Disposable fixture failed safely: {exc}", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    raise SystemExit(main())

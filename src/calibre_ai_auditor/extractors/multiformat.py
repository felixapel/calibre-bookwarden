"""Bounded, non-mutating extraction for every format attached to a Calibre book."""

from __future__ import annotations

import hashlib
import os
import posixpath
import re
import shutil
import subprocess
import tempfile
import zipfile
from abc import ABC, abstractmethod
from pathlib import Path, PurePosixPath
from typing import Any
from xml.etree import ElementTree as ET

from pydantic import BaseModel, ConfigDict, Field

from calibre_ai_auditor.extractors.text import strip_tags
from calibre_ai_auditor.security.files import copy_file_beneath
from calibre_ai_auditor.verification.identity_v2 import (
    EvidenceSourceKind,
    FormatEvidence,
    FormatEvidenceStatus,
    SourceEvidence,
    validate_isbn,
)

CONTAINER_NS = "urn:oasis:names:tc:opendocument:xmlns:container"
DC_NS = "http://purl.org/dc/elements/1.1/"
OPF_NS = "http://www.idpf.org/2007/opf"
MAX_ARCHIVE_BYTES = 256 * 1024 * 1024
MAX_MEMBER_BYTES = 16 * 1024 * 1024
MAX_TEXT_BYTES = 2 * 1024 * 1024
MAX_EBOOK_BYTES = 2 * 1024 * 1024 * 1024
ISBN_CONTEXT_RE = re.compile(
    r"(?:ISBN(?:-1[03])?\s*[: ]\s*)([0-9Xx][0-9Xx\s-]{8,20}[0-9Xx])",
    re.IGNORECASE,
)


class FormatConverter(ABC):
    """Conversion boundary used only with a temporary output path."""

    @abstractmethod
    def convert(self, source: Path, target: Path) -> None:
        raise NotImplementedError


class CalibreFormatConverter(FormatConverter):
    def convert(self, source: Path, target: Path) -> None:
        executable = shutil.which("ebook-convert")
        if executable is None:
            raise FileNotFoundError("ebook-convert is not installed")
        environment = {
            "PATH": os.environ.get("PATH", ""),
            "LANG": os.environ.get("LANG", "C.UTF-8"),
            "HOME": str(target.parent),
            "XDG_CACHE_HOME": str(target.parent / ".cache"),
            "XDG_CONFIG_HOME": str(target.parent / ".config"),
        }
        subprocess.run(
            [executable, str(source), str(target)],
            check=True,
            capture_output=True,
            text=True,
            timeout=120,
            env=environment,
        )


class ExtractedSnippet(BaseModel):
    model_config = ConfigDict(extra="forbid")

    source: str
    text: str
    locator: str
    page_range: str | None = None


class FormatInspection(BaseModel):
    model_config = ConfigDict(extra="forbid")

    format_evidence: FormatEvidence
    evidence: list[SourceEvidence] = Field(default_factory=list)
    snippets: list[ExtractedSnippet] = Field(default_factory=list)
    metadata: dict[str, Any] = Field(default_factory=dict)


def _sha256(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as stream:
        for block in iter(lambda: stream.read(1024 * 1024), b""):
            digest.update(block)
    return digest.hexdigest()


def _failed_inspection(
    path: Path,
    sha256: str,
    status: FormatEvidenceStatus,
    message: str,
) -> FormatInspection:
    return FormatInspection(
        format_evidence=FormatEvidence(
            path=str(path),
            format=path.suffix.lstrip(".") or "UNKNOWN",
            sha256=sha256,
            status=status,
            error=message[:500],
        )
    )


def _safe_member_name(name: str) -> str:
    candidate = PurePosixPath(name)
    if candidate.is_absolute() or ".." in candidate.parts:
        raise ValueError("archive contains an unsafe member path")
    return candidate.as_posix()


def _validate_archive(archive: zipfile.ZipFile) -> None:
    total = 0
    for member in archive.infolist():
        _safe_member_name(member.filename)
        if member.file_size > MAX_MEMBER_BYTES:
            raise ValueError("archive member exceeds the extraction limit")
        total += member.file_size
        if total > MAX_ARCHIVE_BYTES:
            raise ValueError("archive exceeds the total extraction limit")
        if member.compress_size and member.file_size / member.compress_size > 500:
            raise ValueError("archive member has a suspicious compression ratio")


def _read_member(archive: zipfile.ZipFile, name: str) -> bytes:
    safe_name = _safe_member_name(name)
    member = archive.getinfo(safe_name)
    if member.file_size > MAX_MEMBER_BYTES:
        raise ValueError("archive member exceeds the extraction limit")
    return archive.read(member)


def _find_opf_path(archive: zipfile.ZipFile) -> str:
    try:
        container = ET.fromstring(_read_member(archive, "META-INF/container.xml"))
        rootfile = container.find(f".//{{{CONTAINER_NS}}}rootfile")
        if rootfile is not None and rootfile.get("full-path"):
            return _safe_member_name(rootfile.get("full-path") or "")
    except KeyError:
        pass
    candidates = sorted(name for name in archive.namelist() if name.lower().endswith(".opf"))
    if len(candidates) != 1:
        raise ValueError("EPUB must identify exactly one package document")
    return _safe_member_name(candidates[0])


def _first_text(parent: ET.Element, tag: str) -> str | None:
    element = parent.find(tag)
    if element is None or not element.text or not element.text.strip():
        return None
    return element.text.strip()


def _all_text(parent: ET.Element, tag: str) -> list[str]:
    return [item.text.strip() for item in parent.findall(tag) if item.text and item.text.strip()]


def _epub_spine_paths(package: ET.Element, opf_path: str) -> list[str]:
    manifest: dict[str, str] = {}
    for item in package.findall(f".//{{{OPF_NS}}}manifest/{{{OPF_NS}}}item"):
        item_id = item.get("id")
        href = item.get("href")
        if item_id and href:
            manifest[item_id] = href
    base = posixpath.dirname(opf_path)
    paths: list[str] = []
    for itemref in package.findall(f".//{{{OPF_NS}}}spine/{{{OPF_NS}}}itemref"):
        href = manifest.get(itemref.get("idref") or "")
        if href:
            paths.append(_safe_member_name(posixpath.normpath(posixpath.join(base, href))))
    return paths


def _source_for_path(path: str, index: int) -> str:
    lowered = path.casefold()
    if "copyright" in lowered or "colophon" in lowered:
        return "copyright_page"
    if "title" in lowered or index == 0:
        return "title_page"
    return "body"


def _evidence(
    *,
    root_id: str,
    sha256: str,
    field: str,
    value: Any,
    identifiers: dict[str, str],
    locator: str,
    source_kind: EvidenceSourceKind = EvidenceSourceKind.embedded_metadata,
) -> SourceEvidence:
    key = hashlib.sha256(f"{root_id}\0{field}\0{locator}".encode()).hexdigest()[:20]
    return SourceEvidence(
        evidence_id=f"ev_{key}",
        root_id=root_id,
        independence_root="book_content",
        source_kind=source_kind,
        field=field,
        value=value,
        manifestation_ids=identifiers,
        locator=locator,
        artifact_sha256=sha256,
        authoritative=True,
    )


def _inspect_epub(path: Path, sha256: str) -> FormatInspection:
    root_id = f"format:{sha256}"
    with zipfile.ZipFile(path) as archive:
        _validate_archive(archive)
        opf_path = _find_opf_path(archive)
        package = ET.fromstring(_read_member(archive, opf_path))
        metadata = package.find(f"{{{OPF_NS}}}metadata")
        if metadata is None:
            raise ValueError("EPUB package has no metadata element")

        raw_identifiers = _all_text(metadata, f"{{{DC_NS}}}identifier")
        isbn = next((valid for item in raw_identifiers if (valid := validate_isbn(item))), None)
        embedded_identifiers = {"isbn": isbn} if isbn else {}
        title = _first_text(metadata, f"{{{DC_NS}}}title")
        authors = _all_text(metadata, f"{{{DC_NS}}}creator")
        languages = [item.lower() for item in _all_text(metadata, f"{{{DC_NS}}}language")]
        publisher = _first_text(metadata, f"{{{DC_NS}}}publisher")
        pubdate = _first_text(metadata, f"{{{DC_NS}}}date")
        parsed_metadata: dict[str, Any] = {
            "title": title,
            "authors": authors,
            "identifiers": embedded_identifiers,
            "languages": languages,
            "publisher": publisher,
            "pubdate": pubdate,
        }

        evidence: list[SourceEvidence] = []
        for field, value in parsed_metadata.items():
            if value not in (None, [], {}):
                evidence.append(
                    _evidence(
                        root_id=root_id,
                        sha256=sha256,
                        field=field,
                        value=value,
                        identifiers=embedded_identifiers,
                        locator=opf_path,
                    )
                )

        snippets: list[ExtractedSnippet] = []
        content_isbns: set[str] = set()
        consumed = 0
        for index, content_path in enumerate(_epub_spine_paths(package, opf_path)):
            if consumed >= MAX_TEXT_BYTES:
                break
            try:
                raw = _read_member(archive, content_path)
            except KeyError:
                continue
            consumed += len(raw)
            text = strip_tags(raw.decode("utf-8", errors="replace")).strip()
            if text:
                source = _source_for_path(content_path, index)
                snippets.append(
                    ExtractedSnippet(
                        source=source,
                        text=text[:100_000],
                        locator=content_path,
                    )
                )
                lowered = text.casefold()
                edition_context = source == "copyright_page" or (
                    index < 6 and any(marker in lowered for marker in ("copyright", "colophon", "published by"))
                )
                if edition_context:
                    content_isbns.update(
                        candidate
                        for raw_isbn in ISBN_CONTEXT_RE.findall(text)
                        if (candidate := validate_isbn(raw_isbn)) is not None
                    )

        content_identifiers = {"isbn": next(iter(content_isbns))} if len(content_isbns) == 1 else {}
        identifiers = content_identifiers or embedded_identifiers
        if content_identifiers:
            locator = next(
                (
                    item.locator
                    for item in snippets
                    if any(value in _isbn_candidates_from_text(item.text) for value in content_isbns)
                ),
                "epub:front-matter",
            )
            evidence.append(
                _evidence(
                    root_id=root_id,
                    sha256=sha256,
                    field="identifiers",
                    value=content_identifiers,
                    identifiers=content_identifiers,
                    locator=locator,
                    source_kind=EvidenceSourceKind.content_native,
                )
            )

        format_evidence = FormatEvidence(
            path=str(path),
            format="EPUB",
            sha256=sha256,
            status=FormatEvidenceStatus.readable,
            identifiers=identifiers,
            title=title,
            authors=authors,
            languages=languages,
            evidence_ids=[item.evidence_id for item in evidence],
        )
        return FormatInspection(
            format_evidence=format_evidence,
            evidence=evidence,
            snippets=snippets,
            metadata={key: value for key, value in parsed_metadata.items() if value is not None},
        )


def _split_authors(value: str | None) -> list[str]:
    if not value:
        return []
    return [item.strip() for item in re.split(r"\s*[;&]\s*", value) if item.strip()]


def _isbn_candidates_from_text(text: str) -> set[str]:
    return {
        candidate for raw_isbn in ISBN_CONTEXT_RE.findall(text) if (candidate := validate_isbn(raw_isbn)) is not None
    }


def _inspect_pdf(path: Path, sha256: str) -> FormatInspection:
    import fitz  # type: ignore[import-untyped]

    root_id = f"format:{sha256}"
    document = fitz.open(path)
    try:
        if document.needs_pass:
            return _failed_inspection(path, sha256, FormatEvidenceStatus.drm, "PDF requires a password")
        raw_metadata = document.metadata or {}
        title = (raw_metadata.get("title") or "").strip() or None
        authors = _split_authors((raw_metadata.get("author") or "").strip() or None)
        snippets: list[ExtractedSnippet] = []
        identifiers: dict[str, str] = {}
        identifier_locator: str | None = None
        consumed = 0
        for page_index, page in enumerate(document):
            if consumed >= MAX_TEXT_BYTES:
                break
            text = page.get_text("text")[:100_000]
            consumed += len(text.encode("utf-8", errors="ignore"))
            lowered = text.casefold()
            is_edition_page = page_index < 12 and (
                any(marker in lowered for marker in ("copyright", "colophon", "published by"))
                or (page_index < 6 and "edition" in lowered)
            )
            if page_index == 0 or is_edition_page:
                snippets.append(
                    ExtractedSnippet(
                        source="title_page" if page_index == 0 else "copyright_page",
                        text=text,
                        locator=f"page:{page_index + 1}",
                        page_range=str(page_index + 1),
                    )
                )
            if not identifiers and is_edition_page:
                for raw_isbn in ISBN_CONTEXT_RE.findall(text):
                    if valid := validate_isbn(raw_isbn):
                        identifiers = {"isbn": valid}
                        identifier_locator = f"page:{page_index + 1}"
                        break

        parsed_metadata: dict[str, Any] = {
            "title": title,
            "authors": authors,
            "identifiers": identifiers,
            "languages": [],
        }
        evidence: list[SourceEvidence] = []
        for field, value in (("title", title), ("authors", authors)):
            if value not in (None, []):
                evidence.append(
                    _evidence(
                        root_id=root_id,
                        sha256=sha256,
                        field=field,
                        value=value,
                        identifiers=identifiers,
                        locator="pdf:document-info",
                    )
                )
        if identifiers and identifier_locator:
            evidence.append(
                _evidence(
                    root_id=root_id,
                    sha256=sha256,
                    field="identifiers",
                    value=identifiers,
                    identifiers=identifiers,
                    locator=identifier_locator,
                    source_kind=EvidenceSourceKind.content_native,
                )
            )
        return FormatInspection(
            format_evidence=FormatEvidence(
                path=str(path),
                format="PDF",
                sha256=sha256,
                status=FormatEvidenceStatus.readable,
                identifiers=identifiers,
                title=title,
                authors=authors,
                languages=[],
                evidence_ids=[item.evidence_id for item in evidence],
            ),
            evidence=evidence,
            snippets=snippets,
            metadata=parsed_metadata,
        )
    finally:
        document.close()


def _inspect_cbz(path: Path, sha256: str) -> FormatInspection:
    root_id = f"format:{sha256}"
    with zipfile.ZipFile(path) as archive:
        _validate_archive(archive)
        names = {name.casefold(): name for name in archive.namelist()}
        comic_info_name = names.get("comicinfo.xml")
        if comic_info_name is None:
            return FormatInspection(
                format_evidence=FormatEvidence(
                    path=str(path),
                    format="CBZ",
                    sha256=sha256,
                    status=FormatEvidenceStatus.readable,
                    error="ComicInfo.xml is absent; identity evidence is incomplete",
                )
            )
        comic_info = ET.fromstring(_read_member(archive, comic_info_name))
        title = _first_text(comic_info, "Title")
        authors = _split_authors(_first_text(comic_info, "Writer"))
        language = _first_text(comic_info, "LanguageISO")
        raw_gtin = _first_text(comic_info, "GTIN") or _first_text(comic_info, "ISBN")
        isbn = validate_isbn(raw_gtin) if raw_gtin else None
        identifiers = {"isbn": isbn} if isbn else {}
        languages = [language.lower()] if language else []
        parsed_metadata: dict[str, Any] = {
            "title": title,
            "authors": authors,
            "languages": languages,
            "identifiers": identifiers,
        }
        evidence = [
            _evidence(
                root_id=root_id,
                sha256=sha256,
                field=field,
                value=value,
                identifiers=identifiers,
                locator=comic_info_name,
            )
            for field, value in parsed_metadata.items()
            if value not in (None, [], {})
        ]
        return FormatInspection(
            format_evidence=FormatEvidence(
                path=str(path),
                format="CBZ",
                sha256=sha256,
                status=FormatEvidenceStatus.readable,
                identifiers=identifiers,
                title=title,
                authors=authors,
                languages=languages,
                evidence_ids=[item.evidence_id for item in evidence],
            ),
            evidence=evidence,
            metadata=parsed_metadata,
        )


def _inspect_converted(path: Path, sha256: str, converter: FormatConverter, target_suffix: str) -> FormatInspection:
    with tempfile.TemporaryDirectory(prefix="bookaudit-convert-") as tmp:
        target = Path(tmp) / f"converted{target_suffix}"
        converter.convert(path, target)
        if not target.is_file():
            raise ValueError("format converter did not create its declared output")
        converted = _inspect_epub(target, sha256) if target_suffix == ".epub" else _inspect_cbz(target, sha256)
        converted.format_evidence = converted.format_evidence.model_copy(
            update={"path": str(path), "format": path.suffix.lstrip(".").upper(), "sha256": sha256}
        )
        return converted


def _copy_stable_source(source: Path, target: Path, library_root: Path | None) -> str:
    root = library_root if library_root is not None else source.parent
    return copy_file_beneath(root, source, target, max_bytes=MAX_EBOOK_BYTES)


def inspect_format(
    path: Path,
    *,
    converter: FormatConverter | None = None,
    library_root: Path | None = None,
) -> FormatInspection:
    """Inspect one ebook from a stable, no-follow temporary snapshot."""
    suffix = path.suffix.lower()
    try:
        with tempfile.TemporaryDirectory(prefix="bookaudit-inspect-") as tmp:
            snapshot = Path(tmp) / f"source{suffix}"
            before = _copy_stable_source(path, snapshot, library_root)
            try:
                if suffix == ".epub":
                    result = _inspect_epub(snapshot, before)
                elif suffix == ".pdf":
                    result = _inspect_pdf(snapshot, before)
                elif suffix == ".cbz":
                    result = _inspect_cbz(snapshot, before)
                elif suffix in {".mobi", ".azw3", ".cbr"}:
                    selected_converter = converter
                    if selected_converter is None and shutil.which("ebook-convert"):
                        selected_converter = CalibreFormatConverter()
                    if selected_converter is None:
                        result = _failed_inspection(
                            snapshot,
                            before,
                            FormatEvidenceStatus.unsupported,
                            f"{suffix[1:].upper()} requires the ebook-convert capability",
                        )
                    else:
                        result = _inspect_converted(
                            snapshot,
                            before,
                            selected_converter,
                            ".cbz" if suffix == ".cbr" else ".epub",
                        )
                else:
                    result = _failed_inspection(
                        snapshot,
                        before,
                        FormatEvidenceStatus.unsupported,
                        f"format {suffix or '<none>'} has no safe extractor",
                    )
            except subprocess.TimeoutExpired as exc:
                result = _failed_inspection(
                    snapshot,
                    before,
                    FormatEvidenceStatus.error,
                    f"conversion timed out: {exc}",
                )
            except subprocess.CalledProcessError as exc:
                result = _failed_inspection(
                    snapshot,
                    before,
                    FormatEvidenceStatus.error,
                    f"conversion failed: {exc.returncode}",
                )
            except (ET.ParseError, UnicodeError, ValueError, zipfile.BadZipFile, KeyError, OSError) as exc:
                result = _failed_inspection(
                    snapshot,
                    before,
                    FormatEvidenceStatus.corrupt,
                    str(exc) or type(exc).__name__,
                )
            after_snapshot = Path(tmp) / f"after{suffix}"
            after = _copy_stable_source(path, after_snapshot, library_root)
            if after != before:
                return _failed_inspection(path, after, FormatEvidenceStatus.error, "ebook changed during inspection")
            result.format_evidence = result.format_evidence.model_copy(
                update={
                    "path": str(path),
                    "format": path.suffix.lstrip(".").upper() or "UNKNOWN",
                    "sha256": before,
                }
            )
            return result
    except FileNotFoundError:
        return _failed_inspection(path, "0" * 64, FormatEvidenceStatus.error, "ebook file does not exist")
    except (OSError, ValueError) as exc:
        return _failed_inspection(path, "0" * 64, FormatEvidenceStatus.error, str(exc) or type(exc).__name__)

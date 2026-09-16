"""Container Archaeology and Ground Truth Extractor.

Extracts authentic ground truth directly from book containers:
- EPUB 2.0 / 3.0: Deterministic Cover Extraction State Machine (DCESM 5-Tier)
- PDF: Adaptive Hybrid XObject extraction vs 2.0x PyMuPDF rasterization
- MOBI / AZW3 (KF8): PalmDB low-level binary parser for EXTH 201 CoverOffset
- CBZ / CBR: Natural alphanumeric sorting & ComicInfo.xml
- Deep Content Mining: CIP (Cataloging in Publication), colophon, legal deposit (D.L.), and multiple ISBNs.
- Anti-Zip-Bomb protection with member byte limits and compression ratio guards.
"""

from __future__ import annotations

import dataclasses
import io
import logging
import os
import posixpath
import re
import struct
import time
import zipfile
from enum import StrEnum
from pathlib import Path, PurePosixPath
from typing import Any
from xml.etree import ElementTree as ET

logger = logging.getLogger(__name__)

MAX_MEMBER_BYTES = 20 * 1024 * 1024  # 20 MB
MAX_CUMULATIVE_BYTES = 60 * 1024 * 1024  # 60 MB
MAX_COMPRESSION_RATIO = 100.0  # Suspect zip-bomb ratio
MAX_ARCHIVE_ENTRIES = 10_000
MAX_CENTRAL_DIRECTORY_BYTES = 8 * 1024 * 1024
_EOCD_SIGNATURE = b"PK\x05\x06"
_EOCD_SIZE = 22
_MAX_EOCD_SEARCH_BYTES = _EOCD_SIZE + 0xFFFF


class SecurityError(Exception):
    pass


class ZipBombError(SecurityError):
    pass


class PathTraversalError(SecurityError):
    pass


class BookFormat(StrEnum):
    EPUB = "EPUB"
    PDF = "PDF"
    MOBI = "MOBI"
    AZW3 = "AZW3"
    CBZ = "CBZ"
    CBR = "CBR"
    UNKNOWN = "UNKNOWN"


@dataclasses.dataclass(frozen=True)
class ExtractedCover:
    data: bytes
    mime_type: str
    source_strategy: str
    width: int | None = None
    height: int | None = None
    file_size_bytes: int = 0

    def __post_init__(self) -> None:
        if not self.file_size_bytes and self.data:
            object.__setattr__(self, "file_size_bytes", len(self.data))


@dataclasses.dataclass(frozen=True)
class ISBNRecord:
    isbn13: str
    isbn10: str | None
    tag: str | None
    is_valid: bool


@dataclasses.dataclass
class GroundTruthMetadata:
    title: str | None = None
    canonical_title: str | None = None
    subtitle: str | None = None
    authors: list[str] = dataclasses.field(default_factory=list)
    publisher: str | None = None
    publication_year: int | None = None
    legal_deposit: str | None = None
    isbns: list[ISBNRecord] = dataclasses.field(default_factory=list)
    primary_isbn: str | None = None
    title_page_text: str | None = None
    copyright_page_text: str | None = None
    colophon_text: str | None = None


@dataclasses.dataclass
class GroundTruthReport:
    format: BookFormat
    cover: ExtractedCover | None
    metadata: GroundTruthMetadata
    warnings: list[str] = dataclasses.field(default_factory=list)
    inspection_time_ms: float = 0.0


# ==============================================================================
# 1. SECURITY: Safe ZIP Reader
# ==============================================================================


def preflight_zip_file(path: Path) -> None:
    """Validate bounded EOCD metadata before constructing ``ZipFile``.

    ZIP64 and multi-disk archives are rejected because their extended central
    directory structures are outside this reader's bounded parser contract.
    """
    file_size = path.stat().st_size
    if file_size < _EOCD_SIZE:
        raise ZipBombError("ZIP archive is too short to contain an EOCD record")

    read_size = min(file_size, _MAX_EOCD_SEARCH_BYTES)
    with path.open("rb") as source:
        source.seek(file_size - read_size)
        tail = source.read(read_size)
    eocd_offset_in_tail = tail.rfind(_EOCD_SIGNATURE)
    if eocd_offset_in_tail < 0 or eocd_offset_in_tail + _EOCD_SIZE > len(tail):
        raise ZipBombError("ZIP archive has no complete EOCD record")

    eocd_offset = file_size - read_size + eocd_offset_in_tail
    (
        _signature,
        disk_number,
        central_directory_disk,
        entries_on_disk,
        entry_count,
        central_directory_size,
        central_directory_offset,
        comment_size,
    ) = struct.unpack_from("<4s4H2LH", tail, eocd_offset_in_tail)
    if eocd_offset + _EOCD_SIZE + comment_size != file_size:
        raise ZipBombError("ZIP EOCD comment bounds are malformed")
    if disk_number or central_directory_disk or entries_on_disk != entry_count:
        raise ZipBombError("Multi-disk ZIP archives are unsupported")
    if (
        entries_on_disk == 0xFFFF
        or entry_count == 0xFFFF
        or central_directory_size == 0xFFFFFFFF
        or central_directory_offset == 0xFFFFFFFF
    ):
        raise ZipBombError("ZIP64 archives are unsupported")
    if entry_count > MAX_ARCHIVE_ENTRIES:
        raise ZipBombError(f"ZIP archive exceeds {MAX_ARCHIVE_ENTRIES} member limit")
    if central_directory_size > MAX_CENTRAL_DIRECTORY_BYTES:
        raise ZipBombError("ZIP central directory exceeds bounded metadata limit")
    if central_directory_offset + central_directory_size > eocd_offset:
        raise ZipBombError("ZIP central directory bounds are malformed")


def sanitize_member_path(name: str) -> str:
    raw = name.replace("\\", "/")
    if not raw or "\x00" in raw or any(ord(char) < 32 for char in raw):
        raise PathTraversalError(f"Unsafe member path in ZIP archive: {name!r}")
    raw_parts = PurePosixPath(raw).parts
    if raw.startswith("/") or ".." in raw_parts:
        raise PathTraversalError(f"Malicious member path in ZIP archive: {name}")
    clean = posixpath.normpath(raw)
    if clean in ("", ".") or clean.startswith("../") or clean == "..":
        raise PathTraversalError(f"Directory escape path detected: {name}")
    parts = PurePosixPath(clean).parts
    if ".." in parts:
        raise PathTraversalError(f"Directory escape path detected: {name}")
    return clean


def validate_zip_archive(z: zipfile.ZipFile) -> None:
    infos = z.infolist()
    if len(infos) > MAX_ARCHIVE_ENTRIES:
        raise ZipBombError(f"ZIP archive exceeds {MAX_ARCHIVE_ENTRIES} member limit")
    for info in infos:
        sanitize_member_path(info.filename)
        if not info.is_dir() and info.file_size > MAX_MEMBER_BYTES:
            raise ZipBombError(f"ZIP member {info.filename} exceeds limit {MAX_MEMBER_BYTES} bytes")


def read_zip_member_safely(
    z: zipfile.ZipFile,
    member_name: str,
    max_bytes: int = MAX_MEMBER_BYTES,
    cumulative_tracker: list[int] | None = None,
) -> bytes:
    validate_zip_archive(z)
    safe_name = sanitize_member_path(member_name)
    try:
        info = z.getinfo(safe_name)
    except KeyError as error:
        raise FileNotFoundError(f"ZIP member not found: {safe_name}") from error

    if info.file_size > max_bytes:
        raise ZipBombError(f"ZIP member {safe_name} exceeds limit {max_bytes} bytes")
    if info.compress_size > 0:
        ratio = info.file_size / info.compress_size
        if ratio > MAX_COMPRESSION_RATIO and info.file_size > 500_000:
            raise ZipBombError(f"Suspicious compression ratio ({ratio:.1f}:1) in {safe_name}")

    if cumulative_tracker is not None and cumulative_tracker[0] + info.file_size > MAX_CUMULATIVE_BYTES:
        raise ZipBombError("Total cumulative decompression quota exceeded")

    buffer = bytearray()
    with z.open(info, "r") as source:
        while True:
            chunk = source.read(65536)
            if not chunk:
                break
            buffer.extend(chunk)
            if len(buffer) > max_bytes:
                raise ZipBombError(f"ZIP member {safe_name} exceeded limit {max_bytes} bytes")
            if cumulative_tracker is not None:
                cumulative_tracker[0] += len(chunk)
                if cumulative_tracker[0] > MAX_CUMULATIVE_BYTES:
                    raise ZipBombError("Total cumulative decompression quota exceeded")

    return bytes(buffer)


def detect_image_mime(data: bytes) -> str | None:
    if data.startswith(b"\xff\xd8\xff"):
        return "image/jpeg"
    elif data.startswith(b"\x89PNG\r\n\x1a\n"):
        return "image/png"
    elif data.startswith(b"RIFF") and len(data) >= 12 and data[8:12] == b"WEBP":
        return "image/webp"
    elif data.startswith(b"GIF87a") or data.startswith(b"GIF89a"):
        return "image/gif"
    return None


# ==============================================================================
# 2. MOBI / AZW3: PalmDB Low-Level Binary Parser
# ==============================================================================


class MobiPalmDocParser:
    """Low-level PalmDB parser extracting EXTH 201 CoverOffset directly from disk/stream."""

    def __init__(self, stream: io.BufferedReader):
        self.stream = stream
        self.num_records = 0
        self.record_offsets: list[int] = []
        self._parse_palmdb_header()

    def _parse_palmdb_header(self) -> None:
        self.stream.seek(0)
        header = self.stream.read(78)
        if len(header) < 78:
            raise ValueError("File too short to be PalmDB")

        self.num_records = struct.unpack(">H", header[76:78])[0]
        record_list_data = self.stream.read(self.num_records * 8)
        self.record_offsets = []
        for i in range(self.num_records):
            rec_offset = struct.unpack(">I", record_list_data[i * 8 : i * 8 + 4])[0]
            self.record_offsets.append(rec_offset)

    def extract_cover_and_metadata(self) -> tuple[ExtractedCover | None, dict[str, Any]]:
        if self.num_records < 2:
            return None, {}

        rec0_start = self.record_offsets[0]
        rec0_len = self.record_offsets[1] - rec0_start
        self.stream.seek(rec0_start)
        rec0_data = self.stream.read(min(rec0_len, 1024 * 1024))

        if len(rec0_data) < 16 + 128:
            return None, {}

        mobi_magic = rec0_data[16:20]
        if mobi_magic not in (b"MOBI", b"TEXt"):
            return None, {}

        header_length = struct.unpack(">I", rec0_data[20:24])[0]
        first_image_index = struct.unpack(">I", rec0_data[108:112])[0]
        exth_flags = struct.unpack(">I", rec0_data[128:132])[0]

        exth_metadata: dict[str, Any] = {}
        cover_offset: int | None = None

        if exth_flags & 0x40:
            exth_start = 16 + header_length
            if exth_start + 12 <= len(rec0_data) and rec0_data[exth_start : exth_start + 4] == b"EXTH":
                exth_len = struct.unpack(">I", rec0_data[exth_start + 4 : exth_start + 8])[0]
                rec_count = struct.unpack(">I", rec0_data[exth_start + 8 : exth_start + 12])[0]

                curr = exth_start + 12
                limit = min(exth_start + exth_len, len(rec0_data))
                for _ in range(rec_count):
                    if curr + 8 > limit:
                        break
                    rec_type, rec_size = struct.unpack(">II", rec0_data[curr : curr + 8])
                    if rec_size < 8 or curr + rec_size > limit:
                        break
                    val_bytes = rec0_data[curr + 8 : curr + rec_size]

                    if (  # CoverOffset
                        rec_type == 201 and len(val_bytes) == 4
                    ) or (rec_type == 129 and cover_offset is None and len(val_bytes) == 4):
                        cover_offset = struct.unpack(">I", val_bytes)[0]
                    elif rec_type == 100:
                        exth_metadata.setdefault("authors", []).append(val_bytes.decode("utf-8", "replace"))
                    elif rec_type == 101:
                        exth_metadata["publisher"] = val_bytes.decode("utf-8", "replace")
                    elif rec_type == 104:
                        exth_metadata["isbn"] = val_bytes.decode("utf-8", "replace").strip()
                    elif rec_type == 106:
                        exth_metadata["date"] = val_bytes.decode("utf-8", "replace").strip()
                    elif rec_type == 503:
                        exth_metadata["title"] = val_bytes.decode("utf-8", "replace")

                    curr += rec_size

        cover: ExtractedCover | None = None
        if cover_offset is not None and first_image_index != 0xFFFFFFFF:
            target_record_idx = first_image_index + cover_offset
            if target_record_idx < self.num_records:
                img_offset = self.record_offsets[target_record_idx]
                self.stream.seek(0, os.SEEK_END)
                file_size = self.stream.tell()
                next_offset = (
                    self.record_offsets[target_record_idx + 1]
                    if target_record_idx + 1 < self.num_records
                    else file_size
                )
                img_len = min(next_offset - img_offset, 15 * 1024 * 1024)
                self.stream.seek(img_offset)
                raw_img = self.stream.read(img_len)
                mime = detect_image_mime(raw_img)
                if mime:
                    cover = ExtractedCover(
                        data=raw_img,
                        mime_type=mime,
                        source_strategy="mobi_exth_201",
                    )

        return cover, exth_metadata


# ==============================================================================
# 3. METADATA MINER: CIP, Colofón, Depósito Legal e ISBNs
# ==============================================================================

ISBN_CLEAN_RE = re.compile(r"[^0-9Xx]")
ISBN_BLOCK_RE = re.compile(
    r"(?i)\b(?:ISBN(?:-1[03])?[:\s]*)?((?:97[89][-\s]?(?:\d[-\s]?){9}\d)|(?:\d[-\s]?(?:\d[-\s]?){8}[\dXx]))\b(?:\s*\(([^)]+)\))?"
)
DEP_LEGAL_RE = re.compile(
    r"(?i)\b(?:D\.?\s*L\.?|Dep[óo]sito\s+Legal)[\s:]*([A-Z]{1,4}|[A-Z]{1,2}-[A-Za-z]+)?[-\s.]*(\d{1,6}(?:\.\d{3})?)[-\s/]*((?:19|20)\d{2}|\d{2})\b"
)
PUBLISHER_RE = re.compile(
    r"(?i)(?:Publicado por|Published by|Editorial|Ediciones)\s+([A-ZÁÉÍÓÚÑ][A-Za-zÁÉÍÓÚÑáéíóúñ\s&.,'-]+?)(?:\r?\n|,|\.)"
)
YEAR_RE = re.compile(r"\b(?:19|20)\d{2}\b")


def validate_isbn10(s: str) -> bool:
    clean = ISBN_CLEAN_RE.sub("", s).upper()
    if len(clean) != 10 or not clean[:9].isdigit() or not (clean[9].isdigit() or clean[9] == "X"):
        return False
    total = sum(int(c) * (10 - i) for i, c in enumerate(clean[:9]))
    total += 10 if clean[9] == "X" else int(clean[9])
    return total % 11 == 0


def validate_isbn13(s: str) -> bool:
    clean = ISBN_CLEAN_RE.sub("", s)
    if len(clean) != 13 or not clean.isdigit():
        return False
    total = sum(int(c) * (1 if i % 2 == 0 else 3) for i, c in enumerate(clean[:12]))
    return (10 - (total % 10)) % 10 == int(clean[-1])


def to_canonical_isbn13(s: str) -> str | None:
    clean = ISBN_CLEAN_RE.sub("", s).upper()
    if len(clean) == 13 and validate_isbn13(clean):
        return clean
    if len(clean) == 10 and validate_isbn10(clean):
        body = "978" + clean[:9]
        check = (10 - sum(int(c) * (1 if i % 2 == 0 else 3) for i, c in enumerate(body)) % 10) % 10
        return body + str(check)
    return None


class ContentMetadataMiner:
    CIP_MARKERS = (
        "cataloging-in-publication",
        "cataloging in publication",
        "library of congress",
        "depósito legal",
        "deposito legal",
        "d.l.",
        "dépôt légal",
        "todos los derechos reservados",
        "all rights reserved",
        "tous droits réservés",
        "primera edición",
        "first edition",
        "impreso en",
        "printed in",
        "copyright ©",
    )

    COLOPHON_MARKERS = (
        "se terminó de imprimir",
        "se termino de imprimir",
        "acabose de imprimir",
        "achevé d'imprimer",
        "talleres de",
        "talleres gráficos",
        "colofón",
        "colofon",
    )

    @classmethod
    def mine_pages(cls, pages: list[tuple[str, str]]) -> GroundTruthMetadata:
        meta = GroundTruthMetadata()
        isbns_found: dict[str, ISBNRecord] = {}

        for index, (_locator, text) in enumerate(pages):
            lowered = text.casefold()
            is_cip = any(m in lowered for m in cls.CIP_MARKERS)
            is_colophon = any(m in lowered for m in cls.COLOPHON_MARKERS)

            if is_cip and not meta.copyright_page_text:
                meta.copyright_page_text = text[:100_000]
            elif is_colophon and not meta.colophon_text:
                meta.colophon_text = text[:100_000]
            elif index <= 2 and not meta.title_page_text and len(text.split()) < 100 and not is_cip:
                meta.title_page_text = text[:100_000]

            for match in ISBN_BLOCK_RE.finditer(text):
                raw_isbn, tag = match.group(1), match.group(2)
                can13 = to_canonical_isbn13(raw_isbn)
                if can13:
                    clean_orig = ISBN_CLEAN_RE.sub("", raw_isbn).upper()
                    isbn10 = clean_orig if len(clean_orig) == 10 else None
                    record = ISBNRecord(
                        isbn13=can13,
                        isbn10=isbn10,
                        tag=tag.strip().lower() if tag else None,
                        is_valid=True,
                    )
                    isbns_found[can13] = record

            if not meta.legal_deposit:
                dep_match = DEP_LEGAL_RE.search(text)
                if dep_match:
                    prov = dep_match.group(1) or ""
                    num = dep_match.group(2).replace(".", "")
                    year_raw = dep_match.group(3)
                    year = f"20{year_raw}" if len(year_raw) == 2 and int(year_raw) < 50 else year_raw
                    meta.legal_deposit = f"{prov}-{num}-{year}".strip("-")
                    if not meta.publication_year and year.isdigit():
                        meta.publication_year = int(year)

            if not meta.publisher:
                pub_match = PUBLISHER_RE.search(text)
                if pub_match:
                    cand = pub_match.group(1).strip()
                    if len(cand) > 3 and not any(w in cand.lower() for w in ("autor", "traductor", "isbn")):
                        meta.publisher = cand

            if not meta.publication_year:
                years = [int(y) for y in YEAR_RE.findall(text) if 1800 <= int(y) <= 2030]
                if years:
                    meta.publication_year = max(years)

        meta.isbns = list(isbns_found.values())
        if meta.isbns:
            digital_isbn = next(
                (r.isbn13 for r in meta.isbns if r.tag and any(k in r.tag for k in ("epub", "digital", "ebook"))),
                None,
            )
            meta.primary_isbn = digital_isbn or meta.isbns[0].isbn13

        if meta.title_page_text:
            lines = [line.strip() for line in meta.title_page_text.splitlines() if line.strip()]
            if lines:
                candidate_title = lines[0]
                if ":" in candidate_title:
                    parts = candidate_title.split(":", 1)
                    meta.canonical_title = parts[0].strip()
                    meta.subtitle = parts[1].strip()
                else:
                    meta.canonical_title = candidate_title

        return meta


# ==============================================================================
# 4. PIPELINE: Unified Ground Truth Extractor
# ==============================================================================


class ContainerArchaeologyPipeline:
    """Unified container pipeline for zero-Calibre high-fidelity inspection."""

    @classmethod
    def inspect(cls, file_path: Path | str) -> GroundTruthReport:
        start_time = time.perf_counter()
        p = Path(file_path)
        if not p.is_file():
            raise FileNotFoundError(f"Book file not found: {p}")

        suffix = p.suffix.lower()
        if suffix == ".epub":
            fmt = BookFormat.EPUB
            cover, meta, warnings = cls._inspect_epub(p)
        elif suffix == ".pdf":
            fmt = BookFormat.PDF
            cover, meta, warnings = cls._inspect_pdf(p)
        elif suffix in (".mobi", ".azw3"):
            fmt = BookFormat.MOBI if suffix == ".mobi" else BookFormat.AZW3
            cover, meta, warnings = cls._inspect_mobi(p)
        elif suffix == ".cbz":
            fmt = BookFormat.CBZ
            cover, meta, warnings = cls._inspect_cbz(p)
        elif suffix == ".cbr":
            fmt = BookFormat.CBR
            cover, meta, warnings = None, GroundTruthMetadata(), ["CBR is unsupported; no extraction attempted"]
        else:
            fmt = BookFormat.UNKNOWN
            cover, meta, warnings = None, GroundTruthMetadata(), [f"Unsupported format: {suffix}"]

        elapsed = (time.perf_counter() - start_time) * 1000.0
        return GroundTruthReport(
            format=fmt,
            cover=cover,
            metadata=meta,
            warnings=warnings,
            inspection_time_ms=elapsed,
        )

    @classmethod
    def _inspect_epub(cls, path: Path) -> tuple[ExtractedCover | None, GroundTruthMetadata, list[str]]:
        warnings: list[str] = []
        cover: ExtractedCover | None = None
        pages: list[tuple[str, str]] = []
        tracker = [0]

        preflight_zip_file(path)
        with zipfile.ZipFile(path, "r") as z:
            validate_zip_archive(z)
            try:
                container_bytes = read_zip_member_safely(z, "META-INF/container.xml", cumulative_tracker=tracker)
                container_xml = ET.fromstring(container_bytes)
                rootfile = container_xml.find(".//{*}rootfile")
                opf_path = rootfile.get("full-path") if rootfile is not None else None
            except Exception as e:
                warnings.append(f"Error reading container.xml: {e}")
                opf_path = None

            if not opf_path:
                opfs = [n for n in z.namelist() if n.lower().endswith(".opf")]
                opf_path = opfs[0] if opfs else None

            if not opf_path:
                warnings.append("No OPF package file found in EPUB")
                return None, GroundTruthMetadata(), warnings

            opf_dir = posixpath.dirname(opf_path)
            opf_bytes = read_zip_member_safely(z, opf_path, cumulative_tracker=tracker)
            opf = ET.fromstring(opf_bytes)

            manifest_items: dict[str, dict[str, str]] = {}
            for item in opf.findall(".//{*}manifest/{*}item"):
                iid = item.get("id")
                href = item.get("href")
                mtype = item.get("media-type", "")
                props = item.get("properties", "")
                if iid and href:
                    manifest_items[iid] = {"href": href, "media-type": mtype, "properties": props}

            # Level 1: EPUB 3 cover-image
            cover_href: str | None = None
            for item_info in manifest_items.values():
                if "cover-image" in item_info["properties"].split():
                    cover_href = item_info["href"]
                    break

            # Level 2: EPUB 2 <meta name="cover" content="id"/>
            if not cover_href:
                for meta in opf.findall(".//{*}metadata/{*}meta"):
                    if meta.get("name") == "cover":
                        cid = meta.get("content")
                        if cid and cid in manifest_items:
                            cover_href = manifest_items[cid]["href"]
                            break

            # Level 3: <guide><reference type="cover" href="..."/>
            if not cover_href:
                for ref in opf.findall(".//{*}guide/{*}reference"):
                    if ref.get("type") in ("cover", "other.ms-coverimage-standard"):
                        target_href = ref.get("href", "")
                        clean_target = target_href.split("#")[0]
                        if any(clean_target.lower().endswith(ext) for ext in (".jpg", ".jpeg", ".png", ".webp")):
                            cover_href = clean_target
                        elif clean_target:
                            try:
                                full_xh = posixpath.normpath(posixpath.join(opf_dir, clean_target))
                                xh_bytes = read_zip_member_safely(z, full_xh, cumulative_tracker=tracker)
                                xh = ET.fromstring(xh_bytes)
                                img_el = xh.find(".//{*}image")
                                if img_el is None:
                                    img_el = xh.find(".//{*}img")
                                if img_el is not None:
                                    img_src = (
                                        img_el.get("{http://www.w3.org/1999/xlink}href")
                                        or img_el.get("src")
                                        or img_el.get("href")
                                    )
                                    if img_src:
                                        cover_href = posixpath.normpath(
                                            posixpath.join(posixpath.dirname(clean_target), img_src)
                                        )
                            except Exception:
                                pass
                        if cover_href:
                            break

            # Level 4: Manifest heurístics
            if not cover_href:
                for iid, info in manifest_items.items():
                    if info["media-type"].startswith("image/") and (
                        iid.lower() in ("cover", "cover-image", "cover_img") or "cover" in info["href"].lower()
                    ):
                        cover_href = info["href"]
                        break

            # Level 5: Spine first item inspection
            spine_itemrefs = opf.findall(".//{*}spine/{*}itemref")
            if not cover_href and spine_itemrefs:
                first_id = spine_itemrefs[0].get("idref")
                if first_id and first_id in manifest_items:
                    sp_href = manifest_items[first_id]["href"]
                    try:
                        full_sp = posixpath.normpath(posixpath.join(opf_dir, sp_href))
                        sp_bytes = read_zip_member_safely(z, full_sp, cumulative_tracker=tracker)
                        sp_xml = ET.fromstring(sp_bytes)
                        img_el = sp_xml.find(".//{*}image")
                        if img_el is None:
                            img_el = sp_xml.find(".//{*}img")
                        if img_el is not None:
                            img_src = (
                                img_el.get("{http://www.w3.org/1999/xlink}href")
                                or img_el.get("src")
                                or img_el.get("href")
                            )
                            if img_src:
                                cover_href = posixpath.normpath(posixpath.join(posixpath.dirname(sp_href), img_src))
                    except Exception:
                        pass

            if cover_href:
                full_cover_path = posixpath.normpath(posixpath.join(opf_dir, cover_href))
                try:
                    cdata = read_zip_member_safely(z, full_cover_path, cumulative_tracker=tracker)
                    mime = detect_image_mime(cdata)
                    if mime:
                        cover = ExtractedCover(data=cdata, mime_type=mime, source_strategy="epub_manifest_resolved")
                except Exception as e:
                    warnings.append(f"Failed to read cover asset {full_cover_path}: {e}")

            for itemref in spine_itemrefs[:10]:
                iid = itemref.get("idref")
                if iid and iid in manifest_items:
                    sp_href = manifest_items[iid]["href"]
                    full_sp = posixpath.normpath(posixpath.join(opf_dir, sp_href))
                    try:
                        raw_bytes = read_zip_member_safely(
                            z, full_sp, max_bytes=2 * 1024 * 1024, cumulative_tracker=tracker
                        )
                        text = re.sub(r"<[^>]+>", " ", raw_bytes.decode("utf-8", "replace"))
                        text = " ".join(text.split())
                        if text:
                            pages.append((full_sp, text))
                    except Exception:
                        continue

        metadata = ContentMetadataMiner.mine_pages(pages)
        return cover, metadata, warnings

    @classmethod
    def _inspect_pdf(cls, path: Path) -> tuple[ExtractedCover | None, GroundTruthMetadata, list[str]]:
        warnings: list[str] = []
        cover: ExtractedCover | None = None
        pages: list[tuple[str, str]] = []

        try:
            import fitz  # type: ignore[import-untyped]  # PyMuPDF has no type marker

            doc = fitz.open(path)
            try:
                if len(doc) > 0:
                    page0 = doc.load_page(0)
                    images = page0.get_images(full=True)
                    # Adaptive hybrid: if page 1 has exactly 1 dominant image covering viewport
                    if len(images) == 1:
                        xref = images[0][0]
                        base_image = doc.extract_image(xref)
                        if base_image and base_image.get("image"):
                            cover = ExtractedCover(
                                data=base_image["image"],
                                mime_type=f"image/{base_image.get('ext', 'jpeg')}",
                                source_strategy="pdf_native_xobject",
                                width=base_image.get("width"),
                                height=base_image.get("height"),
                            )

                    # Fallback to rasterization if native XObject missing or complex composite
                    if cover is None:
                        zoom = 2.0
                        pix = page0.get_pixmap(matrix=fitz.Matrix(zoom, zoom))
                        cover = ExtractedCover(
                            data=pix.tobytes("jpeg"),
                            mime_type="image/jpeg",
                            source_strategy="pdf_rasterized_2x",
                            width=pix.width,
                            height=pix.height,
                        )

                    # Extract text from first 10 pages for CIP / ISBN mining
                    for idx in range(min(10, len(doc))):
                        pg = doc.load_page(idx)
                        txt = pg.get_text()
                        if txt and txt.strip():
                            pages.append((f"page_{idx + 1}", txt))
            finally:
                doc.close()
        except ImportError:
            warnings.append("PyMuPDF (fitz) is not installed; PDF deep extraction skipped")
        except Exception as e:
            warnings.append(f"PDF extraction error: {e}")

        metadata = ContentMetadataMiner.mine_pages(pages)
        return cover, metadata, warnings

    @classmethod
    def _inspect_mobi(cls, path: Path) -> tuple[ExtractedCover | None, GroundTruthMetadata, list[str]]:
        warnings: list[str] = []
        cover: ExtractedCover | None = None
        meta = GroundTruthMetadata()

        try:
            with open(path, "rb") as f:
                parser = MobiPalmDocParser(f)
                cover, exth = parser.extract_cover_and_metadata()
                if exth.get("title"):
                    meta.title = exth["title"]
                    meta.canonical_title = exth["title"]
                if exth.get("authors"):
                    meta.authors = exth["authors"]
                if exth.get("publisher"):
                    meta.publisher = exth["publisher"]
                if exth.get("isbn"):
                    can13 = to_canonical_isbn13(exth["isbn"])
                    if can13:
                        meta.isbns.append(ISBNRecord(isbn13=can13, isbn10=None, tag="mobi_exth", is_valid=True))
                        meta.primary_isbn = can13
                if exth.get("date"):
                    m = re.search(r"\b(19\d\d|20\d\d)\b", exth["date"])
                    if m:
                        meta.publication_year = int(m.group(1))
        except Exception as e:
            warnings.append(f"MOBI parsing error: {e}")

        return cover, meta, warnings

    @classmethod
    def _inspect_cbz(cls, path: Path) -> tuple[ExtractedCover | None, GroundTruthMetadata, list[str]]:
        warnings: list[str] = []
        cover: ExtractedCover | None = None
        meta = GroundTruthMetadata()
        tracker = [0]

        try:
            preflight_zip_file(path)
            with zipfile.ZipFile(path, "r") as z:
                validate_zip_archive(z)
                # 1. ComicInfo.xml if available
                if "ComicInfo.xml" in z.namelist():
                    try:
                        cinfo_bytes = read_zip_member_safely(z, "ComicInfo.xml", cumulative_tracker=tracker)
                        cxml = ET.fromstring(cinfo_bytes)
                        series = cxml.findtext("Series")
                        num = cxml.findtext("Number")
                        title = cxml.findtext("Title")
                        meta.title = title or (f"{series} #{num}" if series and num else None)
                        writer = cxml.findtext("Writer")
                        if writer:
                            meta.authors = [w.strip() for w in writer.split(",")]
                    except Exception:
                        pass

                # 2. Natural Alphanumeric Sort for Cover Image
                img_exts = (".jpg", ".jpeg", ".png", ".webp")
                candidates = [
                    n
                    for n in z.namelist()
                    if any(n.lower().endswith(ext) for ext in img_exts)
                    and not n.startswith("__MACOSX")
                    and not n.endswith("/")
                ]

                def natural_sort_key(s: str) -> list[Any]:
                    return [int(text) if text.isdigit() else text.lower() for text in re.split(r"(\d+)", s)]

                candidates.sort(key=natural_sort_key)
                if candidates:
                    first_img = candidates[0]
                    cdata = read_zip_member_safely(z, first_img, cumulative_tracker=tracker)
                    mime = detect_image_mime(cdata)
                    if mime:
                        cover = ExtractedCover(data=cdata, mime_type=mime, source_strategy="cbz_first_image")
        except Exception as e:
            warnings.append(f"CBZ extraction error: {e}")

        return cover, meta, warnings

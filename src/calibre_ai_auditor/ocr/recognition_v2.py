"""Bounded OCR and cover-vision recognition for manifestation V2.

Recognition may seed an exact, checksum-valid ISBN lookup, but a single OCR
engine or any vision model remains non-authoritative. Only agreement between
two distinctly named OCR engines emits ``ocr_consensus`` evidence.
"""

from __future__ import annotations

import hashlib
import json
import logging
import tempfile
from collections.abc import Callable
from pathlib import Path
from typing import Any, Protocol

from PIL import Image, UnidentifiedImageError

from calibre_ai_auditor.extractors.cover import extract_pdf_cover, extract_zip_cover
from calibre_ai_auditor.extractors.multiformat import (
    ISBN_CONTEXT_RE,
    ExtractedSnippet,
    FormatInspection,
)
from calibre_ai_auditor.security.files import SecurePathError, copy_file_beneath, sha256_file_beneath
from calibre_ai_auditor.verification.identity_v2 import (
    EvidenceSourceKind,
    FormatEvidenceStatus,
    SourceEvidence,
    validate_isbn,
)
from calibre_ai_auditor.verification.ocr_router import OCRPageResult
from calibre_ai_auditor.verification.pipeline_v2 import (
    BookSnapshot,
    EvidenceEnrichment,
)

logger = logging.getLogger(__name__)

MAX_RECOGNITION_FILE_BYTES = 512 * 1024 * 1024
MAX_OCR_TEXT_CHARS_PER_ENGINE = 40_000
MAX_COVER_BYTES = 20 * 1024 * 1024
MAX_COVER_PIXELS = 25_000_000


class PDFOCRBackend(Protocol):
    name: str

    async def ocr_pdf_pages(
        self,
        pdf_path: Path,
        *,
        page_range: str,
        language: str,
    ) -> list[OCRPageResult]: ...


class CoverVisionBackend(Protocol):
    name: str
    is_local: bool

    async def recognize_cover(self, cover_path: Path) -> dict[str, Any] | None: ...


CoverExtractor = Callable[[Path, Path], bool]


def _sha256(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as stream:
        for chunk in iter(lambda: stream.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def _source_root(path: Path, library_root: str | None) -> Path:
    return Path(library_root) if library_root is not None else path.parent


def _safe_source(inspection: FormatInspection, library_root: str | None) -> Path | None:
    path = Path(inspection.format_evidence.path)
    try:
        if (
            inspection.format_evidence.status is not FormatEvidenceStatus.readable
            or sha256_file_beneath(
                _source_root(path, library_root),
                path,
                max_bytes=MAX_RECOGNITION_FILE_BYTES,
            )
            != inspection.format_evidence.sha256
        ):
            return None
    except (OSError, SecurePathError):
        return None
    return path


def _stable_recognition_copy(
    source: Path,
    target: Path,
    expected_sha256: str,
    library_root: str | None,
) -> bool:
    try:
        copy_file_beneath(
            _source_root(source, library_root),
            source,
            target,
            max_bytes=MAX_RECOGNITION_FILE_BYTES,
            expected_sha256=expected_sha256,
        )
        return True
    except (OSError, SecurePathError):
        return False


def _isbn_candidates(text: str) -> set[str]:
    return {candidate for raw in ISBN_CONTEXT_RE.findall(text) if (candidate := validate_isbn(raw)) is not None}


def _seed_candidate_identifier(inspection: FormatInspection, isbn: str, evidence_id: str) -> bool:
    current = inspection.format_evidence.identifiers
    if current and current.get("isbn") != isbn:
        return False
    inspection.format_evidence.identifiers = {"isbn": isbn}
    if evidence_id not in inspection.format_evidence.evidence_ids:
        inspection.format_evidence.evidence_ids.append(evidence_id)
    return True


class OCRRecognitionEnricher:
    """OCR bounded PDF front matter and require cross-engine agreement for authority."""

    def __init__(
        self,
        *,
        backends: list[PDFOCRBackend],
        max_pages: int = 6,
        language: str = "en",
    ) -> None:
        self.backends = backends
        self.max_pages = min(max(max_pages, 1), 12)
        self.language = language

    async def collect(
        self,
        book: BookSnapshot,
        inspections: list[FormatInspection],
    ) -> EvidenceEnrichment:
        evidence: list[SourceEvidence] = []
        warnings: list[str] = []
        for inspection in inspections:
            if inspection.format_evidence.format != "PDF" or inspection.format_evidence.identifiers:
                continue
            path = _safe_source(inspection, book.library_root)
            if path is None:
                warnings.append(f"ocr_source_changed_or_unsafe:{Path(inspection.format_evidence.path).name}")
                continue

            by_engine: dict[str, set[str]] = {}
            initial_snippet_count = len(inspection.snippets)
            with tempfile.TemporaryDirectory(prefix="bookaudit-ocr-") as tmp:
                stable_path = Path(tmp) / f"source{path.suffix.lower()}"
                if not _stable_recognition_copy(
                    path,
                    stable_path,
                    inspection.format_evidence.sha256,
                    book.library_root,
                ):
                    warnings.append(f"ocr_source_changed_or_unsafe:{path.name}")
                    continue
                for backend in self.backends:
                    try:
                        pages = await backend.ocr_pdf_pages(
                            stable_path,
                            page_range=f"1-{self.max_pages}",
                            language=self.language,
                        )
                    except Exception as exc:  # pragma: no cover - optional backend failures vary
                        logger.warning("OCR backend %s failed closed for %s: %s", backend.name, path.name, exc)
                        warnings.append(f"ocr_backend_failed:{backend.name}:{path.name}")
                        continue
                    text = "\n".join(page.text for page in pages if page.text)[:MAX_OCR_TEXT_CHARS_PER_ENGINE]
                    if text:
                        locator = ",".join(str(page.page_number) for page in pages if page.text)
                        inspection.snippets.append(
                            ExtractedSnippet(
                                source="copyright_page",
                                text=text,
                                locator=f"ocr:{backend.name}:pages:{locator}",
                                page_range=locator,
                            )
                        )
                    candidates = _isbn_candidates(text)
                    if candidates:
                        by_engine.setdefault(backend.name, set()).update(candidates)

                if (
                    _sha256(stable_path) != inspection.format_evidence.sha256
                    or _safe_source(inspection, book.library_root) is None
                ):
                    del inspection.snippets[initial_snippet_count:]
                    warnings.append(f"ocr_source_changed_or_unsafe:{path.name}")
                    continue

            if not by_engine:
                continue
            distinct_sets = {frozenset(values) for values in by_engine.values()}
            if len(distinct_sets) != 1 or len(next(iter(distinct_sets))) != 1:
                warnings.append(f"ocr_identifier_conflict:{path.name}")
                continue
            isbn = next(iter(next(iter(distinct_sets))))
            engine_names = sorted(by_engine)
            authoritative = len(engine_names) >= 2
            source_kind = EvidenceSourceKind.ocr_consensus if authoritative else EvidenceSourceKind.ocr_observation
            evidence_id = (
                "ev_"
                + hashlib.sha256(
                    f"ocr\0{inspection.format_evidence.sha256}\0{isbn}\0{','.join(engine_names)}".encode()
                ).hexdigest()[:20]
            )
            if not _seed_candidate_identifier(inspection, isbn, evidence_id):
                warnings.append(f"ocr_identifier_conflict:{path.name}")
                continue
            evidence.append(
                SourceEvidence(
                    evidence_id=evidence_id,
                    root_id=f"ocr:{inspection.format_evidence.sha256}:{'+'.join(engine_names)}",
                    independence_root="book_content",
                    source_kind=source_kind,
                    field="identifiers",
                    value={"isbn": isbn},
                    manifestation_ids={"isbn": isbn},
                    locator=f"pages:1-{self.max_pages}",
                    artifact_sha256=inspection.format_evidence.sha256,
                    authoritative=authoritative,
                )
            )
        return EvidenceEnrichment(evidence=evidence, warnings=warnings)


def _default_cover_extractor(source: Path, target: Path) -> bool:
    suffix = source.suffix.casefold()
    if suffix == ".pdf":
        return extract_pdf_cover(source, target)
    if suffix in {".epub", ".cbz", ".zip"}:
        return extract_zip_cover(source, target)
    return False


def _valid_cover(path: Path) -> bool:
    try:
        if not path.is_file() or path.stat().st_size > MAX_COVER_BYTES:
            return False
        with Image.open(path) as image:
            if image.width * image.height > MAX_COVER_PIXELS:
                return False
            image.verify()
    except (OSError, UnidentifiedImageError, Image.DecompressionBombError):
        return False
    return True


class VisionRecognitionEnricher:
    """Use cover vision for review context under an explicit image-egress gate."""

    def __init__(
        self,
        *,
        backend: CoverVisionBackend | None,
        allow_remote_images: bool,
        run_allows_remote_images: bool,
        max_images: int,
        cover_extractor: CoverExtractor = _default_cover_extractor,
    ) -> None:
        self.backend = backend
        self.allow_remote_images = allow_remote_images
        self.run_allows_remote_images = run_allows_remote_images
        self.max_images = min(max(max_images, 0), 4)
        self.cover_extractor = cover_extractor

    async def collect(
        self,
        book: BookSnapshot,
        inspections: list[FormatInspection],
    ) -> EvidenceEnrichment:
        if self.backend is None:
            return EvidenceEnrichment(warnings=["vision_provider_unavailable"])
        remote = not self.backend.is_local
        if remote and not (self.allow_remote_images and self.run_allows_remote_images):
            return EvidenceEnrichment(warnings=["remote_vision_images_not_authorized"])

        evidence: list[SourceEvidence] = []
        receipts: list[dict[str, Any]] = []
        warnings: list[str] = []
        processed = 0
        for inspection in inspections:
            if processed >= self.max_images:
                break
            source = _safe_source(inspection, book.library_root)
            if source is None:
                warnings.append(f"vision_source_changed_or_unsafe:{Path(inspection.format_evidence.path).name}")
                continue
            with tempfile.TemporaryDirectory(prefix="bookaudit-vision-") as tmp:
                stable_source = Path(tmp) / f"source{source.suffix.lower()}"
                if not _stable_recognition_copy(
                    source,
                    stable_source,
                    inspection.format_evidence.sha256,
                    book.library_root,
                ):
                    warnings.append(f"vision_source_changed_or_unsafe:{source.name}")
                    continue
                cover = Path(tmp) / "cover.png"
                if not self.cover_extractor(stable_source, cover) or not _valid_cover(cover):
                    warnings.append(f"vision_cover_unavailable:{source.name}")
                    continue
                cover_sha = _sha256(cover)
                processed += 1
                receipt_payload = {
                    "book_key": book.book_key,
                    "format_sha256": inspection.format_evidence.sha256,
                    "cover_sha256": cover_sha,
                    "provider": self.backend.name,
                }
                receipts.append(
                    {
                        "book_key": book.book_key,
                        "text_chars": 0,
                        "image_count": 1,
                        "payload_sha256": hashlib.sha256(
                            json.dumps(receipt_payload, sort_keys=True, separators=(",", ":")).encode()
                        ).hexdigest(),
                        "provider": self.backend.name,
                        "remote": remote,
                    }
                )
                try:
                    result = await self.backend.recognize_cover(cover)
                except Exception as exc:  # pragma: no cover - provider-specific failures
                    logger.warning("Vision backend failed closed for %s: %s", source.name, exc)
                    warnings.append(f"vision_backend_failed:{source.name}")
                    continue
                if not isinstance(result, dict):
                    warnings.append(f"vision_response_rejected:{source.name}")
                    continue
                if (
                    _sha256(stable_source) != inspection.format_evidence.sha256
                    or _safe_source(inspection, book.library_root) is None
                ):
                    warnings.append(f"vision_source_changed_or_unsafe:{source.name}")
                    continue

                raw_isbn = result.get("isbn")
                isbn = validate_isbn(raw_isbn) if isinstance(raw_isbn, str) else None
                manifestation_ids = {"isbn": isbn} if isbn else {}
                values: dict[str, Any] = {
                    "title": result.get("title") if isinstance(result.get("title"), str) else None,
                    "authors": (
                        [str(item).strip() for item in result.get("authors", []) if str(item).strip()]
                        if isinstance(result.get("authors"), list)
                        else None
                    ),
                    "publisher": (result.get("publisher") if isinstance(result.get("publisher"), str) else None),
                    "identifiers": manifestation_ids or None,
                }
                root_id = f"vision:{cover_sha}"
                generated: list[SourceEvidence] = []
                for field, value in values.items():
                    if value in (None, [], {}):
                        continue
                    evidence_id = "ev_" + hashlib.sha256(f"{root_id}\0{field}".encode()).hexdigest()[:20]
                    generated.append(
                        SourceEvidence(
                            evidence_id=evidence_id,
                            root_id=root_id,
                            independence_root="vision",
                            source_kind=EvidenceSourceKind.vision,
                            field=field,
                            value=value,
                            manifestation_ids=manifestation_ids,
                            locator="cover:first-page",
                            artifact_sha256=cover_sha,
                            authoritative=False,
                        )
                    )
                if isbn:
                    identifier_evidence = next(
                        (item for item in generated if item.field == "identifiers"),
                        None,
                    )
                    if identifier_evidence and not _seed_candidate_identifier(
                        inspection,
                        isbn,
                        identifier_evidence.evidence_id,
                    ):
                        warnings.append(f"vision_identifier_conflict:{source.name}")
                evidence.extend(generated)
        return EvidenceEnrichment(
            evidence=evidence,
            privacy_receipts=receipts,
            warnings=warnings,
        )

from __future__ import annotations

import asyncio
import hashlib
import signal
from pathlib import Path
from typing import Any

import pytest
from PIL import Image

from calibre_ai_auditor.extractors.multiformat import FormatInspection
from calibre_ai_auditor.ocr.recognition_v2 import OCRRecognitionEnricher, VisionRecognitionEnricher
from calibre_ai_auditor.providers.evidence_v2 import (
    CompositeEvidenceEnricher,
    StructuredEvidenceEnricher,
)
from calibre_ai_auditor.verification.identity_v2 import (
    EvidenceSourceKind,
    FormatEvidence,
    FormatEvidenceStatus,
    SourceEvidence,
)
from calibre_ai_auditor.verification.ocr_router import (
    OCRPageResult,
    OCRProcessTimeoutError,
    OCRQuality,
    TesseractProvider,
)
from calibre_ai_auditor.verification.pipeline_v2 import BookSnapshot

ISBN = "9780306406157"


@pytest.mark.asyncio
async def test_tesseract_timeout_kills_the_entire_ocr_process_group(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    pdf = tmp_path / "scan.pdf"
    pdf.write_bytes(b"pdf")
    process_group_kills: list[tuple[int, signal.Signals]] = []
    options: dict[str, object] = {}

    class HangingProcess:
        pid = 4242
        returncode: int | None = None

        async def communicate(self) -> tuple[None, None]:
            await asyncio.Future()
            return None, None

        async def wait(self) -> int:
            self.returncode = -signal.SIGKILL
            return self.returncode

        def kill(self) -> None:
            self.returncode = -signal.SIGKILL

    async def create_process(*_args: object, **kwargs: object) -> HangingProcess:
        options.update(kwargs)
        return HangingProcess()

    monkeypatch.setattr(asyncio, "create_subprocess_exec", create_process)
    monkeypatch.setattr(
        "calibre_ai_auditor.verification.ocr_router.os.killpg",
        lambda pid, sig: process_group_kills.append((pid, sig)),
    )

    with pytest.raises(OCRProcessTimeoutError):
        await TesseractProvider(timeout_seconds=0.01).ocr_pdf_pages(pdf, page_range="1-2")

    assert options["start_new_session"] is True
    assert process_group_kills == [(4242, signal.SIGKILL)]


def _inspection(path: Path) -> FormatInspection:
    digest = hashlib.sha256(path.read_bytes()).hexdigest()
    return FormatInspection(
        format_evidence=FormatEvidence(
            path=str(path),
            format="PDF",
            sha256=digest,
            status=FormatEvidenceStatus.readable,
            title="The Exact Book",
            authors=["Ada Author"],
            languages=["eng"],
        )
    )


def _book(path: Path) -> BookSnapshot:
    return BookSnapshot(
        book_key="calibre:1",
        calibre_book_id=1,
        current_metadata={},
        files=[str(path)],
        snapshot_sha256="a" * 64,
    )


class _OCRBackend:
    def __init__(self, name: str, text: str) -> None:
        self.name = name
        self.text = text
        self.calls = 0

    async def ocr_pdf_pages(self, _path: Path, **_kwargs: Any) -> list[OCRPageResult]:
        self.calls += 1
        return [
            OCRPageResult(
                page_number=2,
                text=self.text,
                quality=OCRQuality.high,
                provider=self.name,
                confidence=0.95,
            )
        ]


class _MutatingOCRBackend(_OCRBackend):
    async def ocr_pdf_pages(self, path: Path, **kwargs: Any) -> list[OCRPageResult]:
        result = await super().ocr_pdf_pages(path, **kwargs)
        path.write_bytes(b"replacement edition")
        return result


class _ExactProvider:
    def __init__(self) -> None:
        self.requested_isbns: list[str] = []

    async def fetch_by_isbn(self, isbn: str) -> list[SourceEvidence]:
        self.requested_isbns.append(isbn)
        return [
            SourceEvidence(
                evidence_id="provider-title",
                root_id="test-provider:record-1",
                independence_root="test-provider",
                source_kind=EvidenceSourceKind.provider_structured,
                field="title",
                value="The Exact Book",
                manifestation_ids={"isbn": isbn},
                artifact_sha256="b" * 64,
                authoritative=True,
            )
        ]


@pytest.mark.asyncio
async def test_single_ocr_engine_can_seed_exact_web_lookup_but_is_not_authoritative(tmp_path: Path) -> None:
    path = tmp_path / "scan.pdf"
    path.write_bytes(b"stable scanned pdf")
    inspection = _inspection(path)
    backend = _OCRBackend("tesseract", f"Copyright page\nISBN {ISBN}")

    result = await OCRRecognitionEnricher(backends=[backend]).collect(_book(path), [inspection])

    assert inspection.format_evidence.identifiers == {"isbn": ISBN}
    assert len(result.evidence) == 1
    assert result.evidence[0].source_kind is EvidenceSourceKind.ocr_observation
    assert result.evidence[0].authoritative is False
    assert inspection.snippets[0].source == "copyright_page"


@pytest.mark.asyncio
async def test_composite_runs_ocr_before_exact_structured_provider_lookup(tmp_path: Path) -> None:
    path = tmp_path / "scan.pdf"
    path.write_bytes(b"stable scanned pdf")
    inspection = _inspection(path)
    provider = _ExactProvider()
    enricher = CompositeEvidenceEnricher(
        [
            OCRRecognitionEnricher(backends=[_OCRBackend("tesseract", f"ISBN {ISBN}")]),
            StructuredEvidenceEnricher([provider]),
        ]
    )

    result = await enricher.collect(_book(path), [inspection])

    assert provider.requested_isbns == [ISBN]
    assert inspection.format_evidence.identifiers == {"isbn": ISBN}
    assert [item.source_kind for item in result.evidence] == [
        EvidenceSourceKind.ocr_observation,
        EvidenceSourceKind.provider_structured,
    ]
    assert result.evidence[0].authoritative is False
    assert result.evidence[1].authoritative is True


@pytest.mark.asyncio
async def test_two_distinct_ocr_engines_must_agree_before_emitting_consensus(tmp_path: Path) -> None:
    path = tmp_path / "scan.pdf"
    path.write_bytes(b"stable scanned pdf")
    inspection = _inspection(path)
    backends = [
        _OCRBackend("tesseract", f"ISBN: {ISBN}"),
        _OCRBackend("paddleocr", "ISBN 978-0-306-40615-7"),
    ]

    result = await OCRRecognitionEnricher(backends=backends).collect(_book(path), [inspection])

    assert inspection.format_evidence.identifiers == {"isbn": ISBN}
    assert len(result.evidence) == 1
    assert result.evidence[0].source_kind is EvidenceSourceKind.ocr_consensus
    assert result.evidence[0].authoritative is True
    assert result.evidence[0].independence_root == "book_content"


@pytest.mark.asyncio
async def test_conflicting_ocr_engines_do_not_seed_an_identifier(tmp_path: Path) -> None:
    path = tmp_path / "scan.pdf"
    path.write_bytes(b"stable scanned pdf")
    inspection = _inspection(path)
    backends = [
        _OCRBackend("tesseract", f"ISBN: {ISBN}"),
        _OCRBackend("paddleocr", "ISBN: 9783161484100"),
    ]

    result = await OCRRecognitionEnricher(backends=backends).collect(_book(path), [inspection])

    assert inspection.format_evidence.identifiers == {}
    assert result.evidence == []
    assert result.warnings == ["ocr_identifier_conflict:scan.pdf"]


@pytest.mark.asyncio
async def test_ocr_discards_results_when_source_changes_during_recognition(tmp_path: Path) -> None:
    path = tmp_path / "scan.pdf"
    path.write_bytes(b"stable scanned pdf")
    inspection = _inspection(path)

    result = await OCRRecognitionEnricher(backends=[_MutatingOCRBackend("tesseract", f"ISBN {ISBN}")]).collect(
        _book(path), [inspection]
    )

    assert inspection.format_evidence.identifiers == {}
    assert result.evidence == []
    assert result.warnings == ["ocr_source_changed_or_unsafe:scan.pdf"]


class _VisionBackend:
    name = "vision-test"

    def __init__(self, *, is_local: bool) -> None:
        self.is_local = is_local
        self.calls = 0

    async def recognize_cover(self, _path: Path) -> dict[str, Any]:
        self.calls += 1
        return {
            "title": "The Exact Book",
            "authors": ["Ada Author"],
            "isbn": ISBN,
            "confidence": 0.99,
        }


def _write_cover(_source: Path, target: Path) -> bool:
    Image.new("RGB", (20, 30), color="navy").save(target, format="PNG")
    return True


@pytest.mark.asyncio
async def test_remote_vision_requires_config_and_per_run_image_consent(tmp_path: Path) -> None:
    path = tmp_path / "scan.pdf"
    path.write_bytes(b"stable scanned pdf")
    backend = _VisionBackend(is_local=False)

    result = await VisionRecognitionEnricher(
        backend=backend,
        allow_remote_images=True,
        run_allows_remote_images=False,
        max_images=1,
        cover_extractor=_write_cover,
    ).collect(_book(path), [_inspection(path)])

    assert backend.calls == 0
    assert result.evidence == []
    assert result.warnings == ["remote_vision_images_not_authorized"]


@pytest.mark.asyncio
async def test_vision_is_receipted_nonauthoritative_and_only_seeds_review_lookup(tmp_path: Path) -> None:
    path = tmp_path / "scan.pdf"
    path.write_bytes(b"stable scanned pdf")
    inspection = _inspection(path)
    backend = _VisionBackend(is_local=True)

    result = await VisionRecognitionEnricher(
        backend=backend,
        allow_remote_images=False,
        run_allows_remote_images=False,
        max_images=1,
        cover_extractor=_write_cover,
    ).collect(_book(path), [inspection])

    assert backend.calls == 1
    assert inspection.format_evidence.identifiers == {"isbn": ISBN}
    assert {item.field for item in result.evidence} == {"authors", "identifiers", "title"}
    assert all(item.source_kind is EvidenceSourceKind.vision for item in result.evidence)
    assert all(item.authoritative is False for item in result.evidence)
    assert result.privacy_receipts[0]["image_count"] == 1
    assert result.privacy_receipts[0]["remote"] is False

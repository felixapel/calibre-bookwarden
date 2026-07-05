"""OCRRouter — multi-provider OCR with per-page-type routing.

Picks the best OCR backend per page based on:
- Page classifier hint (text_present | clean_scan | noisy_scan | table_heavy | multilingual)
- Available providers (feature-flagged at import time)
- Per-host GPU capability

Backends (all optional, all behind feature flags):
  - Tesseract (always available if binary installed; subprocess via ocrmypdf)
  - PaddleOCR (best for clean printed Latin/CJK, runs on any GPU or CPU)
  - Surya (best for multilingual / handwriting / old books, GPU-heavy)

This module is import-safe: if paddleocr or surya aren't installed, the providers
are simply not registered.  Tesseract is the universal fallback.
"""

from __future__ import annotations

import asyncio
import logging
from dataclasses import dataclass, field
from enum import StrEnum
from pathlib import Path
from typing import Any, Protocol

logger = logging.getLogger(__name__)


class PageHint(StrEnum):
    """Per-page classification that drives OCR backend choice."""

    text_present = "text_present"
    clean_scan = "clean_scan"
    noisy_scan = "noisy_scan"
    table_heavy = "table_heavy"
    multilingual = "multilingual"
    unknown = "unknown"


class OCRQuality(StrEnum):
    """Self-reported quality of the OCR result."""

    high = "high"
    medium = "medium"
    low = "low"


@dataclass
class OCRPageResult:
    """OCR result for a single page."""

    page_number: int
    text: str
    quality: OCRQuality
    provider: str
    confidence: float  # 0.0–1.0
    bbox: list[tuple[float, float, float, float]] = field(default_factory=list)
    duration_ms: int = 0


class OCRProvider(Protocol):
    """Minimal OCR provider interface."""

    name: str
    requires_gpu: bool
    supports_languages: list[str]
    best_for: list[PageHint]

    async def ocr_page(self, image: bytes, *, language: str = "en") -> OCRPageResult: ...
    async def ocr_pages(self, images: list[bytes], *, language: str = "en") -> list[OCRPageResult]: ...
    async def health(self) -> bool: ...


# ---------------------------------------------------------------------------
# Tesseract provider (subprocess-backed, always available if ocrmypdf installed)
# ---------------------------------------------------------------------------


class TesseractProvider:
    name = "tesseract"
    requires_gpu = False
    supports_languages = ["en", "de", "fr", "es", "it", "pt", "nl", "ru", "ja", "zh"]
    best_for = [PageHint.clean_scan, PageHint.unknown]

    def __init__(self, ocrmypdf_path: str = "ocrmypdf", tesseract_lang: str = "eng"):
        self.ocrmypdf_path = ocrmypdf_path
        self.tesseract_lang = tesseract_lang

    async def ocr_page(self, image: bytes, *, language: str = "en") -> OCRPageResult:
        raise NotImplementedError(
            "TesseractProvider.ocr_page expects a pre-rendered image; "
            "for PDF use TesseractProvider.ocr_pdf_pages()."
        )

    async def ocr_pages(self, images: list[bytes], *, language: str = "en") -> list[OCRPageResult]:
        raise NotImplementedError("Use ocr_pdf_pages for PDFs.")

    async def health(self) -> bool:
        proc = await asyncio.create_subprocess_exec(
            self.ocrmypdf_path, "--version",
            stdout=asyncio.subprocess.PIPE,
            stderr=asyncio.subprocess.PIPE,
        )
        await proc.communicate()
        return proc.returncode == 0

    async def ocr_pdf_pages(
        self,
        pdf_path: Path,
        *,
        page_range: str = "1-3",
        language: str = "en",
    ) -> list[OCRPageResult]:
        """Run ocrmypdf on the PDF and parse the sidecar text per page.

        ocrmypdf outputs a sidecar .txt file with form-feed-separated pages.
        """
        import tempfile

        with tempfile.TemporaryDirectory() as tmp:
            tmp_dir = Path(tmp)
            sidecar = tmp_dir / "sidecar.txt"
            out_pdf = tmp_dir / "out.pdf"
            cmd = [
                self.ocrmypdf_path,
                "--sidecar", str(sidecar),
                "--pages", page_range,
                "--optimize", "0",
                "--skip-text",
                "--language", self._map_lang(language),
                str(pdf_path),
                str(out_pdf),
            ]
            proc = await asyncio.create_subprocess_exec(
                *cmd,
                stdout=asyncio.subprocess.PIPE,
                stderr=asyncio.subprocess.PIPE,
            )
            stdout, stderr = await proc.communicate()
            if proc.returncode != 0:
                logger.error("ocrmypdf failed: %s", stderr.decode(errors="replace")[:500])
                return []
            if not sidecar.exists():
                return []
            text = sidecar.read_text(errors="replace")
            pages = text.split("\f")
            results = []
            for i, page_text in enumerate(pages, start=1):
                clean = page_text.strip()
                if not clean:
                    continue
                quality = self._assess_quality(clean)
                results.append(
                    OCRPageResult(
                        page_number=i,
                        text=clean,
                        quality=quality,
                        provider=self.name,
                        confidence=0.85 if quality == OCRQuality.high else 0.6,
                        duration_ms=0,
                    )
                )
            return results

    @staticmethod
    def _map_lang(lang: str) -> str:
        # ocrmypdf uses ISO 639-2/B codes; "en" -> "eng"
        mapping = {
            "en": "eng", "de": "deu", "fr": "fra", "es": "spa", "it": "ita",
            "pt": "por", "nl": "nld", "ru": "rus", "ja": "jpn", "zh": "chi_sim",
        }
        return mapping.get(lang, "eng")

    @staticmethod
    def _assess_quality(text: str) -> OCRQuality:
        if not text:
            return OCRQuality.low
        non_printable = sum(1 for c in text if not c.isprintable() and c not in "\n\t ")
        ratio = non_printable / max(len(text), 1)
        if ratio > 0.1:
            return OCRQuality.low
        word_count = len(text.split())
        if word_count < 5:
            return OCRQuality.low
        return OCRQuality.high


# ---------------------------------------------------------------------------
# PaddleOCR provider (optional, feature-flagged)
# ---------------------------------------------------------------------------


class PaddleOCRProvider:
    name = "paddleocr"
    requires_gpu = False  # can run on CPU; faster on GPU
    supports_languages = ["en", "ch", "fr", "de", "ja", "ko", "ru"]
    best_for = [PageHint.clean_scan, PageHint.table_heavy]

    def __init__(self, lang: str = "en", use_gpu: bool = False):
        self.lang = lang
        self.use_gpu = use_gpu
        self._ocr: Any = None

    def _ensure_loaded(self) -> Any:
        if self._ocr is None:
            try:
                from paddleocr import PaddleOCR  # type: ignore
            except ImportError as e:
                raise RuntimeError(
                    "paddleocr not installed. Install with: pip install paddleocr paddlepaddle"
                ) from e
            self._ocr = PaddleOCR(use_angle_cls=True, lang=self.lang, use_gpu=self.use_gpu)
        return self._ocr

    async def ocr_page(self, image: bytes, *, language: str = "en") -> OCRPageResult:
        raise NotImplementedError("Use ocr_pdf_pages for PDFs.")

    async def ocr_pages(self, images: list[bytes], *, language: str = "en") -> list[OCRPageResult]:
        raise NotImplementedError("Use ocr_pdf_pages for PDFs.")

    async def health(self) -> bool:
        try:
            self._ensure_loaded()
            return True
        except Exception:
            return False

    async def ocr_pdf_pages(
        self,
        pdf_path: Path,
        *,
        page_range: str = "1-3",
        language: str = "en",
    ) -> list[OCRPageResult]:
        """Render each page to an image, then OCR with PaddleOCR."""
        import fitz  # PyMuPDF

        ocr = await asyncio.to_thread(self._ensure_loaded)
        doc = await asyncio.to_thread(fitz.open, str(pdf_path))
        try:
            page_nums = self._parse_page_range(page_range, len(doc))
            results: list[OCRPageResult] = []
            for pnum in page_nums:
                page = doc.load_page(pnum - 1)
                pix = await asyncio.to_thread(page.get_pixmap, matrix=fitz.Matrix(2.0, 2.0))
                img_bytes = pix.tobytes("png")
                loop = asyncio.get_event_loop()
                ocr_result = await loop.run_in_executor(
                    None, lambda b=img_bytes: ocr.ocr(b, cls=True)
                )
                # Parse PaddleOCR result: [[(box, (text, conf))], ...]
                text_lines = []
                confidences = []
                if ocr_result and ocr_result[0]:
                    for line in ocr_result[0]:
                        if line and len(line) >= 2:
                            text_lines.append(line[1][0])
                            confidences.append(float(line[1][1]))
                joined = "\n".join(text_lines)
                avg_conf = sum(confidences) / len(confidences) if confidences else 0.0
                quality = (
                    OCRQuality.high if avg_conf > 0.9
                    else OCRQuality.medium if avg_conf > 0.7
                    else OCRQuality.low
                )
                results.append(
                    OCRPageResult(
                        page_number=pnum,
                        text=joined,
                        quality=quality,
                        provider=self.name,
                        confidence=avg_conf,
                    )
                )
            return results
        finally:
            doc.close()

    @staticmethod
    def _parse_page_range(page_range: str, total: int) -> list[int]:
        """Parse '1-3' / '1,3,5' / '2' into a sorted list of 1-indexed page numbers."""
        if not page_range:
            return list(range(1, total + 1))
        pages: set[int] = set()
        for part in page_range.split(","):
            part = part.strip()
            if "-" in part:
                a, b = part.split("-", 1)
                pages.update(range(int(a), int(b) + 1))
            else:
                pages.add(int(part))
        return sorted(p for p in pages if 1 <= p <= total)


# ---------------------------------------------------------------------------
# Surya provider (optional, GPU-heavy)
# ---------------------------------------------------------------------------


class SuryaProvider:
    name = "surya"
    requires_gpu = True
    supports_languages = ["en", "de", "fr", "es", "it", "pt", "ru", "zh", "ja", "ko", "ar"]
    best_for = [PageHint.noisy_scan, PageHint.multilingual]

    def __init__(self, device: str = "cuda"):
        self.device = device
        self._predictor: Any = None

    def _ensure_loaded(self) -> Any:
        if self._predictor is None:
            try:
                from surya.model.detection.model import (
                    load_model as load_det_model,
                )
                from surya.model.detection.model import (
                    load_processor as load_det_processor,
                )
                from surya.model.recognition.model import load_model as load_rec_model
                from surya.model.recognition.processor import load_processor as load_rec_processor
            except ImportError as e:
                raise RuntimeError(
                    "surya-ocr not installed. Install with: pip install surya-ocr"
                ) from e
            self._predictor = {
                "det_model": load_det_model(),
                "det_processor": load_det_processor(),
                "rec_model": load_rec_model(),
                "rec_processor": load_rec_processor(),
            }
        return self._predictor

    async def ocr_page(self, image: bytes, *, language: str = "en") -> OCRPageResult:
        raise NotImplementedError("Use ocr_pdf_pages for PDFs.")

    async def ocr_pages(self, images: list[bytes], *, language: str = "en") -> list[OCRPageResult]:
        raise NotImplementedError("Use ocr_pdf_pages for PDFs.")

    async def health(self) -> bool:
        try:
            self._ensure_loaded()
            return True
        except Exception:
            return False

    async def ocr_pdf_pages(
        self,
        pdf_path: Path,
        *,
        page_range: str = "1-3",
        language: str = "en",
    ) -> list[OCRPageResult]:
        """OCR PDF pages with Surya — high quality on noisy/multilingual scans."""
        import io as _io

        import fitz
        from PIL import Image
        from surya.ocr import run_ocr

        predictor = await asyncio.to_thread(self._ensure_loaded)
        doc = await asyncio.to_thread(fitz.open, str(pdf_path))
        try:
            page_nums = PaddleOCRProvider._parse_page_range(page_range, len(doc))
            images: list[Image.Image] = []
            for pnum in page_nums:
                page = doc.load_page(pnum - 1)
                pix = await asyncio.to_thread(page.get_pixmap, matrix=fitz.Matrix(2.0, 2.0))
                images.append(Image.open(_io.BytesIO(pix.tobytes("png"))))
            langs = [language] * len(images)
            loop = asyncio.get_event_loop()
            predictions = await loop.run_in_executor(
                None,
                lambda: run_ocr(
                    images, langs,
                    predictor["det_model"], predictor["det_processor"],
                    predictor["rec_model"], predictor["rec_processor"],
                ),
            )
            results: list[OCRPageResult] = []
            for pnum, pred in zip(page_nums, predictions):
                text_lines = [line.text for line in pred.text_lines]
                confs = [line.confidence for line in pred.text_lines]
                avg_conf = sum(confs) / len(confs) if confs else 0.0
                quality = (
                    OCRQuality.high if avg_conf > 0.85
                    else OCRQuality.medium if avg_conf > 0.6
                    else OCRQuality.low
                )
                results.append(
                    OCRPageResult(
                        page_number=pnum,
                        text="\n".join(text_lines),
                        quality=quality,
                        provider=self.name,
                        confidence=avg_conf,
                    )
                )
            return results
        finally:
            doc.close()


# ---------------------------------------------------------------------------
# OCRRouter — picks the best provider per page hint
# ---------------------------------------------------------------------------


@dataclass
class OCRRouterConfig:
    """Settings for the OCR router."""

    enable_paddleocr: bool = False
    enable_surya: bool = False
    enable_tesseract: bool = True
    paddleocr_use_gpu: bool = False
    surya_device: str = "cuda"
    default_language: str = "en"
    # Hosts available for OCR (GPU capability flag controls routing decisions)
    gpu_hosts: list[str] = field(default_factory=list)


class OCRRouter:
    """Routes OCR jobs to the best available provider per page hint.

    Routing priority:
      noisy_scan / multilingual → Surya (if enabled and GPU available)
      table_heavy               → PaddleOCR (if enabled)
      clean_scan / unknown      → Tesseract (always available fallback)

    The router is async and accepts a list of (page_index, page_bytes) tuples.
    """

    def __init__(self, config: OCRRouterConfig | None = None):
        self.config = config or OCRRouterConfig()
        self.providers: dict[str, OCRProvider] = {}
        self._init_providers()

    def _init_providers(self) -> None:
        if self.config.enable_tesseract:
            self.providers["tesseract"] = TesseractProvider()
        if self.config.enable_paddleocr:
            try:
                self.providers["paddleocr"] = PaddleOCRProvider(
                    lang=self.config.default_language, use_gpu=self.config.paddleocr_use_gpu
                )
            except Exception as e:
                logger.warning("PaddleOCR provider disabled: %s", e)
        if self.config.enable_surya and self.config.gpu_hosts:
            try:
                self.providers["surya"] = SuryaProvider(device=self.config.surya_device)
            except Exception as e:
                logger.warning("Surya provider disabled: %s", e)

    def available_providers(self) -> list[str]:
        return list(self.providers.keys())

    def choose_provider(self, hint: PageHint) -> OCRProvider:
        """Pick the best provider for a given page hint.

        Fallback chain:
          1. Best match by hint + availability
          2. First registered provider
          3. Tesseract (always last resort)
        """
        for pname, provider in self.providers.items():
            if hint in provider.best_for:
                return provider
        # Fallback to first available
        if self.providers:
            return next(iter(self.providers.values()))
        raise RuntimeError("No OCR providers available. Enable at least tesseract.")

    async def ocr_pdf_pages(
        self,
        pdf_path: Path,
        *,
        page_range: str = "1-3",
        hint: PageHint = PageHint.unknown,
        language: str = "en",
    ) -> list[OCRPageResult]:
        """OCR all pages in the range, routing by hint."""
        provider = self.choose_provider(hint)
        logger.info(
            "OCR routing: hint=%s → provider=%s for %s",
            hint.value, provider.name, pdf_path.name,
        )
        # PaddleOCR / Surya expose .ocr_pdf_pages too; Tesseract already has it
        if hasattr(provider, "ocr_pdf_pages"):
            return await provider.ocr_pdf_pages(pdf_path, page_range=page_range, language=language)
        raise RuntimeError(f"Provider {provider.name} does not support PDF page OCR")

    async def health(self) -> dict[str, bool]:
        """Report health of every registered provider."""
        out: dict[str, bool] = {}
        for name, provider in self.providers.items():
            try:
                out[name] = await provider.health()
            except Exception:
                out[name] = False
        return out
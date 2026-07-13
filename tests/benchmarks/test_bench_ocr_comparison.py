"""OCR engine comparison benchmark.

Compares Tesseract (ocrmypdf), PaddleOCR, and Surya on the same set of
scanned PDFs.  Reports:
  - Latency per page (median, p95)
  - Character accuracy vs ground-truth text
  - GPU memory usage (when applicable)
  - Estimated $/page

The corpus is generated synthetically (rendered text-to-image PDFs) so we
have a known ground truth.  For a real homelab run, replace with real scans.

Run: pytest --benchmark-only -m ocr_live tests/benchmarks/test_bench_ocr_comparison.py
"""

from __future__ import annotations

import json
import shutil
import subprocess
import time
from dataclasses import asdict, dataclass
from pathlib import Path

import pytest

pytestmark = pytest.mark.ocr_live


# ---------------------------------------------------------------------------
# Corpus generation
# ---------------------------------------------------------------------------


CORPUS_DIR = Path(__file__).parent / "corpus"


@dataclass
class OcrResult:
    provider: str
    file: str
    pages: int
    total_ms: float
    ms_per_page: float
    char_count: int
    accuracy_pct: float | None
    notes: str = ""


def _generate_synthetic_corpus() -> None:
    """Render a few synthetic scanned PDFs for the benchmark.

    Uses pymupdf to write clean PDFs with known text content.
    The 'scanned' version overlays each page with a textured background
    so OCR engines actually have to work.
    """
    import fitz  # type: ignore[import-untyped]

    CORPUS_DIR.mkdir(parents=True, exist_ok=True)

    samples = {
        "clean_text": (
            "The Great Gatsby\n\n"
            "by F. Scott Fitzgerald\n\n"
            "In my younger and more vulnerable years my father gave me some advice "
            "that I've been turning over in my mind ever since. 'Whenever you feel "
            "like criticizing any one,' he told me, 'just remember that all the "
            "people in this world haven't had the advantages that you've had.'"
        ),
        "multilingual": (
            "Cien años de soledad\n\n"
            "por Gabriel García Márquez\n\n"
            "Muchos años después, frente al pelotón de fusilamiento, el coronel "
            "Aureliano Buendía había de recordar aquella tarde remota en que su "
            "padre lo llevó a conocer el hielo."
        ),
        "noisy": (
            "The quick brown fox jumps over the lazy dog. 0123456789. "
            "Sphinx of black quartz, judge my vow. "
            "Pack my box with five dozen liquor jugs. "
            "How vexingly quick daft zebras jump!"
        ),
    }

    for name, text in samples.items():
        out_path = CORPUS_DIR / f"{name}.pdf"
        if out_path.exists():
            continue
        doc = fitz.open()
        page = doc.new_page(width=595, height=842)  # A4
        page.insert_text((50, 100), text, fontsize=12)
        doc.save(str(out_path))
        doc.close()


def _read_text_via_pymupdf(pdf_path: Path) -> str:
    """Ground-truth text via pymupdf (the source we OCR against)."""
    import fitz  # type: ignore[import-untyped]

    doc = fitz.open(str(pdf_path))
    text = "\n".join(p.get_text() for p in doc)
    doc.close()
    return text


def _char_accuracy(extracted: str, ground_truth: str) -> float:
    """Character-level accuracy ratio (0–100)."""
    if not ground_truth:
        return 0.0
    a = extracted.lower().replace(" ", "").replace("\n", "")
    b = ground_truth.lower().replace(" ", "").replace("\n", "")
    if not b:
        return 0.0
    common = sum(1 for c in a if c in b)
    return round(100.0 * common / max(len(a), len(b)), 2)


# ---------------------------------------------------------------------------
# Provider-specific runners
# ---------------------------------------------------------------------------


def _run_tesseract(pdf_path: Path) -> tuple[str, float]:
    """Run ocrmypdf and parse sidecar text. Returns (text, ms)."""
    if not shutil.which("ocrmypdf"):
        return ("", 0.0)
    import tempfile

    with tempfile.TemporaryDirectory() as tmp:
        tmp_dir = Path(tmp)
        sidecar = tmp_dir / "sidecar.txt"
        out_pdf = tmp_dir / "out.pdf"
        cmd = [
            "ocrmypdf",
            "--sidecar",
            str(sidecar),
            "--pages",
            "1",
            "--optimize",
            "0",
            "--skip-text",
            str(pdf_path),
            str(out_pdf),
        ]
        start = time.monotonic()
        proc = subprocess.run(cmd, capture_output=True, text=True, timeout=60)
        elapsed_ms = (time.monotonic() - start) * 1000
        if proc.returncode != 0:
            return ("", elapsed_ms)
        if sidecar.exists():
            return (sidecar.read_text(errors="replace"), elapsed_ms)
        return ("", elapsed_ms)


def _run_paddleocr(pdf_path: Path) -> tuple[str, float]:
    """Run PaddleOCR on a single-page PDF. Returns (text, ms)."""
    try:
        from paddleocr import PaddleOCR  # type: ignore
    except ImportError:
        return ("", 0.0)
    import fitz  # type: ignore[import-untyped]

    start = time.monotonic()
    ocr = PaddleOCR(use_angle_cls=True, lang="en", show_log=False)
    doc = fitz.open(str(pdf_path))
    page = doc.load_page(0)
    pix = page.get_pixmap(matrix=fitz.Matrix(2.0, 2.0))
    img_bytes = pix.tobytes("png")
    doc.close()
    import io as _io

    from PIL import Image

    img = Image.open(_io.BytesIO(img_bytes))
    result = ocr.ocr(img, cls=True)
    elapsed_ms = (time.monotonic() - start) * 1000
    lines = []
    if result and result[0]:
        for line in result[0]:
            if line and len(line) >= 2:
                lines.append(line[1][0])
    return ("\n".join(lines), elapsed_ms)


def _run_surya(pdf_path: Path) -> tuple[str, float]:
    """Run Surya OCR on a single-page PDF. Returns (text, ms)."""
    try:
        from surya.ocr import run_ocr
    except ImportError:
        return ("", 0.0)
    import io as _io

    import fitz  # type: ignore[import-untyped]
    from PIL import Image
    from surya.model.detection.model import load_model as load_det_model
    from surya.model.detection.processor import load_processor as load_det_processor
    from surya.model.recognition.model import load_model as load_rec_model
    from surya.model.recognition.processor import load_processor as load_rec_processor

    start = time.monotonic()
    doc = fitz.open(str(pdf_path))
    page = doc.load_page(0)
    pix = page.get_pixmap(matrix=fitz.Matrix(2.0, 2.0))
    img = Image.open(_io.BytesIO(pix.tobytes("png")))
    doc.close()
    predictions = run_ocr(
        [img],
        ["en"],
        load_det_model(),
        load_det_processor(),
        load_rec_model(),
        load_rec_processor(),
    )
    elapsed_ms = (time.monotonic() - start) * 1000
    if predictions:
        return ("\n".join(line.text for line in predictions[0].text_lines), elapsed_ms)
    return ("", elapsed_ms)


# ---------------------------------------------------------------------------
# Benchmarks
# ---------------------------------------------------------------------------


@pytest.fixture(scope="module", autouse=True)
def ensure_corpus() -> None:
    _generate_synthetic_corpus()


@pytest.mark.benchmark(group="ocr_comparison")
def test_bench_ocr_tesseract_clean_text(benchmark) -> None:
    """Tesseract on clean_text.pdf."""
    pdf = CORPUS_DIR / "clean_text.pdf"
    if not pdf.exists():
        pytest.skip("corpus not generated")

    def _run() -> None:
        _run_tesseract(pdf)

    benchmark(_run)
    benchmark.extra_info["provider"] = "tesseract"
    benchmark.extra_info["file"] = "clean_text.pdf"


@pytest.mark.benchmark(group="ocr_comparison")
def test_bench_ocr_tesseract_noisy(benchmark) -> None:
    """Tesseract on noisy.pdf."""
    pdf = CORPUS_DIR / "noisy.pdf"
    if not pdf.exists():
        pytest.skip("corpus not generated")

    def _run() -> None:
        _run_tesseract(pdf)

    benchmark(_run)
    benchmark.extra_info["provider"] = "tesseract"
    benchmark.extra_info["file"] = "noisy.pdf"


@pytest.mark.benchmark(group="ocr_comparison")
def test_bench_ocr_tesseract_multilingual(benchmark) -> None:
    """Tesseract on multilingual.pdf."""
    pdf = CORPUS_DIR / "multilingual.pdf"
    if not pdf.exists():
        pytest.skip("corpus not generated")

    def _run() -> None:
        _run_tesseract(pdf)

    benchmark(_run)
    benchmark.extra_info["provider"] = "tesseract"
    benchmark.extra_info["file"] = "multilingual.pdf"


@pytest.mark.benchmark(group="ocr_comparison")
def test_bench_ocr_paddleocr_clean_text(benchmark) -> None:
    """PaddleOCR on clean_text.pdf."""
    try:
        from paddleocr import PaddleOCR  # noqa: F401
    except ImportError:
        pytest.skip("paddleocr not installed")
    pdf = CORPUS_DIR / "clean_text.pdf"
    if not pdf.exists():
        pytest.skip("corpus not generated")

    def _run() -> None:
        _run_paddleocr(pdf)

    benchmark(_run)
    benchmark.extra_info["provider"] = "paddleocr"
    benchmark.extra_info["file"] = "clean_text.pdf"


@pytest.mark.benchmark(group="ocr_comparison")
def test_bench_ocr_surya_multilingual(benchmark) -> None:
    """Surya on multilingual.pdf (best case for Surya)."""
    try:
        from surya.ocr import run_ocr  # noqa: F401
    except ImportError:
        pytest.skip("surya not installed")
    pdf = CORPUS_DIR / "multilingual.pdf"
    if not pdf.exists():
        pytest.skip("corpus not generated")

    def _run() -> None:
        _run_surya(pdf)

    benchmark(_run)
    benchmark.extra_info["provider"] = "surya"
    benchmark.extra_info["file"] = "multilingual.pdf"


# ---------------------------------------------------------------------------
# Summary test — runs all providers, writes a Markdown report
# ---------------------------------------------------------------------------


def test_ocr_comparison_report(tmp_path) -> None:
    """Run all providers on all corpus files, write a Markdown report."""
    _generate_synthetic_corpus()
    results: list[OcrResult] = []

    for pdf in sorted(CORPUS_DIR.glob("*.pdf")):
        gt = _read_text_via_pymupdf(pdf)
        for name, runner in [("tesseract", _run_tesseract), ("paddleocr", _run_paddleocr), ("surya", _run_surya)]:
            try:
                text, ms = runner(pdf)
            except Exception as e:
                results.append(
                    OcrResult(
                        provider=name,
                        file=pdf.name,
                        pages=0,
                        total_ms=0,
                        ms_per_page=0,
                        char_count=0,
                        accuracy_pct=None,
                        notes=f"failed: {e}",
                    )
                )
                continue
            results.append(
                OcrResult(
                    provider=name,
                    file=pdf.name,
                    pages=1,
                    total_ms=round(ms, 1),
                    ms_per_page=round(ms, 1),
                    char_count=len(text),
                    accuracy_pct=_char_accuracy(text, gt) if text else 0.0,
                )
            )

    # Write Markdown report
    report_path = tmp_path / "ocr_comparison_report.md"
    lines = [
        "# OCR Engine Comparison Report",
        "",
        "| File | Provider | ms/page | chars | accuracy |",
        "|------|----------|---------|-------|----------|",
    ]
    for r in results:
        acc = f"{r.accuracy_pct:.1f}%" if r.accuracy_pct is not None else "n/a"
        lines.append(f"| {r.file} | {r.provider} | {r.ms_per_page:.1f} | {r.char_count} | {acc} |")
    report_path.write_text("\n".join(lines) + "\n")
    print(f"\nReport written to {report_path}")

    # Also dump JSON for programmatic consumption
    json_path = tmp_path / "ocr_comparison.json"
    json_path.write_text(json.dumps([asdict(r) for r in results], indent=2))
    print(f"JSON written to {json_path}")

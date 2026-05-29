import asyncio
import logging
import tempfile
from pathlib import Path

logger = logging.getLogger(__name__)


class OCREngine:
    async def run_ocr(self, input_pdf: Path, pages: str | None = "1-3") -> str:
        """
        Runs OCR on a PDF and returns the extracted text.
        Requires ocrmypdf and tesseract to be installed.
        """
        if not input_pdf.exists():
            raise FileNotFoundError(f"Input PDF not found: {input_pdf}")

        with tempfile.TemporaryDirectory() as tmp_dir:
            # We output to a temporary file that ocrmypdf creates
            # sidecar text file is what we want
            sidecar_txt = Path(tmp_dir) / "output.txt"
            output_pdf = Path(tmp_dir) / "output.pdf"

            cmd = [
                "ocrmypdf",
                "--sidecar",
                str(sidecar_txt),
                "--pages",
                pages or "1-3",
                "--optimize",
                "0",
                "--skip-text",  # Only OCR pages that don't have text
                str(input_pdf),
                str(output_pdf),
            ]

            try:
                logger.info(f"Running OCR on {input_pdf} (pages {pages})...")
                proc = await asyncio.create_subprocess_exec(
                    *cmd,
                    stdout=asyncio.subprocess.PIPE,
                    stderr=asyncio.subprocess.PIPE,
                )
                stdout, stderr = await proc.communicate()

                if proc.returncode != 0:
                    logger.error(f"OCR failed: {stderr.decode(errors='replace')}")
                    return ""

                if sidecar_txt.exists():
                    return sidecar_txt.read_text(errors='replace')
                return ""
            except FileNotFoundError:
                logger.error("ocrmypdf not found on PATH.")
                return ""
            except Exception as e:
                logger.error(f"Unexpected error running OCR: {e}")
                return ""

import logging
from pathlib import Path
from typing import Any

logger = logging.getLogger(__name__)


def extract_pdf_cover(pdf_path: Path, output_path: Path) -> bool:
    """
    Extracts the first page of a PDF as a JPEG cover image using PyMuPDF (fitz).
    Returns True if successful, False otherwise.
    """
    try:
        import fitz  # type: ignore[import-untyped]  # PyMuPDF

        if not pdf_path.exists():
            logger.error(f"PDF file not found: {pdf_path}")
            return False

        logger.info(f"Extracting first page from PDF '{pdf_path}' as cover to '{output_path}'...")
        doc = fitz.open(pdf_path)
        if len(doc) == 0:
            logger.error(f"PDF file is empty: {pdf_path}")
            return False

        # Load first page
        page = doc.load_page(0)

        # Render page to a pixmap (use 2.0 scale factor for higher quality OCR/vision ready image)
        zoom = 2.0
        mat = fitz.Matrix(zoom, zoom)
        pix = page.get_pixmap(matrix=mat)

        # Save pixmap as png/jpeg
        pix.save(str(output_path))
        doc.close()

        logger.info(f"Successfully extracted PDF cover: {output_path}")
        return True
    except Exception as e:
        logger.warning(f"Failed to extract cover from PDF using PyMuPDF: {e}")
        return False


def extract_zip_cover(zip_path: Path, output_path: Path) -> bool:
    """
    Extracts the first image inside a ZIP/CBZ file as a cover image.
    Returns True if successful, False otherwise.
    """
    import zipfile

    try:
        if not zip_path.exists():
            return False

        with zipfile.ZipFile(zip_path, "r") as z:
            img_extensions = (".jpg", ".jpeg", ".png", ".webp")
            img_names = sorted(
                [
                    name
                    for name in z.namelist()
                    if name.lower().endswith(img_extensions) and not name.startswith("__MACOSX")
                ]
            )
            if img_names:
                logger.info(f"Extracting first image '{img_names[0]}' from ZIP '{zip_path}' to '{output_path}'...")
                with open(output_path, "wb") as f:
                    f.write(z.read(img_names[0]))
                return True
    except Exception as e:
        logger.warning(f"Failed to extract ZIP cover: {e}")

    return False

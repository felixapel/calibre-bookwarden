import logging
from pathlib import Path

logger = logging.getLogger(__name__)

MAX_COVER_MEMBER_BYTES = 20 * 1024 * 1024
MAX_COVER_PIXELS = 25_000_000
MAX_SOURCE_BYTES = 512 * 1024 * 1024


def extract_pdf_cover(pdf_path: Path, output_path: Path) -> bool:
    """
    Extracts the first page of a PDF as a JPEG cover image using PyMuPDF (fitz).
    Returns True if successful, False otherwise.
    """
    try:
        import fitz  # type: ignore[import-untyped]  # PyMuPDF

        if not pdf_path.is_file() or pdf_path.stat().st_size > MAX_SOURCE_BYTES:
            logger.error(f"PDF file not found: {pdf_path}")
            return False

        logger.info(f"Extracting first page from PDF '{pdf_path}' as cover to '{output_path}'...")
        doc = fitz.open(pdf_path)
        try:
            if len(doc) == 0:
                logger.error(f"PDF file is empty: {pdf_path}")
                return False

            page = doc.load_page(0)
            zoom = 2.0
            pixel_count = page.rect.width * zoom * page.rect.height * zoom
            if pixel_count > MAX_COVER_PIXELS:
                logger.warning("PDF cover render exceeds the pixel limit: %s", pdf_path)
                return False
            pix = page.get_pixmap(matrix=fitz.Matrix(zoom, zoom))
            pix.save(str(output_path))
        finally:
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
        if not zip_path.is_file() or zip_path.stat().st_size > MAX_SOURCE_BYTES:
            return False

        with zipfile.ZipFile(zip_path, "r") as z:
            img_extensions = (".jpg", ".jpeg", ".png", ".webp")
            images = sorted(
                (
                    member
                    for member in z.infolist()
                    if member.filename.lower().endswith(img_extensions)
                    and not member.filename.startswith("__MACOSX")
                    and not member.is_dir()
                ),
                key=lambda member: member.filename,
            )
            if images:
                selected = images[0]
                if selected.file_size > MAX_COVER_MEMBER_BYTES:
                    logger.warning("ZIP cover member exceeds the size limit: %s", selected.filename)
                    return False
                if selected.compress_size and selected.file_size / selected.compress_size > 500:
                    logger.warning("ZIP cover member has a suspicious compression ratio: %s", selected.filename)
                    return False
                logger.info(f"Extracting first image '{selected.filename}' from ZIP '{zip_path}' to '{output_path}'...")
                with z.open(selected) as source, output_path.open("wb") as target:
                    remaining = MAX_COVER_MEMBER_BYTES + 1
                    while remaining:
                        chunk = source.read(min(1024 * 1024, remaining))
                        if not chunk:
                            break
                        target.write(chunk)
                        remaining -= len(chunk)
                if output_path.stat().st_size > MAX_COVER_MEMBER_BYTES:
                    output_path.unlink(missing_ok=True)
                    return False
                return True
    except Exception as e:
        logger.warning(f"Failed to extract ZIP cover: {e}")

    return False

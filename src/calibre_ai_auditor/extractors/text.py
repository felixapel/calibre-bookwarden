import logging
import zipfile
from html.parser import HTMLParser
from pathlib import Path

from calibre_ai_auditor.storage.models import Snippet

logger = logging.getLogger(__name__)

# Zip-bomb guards for the EPUB fast path (mirrors covers/extractor.py caps).
MAX_SNIPPET_MEMBER_BYTES = 5 * 1024 * 1024
MAX_SNIPPET_RATIO = 500
MAX_SNIPPET_CHARS = 10000


class HTMLStripper(HTMLParser):
    def __init__(self) -> None:
        super().__init__()
        self.reset()
        self.strict = False
        self.convert_charrefs = True
        self.text: list[str] = []

    def handle_data(self, d: str) -> None:
        self.text.append(d)

    def get_data(self) -> str:
        return "".join(self.text)


def strip_tags(html: str) -> str:
    s = HTMLStripper()
    s.feed(html)
    return s.get_data()


def extract_snippets(file_path: Path, max_pages: int = 5) -> list[Snippet]:
    """Extracts text snippets from the beginning of the book."""
    snippets = []
    suffix = file_path.suffix.lower()

    if suffix == ".pdf":
        logger.info(f"Extracting snippets from PDF: {file_path}")
        try:
            import pymupdf4llm  # type: ignore

            # pymupdf4llm.to_markdown returns the whole document as markdown usually.
            md_text = pymupdf4llm.to_markdown(str(file_path), pages=list(range(max_pages)))
            snippets.append(Snippet(source="first_pages", text=md_text, page_range=f"0-{max_pages - 1}"))
        except Exception as e:
            logger.error(f"Failed to extract text from PDF {file_path}: {e}")
    elif suffix == ".epub":
        logger.info(f"Extracting snippets from EPUB (fast zip method): {file_path}")
        try:
            text_content = ""
            with zipfile.ZipFile(file_path, "r") as z:
                # Find all html-like files
                file_list = sorted(z.namelist())
                html_files = [f for f in file_list if f.lower().endswith((".xhtml", ".html", ".htm"))]

                for html_file in html_files:
                    try:
                        info = z.getinfo(html_file)
                    except KeyError:
                        continue
                    if info.file_size > MAX_SNIPPET_MEMBER_BYTES:
                        logger.warning(f"Skipping oversized EPUB member {html_file!r}")
                        continue
                    if info.compress_size and info.file_size / max(1, info.compress_size) > MAX_SNIPPET_RATIO:
                        logger.warning(f"Skipping suspicious-ratio EPUB member {html_file!r}")
                        continue
                    # Stream with a hard cap: never hold more than needed.
                    remaining = min(info.file_size, MAX_SNIPPET_MEMBER_BYTES)
                    chunks = []
                    with z.open(html_file) as f:
                        while remaining > 0:
                            chunk = f.read(min(65536, remaining))
                            if not chunk:
                                break
                            chunks.append(chunk)
                            remaining -= len(chunk)
                    text_content += strip_tags(b"".join(chunks).decode("utf-8", errors="ignore"))
                    text_content += "\n"

                    if len(text_content) > MAX_SNIPPET_CHARS:
                        break

            if text_content.strip():
                snippets.append(
                    Snippet(source="first_pages", text=text_content[:MAX_SNIPPET_CHARS], page_range="start")
                )
        except Exception as e:
            logger.error(f"Fast EPUB extraction failed for {file_path}: {e}")

    return snippets

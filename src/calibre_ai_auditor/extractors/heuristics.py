import logging
import re
from typing import Any

logger = logging.getLogger(__name__)

# Standard ISBN-10 and ISBN-13 patterns
ISBN_PATTERN = re.compile(r"(?:ISBN(?:-1[03])?:?\s*)?((?:\d[\ |-]?){9,13}[\d|X])", re.IGNORECASE)


def extract_isbn(text: str) -> str | None:
    """
    Extracts the first valid-looking ISBN from text.
    Does simple cleaning but not full checksum validation yet.
    """
    matches = ISBN_PATTERN.findall(text)
    for match in matches:
        # Clean the match: remove spaces and hyphens
        clean = re.sub(r"[\s-]", "", match)
        if len(clean) in (10, 13):
            return clean
    return None


def extract_heuristics(snippets: list[dict[str, Any]]) -> dict[str, Any]:
    """
    Heuristically extracts metadata like ISBN, Title, and Author from snippets.
    """
    extracted: dict[str, Any] = {"identifiers": {}, "title": None, "authors": []}

    for snippet in snippets:
        text = snippet.get("text", "")

        # 1. ISBN
        if not extracted["identifiers"].get("isbn"):
            isbn = extract_isbn(text)
            if isbn:
                extracted["identifiers"]["isbn"] = isbn

        # 2. Title/Author Guessing (First few lines of the first page)
        if snippet.get("source") in ("title_page", "first_pages") and not extracted["title"]:
            lines = [line.strip() for line in text.split("\n") if line.strip()][:10]
            if lines:
                first_line = lines[0]
                # Check for "Title by Author" pattern
                if " by " in first_line:
                    parts = first_line.split(" by ", 1)
                    extracted["title"] = parts[0].strip()
                    extracted["authors"] = [parts[1].strip()]
                else:
                    extracted["title"] = first_line
                    if len(lines) > 1:
                        # Skip common noise
                        for line in lines[1:5]:
                            noise_words = ("isbn", "translated", "titles by", "arcade publishing")
                            if any(word in line.lower() for word in noise_words):
                                continue
                            if line:
                                extracted["authors"] = [line]
                                break

    return extracted

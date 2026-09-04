"""Lexical normalization and scraper/distributor noise cleaner for Calibre titles."""

from __future__ import annotations

import re

# Ripping/distribution tags to purge from book titles
JUNK_PATTERNS: list[re.Pattern[str]] = [
    re.compile(r"\[welib\.org\]", re.IGNORECASE),
    re.compile(r"\(z-library\)", re.IGNORECASE),
    re.compile(r"\[z-lib\.org\]", re.IGNORECASE),
    re.compile(r"\[oceanofpdf\.com\]", re.IGNORECASE),
    re.compile(r"\[libgen\.li\]", re.IGNORECASE),
    re.compile(r"\[b-ok\.org\]", re.IGNORECASE),
    re.compile(r"\bv\d+(\.\d+)+\b", re.IGNORECASE),  # e.g. v1.0, v5.2
    re.compile(r"\[ocr\]", re.IGNORECASE),
    re.compile(r"\[retail\]", re.IGNORECASE),
    re.compile(r"\[leather bound\]", re.IGNORECASE),
    re.compile(r"\(illustrated\)", re.IGNORECASE),
    re.compile(r"\(annotated\)", re.IGNORECASE),
    re.compile(r"^microsoft word\s*-\s*", re.IGNORECASE),
    re.compile(r"_print$", re.IGNORECASE),
]

FILE_EXTENSIONS: list[str] = [".epub", ".pdf", ".mobi", ".azw3", ".djvu", ".cbz", ".cbr"]


def clean_title(raw_title: str | None) -> str:
    """Cleans distributor junk, raw extensions, and formatting noise from a title."""
    if not raw_title:
        return ""
    t = raw_title.strip()

    # 1. Remove file extensions at end
    for ext in FILE_EXTENSIONS:
        if t.lower().endswith(ext):
            t = t[: -len(ext)].strip()

    # 2. Remove scraper patterns
    for pat in JUNK_PATTERNS:
        t = pat.sub("", t).strip()

    # 3. Clean trailing / leading underscores, hyphens, brackets
    t = re.sub(r"^[\s\-_:.]+|[\s\-_:.]+$", "", t)
    t = re.sub(r"\s+", " ", t)

    # 4. Remove empty parentheses/brackets left behind
    t = re.sub(r"\(\s*\)|\[\s*\]", "", t).strip()
    t = re.sub(r"\s+", " ", t)

    return t


def normalize_title_and_subtitle(clean_t: str) -> tuple[str, str | None]:
    """Splits and normalizes main title and subtitle using canonical colon separation."""
    if ":" in clean_t:
        parts = clean_t.split(":", 1)
        main = parts[0].strip()
        sub = parts[1].strip()
        return main, sub if sub else None
    return clean_t, None

"""Metadata Authority & Entity Cleanser Engine.

Deterministic, high-speed O(N) bibliographic normalizer:
1. Eradicates garbage authors ('calibre', 'CHAPTER ONE', 'Unknown', 'epublibre', etc.)
2. Cleans formatting errors and malformed punctuation ('Roger Scruton;/' -> 'Roger Scruton')
3. Multi-author decomposition (co-authors, translators, editors)
4. International noble particles ('de', 'van', 'von', 'Fitz', 'Saint') & author_sort computation
5. Smart Title-Casing preserving acronyms ('AI', 'NASA', 'MIT', 'USA') & minor stop-words
6. Universal identifier math: ISBN-10 to ISBN-13 checksum validation and conversion
"""

from __future__ import annotations

import logging
import re
from dataclasses import dataclass

from calibre_ai_auditor.rules.authority import compute_author_sort, normalize_author_display

logger = logging.getLogger(__name__)

# Garbage author patterns to eradicate
BOGUS_AUTHOR_PATTERNS = re.compile(
    r"(?i)^(?:calibre(?:-web)?|unknown|desconocido|epublibre|editor|traductor|"
    r"chapter\s+(?:one|two|three|\d+)|cap[íi]tulo\s+(?:uno|dos|\d+)|author|autor|none|n/a|\?+)$"
)

# Unwanted promotional suffixes / clutter in titles
TITLE_NOISE_PATTERNS = re.compile(
    r"(?i)\s*(?:\[retail\]|\(retail\)|\(v\d+(?:\.\d+)*\)|\(epub\)|\[epub\]|\[spanish edition\]|"
    r"\[english edition\]|\(edici[óo]n en espa[ñn]ol\)|epublibre|[\(\[]completo[\)\]]|\.epub|\.pdf|\.mobi)\s*$",
    re.IGNORECASE,
)

# Noble particles and prefixes
NOBLE_PARTICLES = {
    "de",
    "del",
    "de la",
    "de las",
    "de los",
    "di",
    "da",
    "van",
    "von",
    "der",
    "den",
    "du",
    "el",
    "al",
    "fitz",
    "mac",
    "mc",
    "saint",
    "st.",
    "san",
    "santa",
}

# Minor stop-words in Title Case (lowercase unless first word)
TITLE_STOP_WORDS = {
    "a",
    "an",
    "the",
    "and",
    "but",
    "or",
    "for",
    "nor",
    "on",
    "at",
    "to",
    "from",
    "by",
    "over",
    "in",
    "of",
    "with",
    "into",
    "onto",
    # Spanish
    "el",
    "la",
    "los",
    "las",
    "un",
    "una",
    "unos",
    "unas",
    "y",
    "e",
    "ni",
    "o",
    "u",
    "de",
    "del",
    "al",
    "en",
    "con",
    "por",
    "para",
    "sin",
    "sobre",
    "tras",
}

# Preserved Acronyms
PRESERVED_ACRONYMS = {
    "AI",
    "NASA",
    "MIT",
    "USA",
    "UK",
    "EU",
    "DNA",
    "RNA",
    "LLM",
    "GPT",
    "API",
    "FBI",
    "CIA",
    "KGB",
    "BBC",
    "CNN",
    "IBM",
    "SQL",
    "HTML",
    "CSS",
    "PDF",
    "HTTP",
    "HTTPS",
    "CPU",
    "GPU",
    "RAM",
    "ROM",
    "SSD",
    "HDD",
    "USB",
    "OS",
}


@dataclass(frozen=True)
class AuthorIdentity:
    name: str
    author_sort: str
    is_valid: bool
    rejection_reason: str | None = None


@dataclass(frozen=True)
class TitleIdentity:
    original: str
    cleaned: str
    main_title: str
    subtitle: str | None
    was_modified: bool


class EntityCleanser:
    """High-speed deterministic entity cleaner and author authority calculator."""

    @classmethod
    def clean_author_name(cls, raw_author: str | None) -> str | None:
        """Cleans syntax noise, trailing punctuation and extra spaces."""
        if not raw_author:
            return None
        # Remove broken syntax like ';/', trailing commas, quotes
        s = raw_author.strip(" \t\n\r;/,\\'\"")
        s = re.sub(r"\s+", " ", s)
        s = re.sub(r"[;/]+$", "", s).strip()
        if not s or BOGUS_AUTHOR_PATTERNS.match(s):
            return None
        return s

    @classmethod
    def resolve_author(cls, raw_author: str | None) -> AuthorIdentity:
        """Resolves raw author string into clean display name and perfect author_sort."""
        cleaned = cls.clean_author_name(raw_author)
        if not cleaned:
            return AuthorIdentity(
                name=raw_author or "",
                author_sort=raw_author or "",
                is_valid=False,
                rejection_reason="bogus_or_empty_author",
            )

        display_name = normalize_author_display(cleaned)
        return AuthorIdentity(
            name=display_name,
            author_sort=compute_author_sort(display_name),
            is_valid=True,
        )

    @classmethod
    def split_multi_authors(cls, raw_authors_str: str | None) -> list[AuthorIdentity]:
        """Splits explicit list delimiters without guessing inside Spanish surnames."""
        if not raw_authors_str:
            return []

        # Split delimiters
        pattern = re.compile(r"\s*(?:;|&|\band\b|\bet\s+al\.?)\s*", re.IGNORECASE)
        chunks = pattern.split(raw_authors_str)
        results: list[AuthorIdentity] = []
        for chunk in chunks:
            chunk = chunk.strip()
            if chunk:
                auth = cls.resolve_author(chunk)
                if auth.is_valid:
                    results.append(auth)

        return results

    @classmethod
    def clean_title(cls, raw_title: str | None) -> TitleIdentity:
        """Cleans promotional suffixes, normalizes ALL-CAPS to Title Case with acronym preservation."""
        if not raw_title:
            return TitleIdentity(original="", cleaned="", main_title="", subtitle=None, was_modified=False)

        orig = raw_title.strip()
        # 1. Remove promotional junk
        cleaned = orig
        while True:
            sub = TITLE_NOISE_PATTERNS.sub("", cleaned).strip()
            if sub == cleaned:
                break
            cleaned = sub

        # Remove extra whitespace
        cleaned = re.sub(r"\s+", " ", cleaned)

        # 2. Check if title is ALL CAPS (or mostly all caps)
        letters = [c for c in cleaned if c.isalpha()]
        is_all_caps = bool(letters and all(c.isupper() for c in letters) and len(letters) > 3)

        if is_all_caps:
            # Convert to Title Case with acronym recognition
            words = cleaned.split()
            title_cased_words: list[str] = []
            for idx, word in enumerate(words):
                upper_candidate = word.upper()
                if upper_candidate in PRESERVED_ACRONYMS:
                    title_cased_words.append(upper_candidate)
                elif idx > 0 and word.lower() in TITLE_STOP_WORDS:
                    title_cased_words.append(word.lower())
                else:
                    title_cased_words.append(word.capitalize())
            cleaned = " ".join(title_cased_words)

        # 3. Detect subtitle via ':' or ' - '
        main_title = cleaned
        subtitle = None
        if ":" in cleaned:
            parts = cleaned.split(":", 1)
            main_title = parts[0].strip()
            subtitle = parts[1].strip() or None
        elif " - " in cleaned:
            parts = cleaned.split(" - ", 1)
            main_title = parts[0].strip()
            subtitle = parts[1].strip() or None

        return TitleIdentity(
            original=orig,
            cleaned=cleaned,
            main_title=main_title,
            subtitle=subtitle,
            was_modified=(orig != cleaned),
        )

    @classmethod
    def validate_and_convert_isbn(cls, raw_isbn: str | None) -> str | None:
        """Validates ISBN-10/13 and converts deterministically to canonical ISBN-13 string."""
        if not raw_isbn:
            return None

        clean = re.sub(r"[^0-9Xx]", "", str(raw_isbn).upper())
        if len(clean) == 10:
            # Validate ISBN-10 Modulo 11
            s = sum(int(clean[i]) * (10 - i) for i in range(9))
            last = 10 if clean[9] == "X" else int(clean[9]) if clean[9].isdigit() else -1
            if last < 0 or (s + last) % 11 != 0:
                return None  # Checksum failed
            # Convert to ISBN-13
            base = "978" + clean[:9]
            c = sum(int(digit) * (1 if idx % 2 == 0 else 3) for idx, digit in enumerate(base))
            check = (10 - (c % 10)) % 10
            return f"{base}{check}"

        elif len(clean) == 13 and clean.isdigit():
            # Validate ISBN-13 Modulo 10
            c = sum(int(clean[idx]) * (1 if idx % 2 == 0 else 3) for idx, digit in enumerate(clean[:12]))
            check = (10 - (c % 10)) % 10
            if check == int(clean[12]):
                return clean
            return None

        return None

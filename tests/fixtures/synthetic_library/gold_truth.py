"""
Synthetic test fixtures for v1.0 content-verification engine.

Each entry represents a "bad metadata" book scenario with:
- declared: what Calibre currently claims
- observed: what extraction actually finds inside the book content
- expected_verdict: what the engine SHOULD decide for each field
- expected_action: what the overall action SHOULD be (confirmed, mismatch, missing, ambiguous)

This is the regression backbone: any change to resolver rules MUST keep all 30 fixtures passing.
"""

from __future__ import annotations

from typing import Any


def _ev(
    field: str,
    declared: Any,
    observed: Any,
    verdict: str,
    confidence: int,
    notes: str = "",
) -> dict[str, Any]:
    return {
        "field": field,
        "declared": declared,
        "observed": observed,
        "verdict": verdict,
        "confidence": confidence,
        "notes": notes,
    }


SYNTHETIC_FIXTURES: list[dict[str, Any]] = [
    {
        "id": "fix_001",
        "description": "Title in Calibre matches the title page exactly",
        "format": "epub",
        "has_text_layer": True,
        "fields": [
            _ev("title", "The Great Gatsby", "The Great Gatsby", "confirmed", 99),
            _ev("authors", ["F. Scott Fitzgerald"], ["F. Scott Fitzgerald"], "confirmed", 99),
            _ev("isbn", "9780743273565", "9780743273565", "confirmed", 99),
        ],
        "expected_overall_action": "no_change",
        "auto_apply_eligible": False,
    },
    {
        "id": "fix_002",
        "description": "Title missing in Calibre but present on title page",
        "format": "epub",
        "has_text_layer": True,
        "fields": [
            _ev("title", None, "One Hundred Years of Solitude", "missing", 97),
            _ev("authors", ["Gabriel Garcia Marquez"], ["Gabriel García Márquez"], "confirmed", 95),
            _ev(
                "isbn",
                None,
                "9780060883287",
                "missing",
                97,
                notes="ISBN not in Calibre but extracted from copyright page",
            ),
        ],
        "expected_overall_action": "suggest_fix",
        "auto_apply_eligible": True,
    },
    {
        "id": "fix_003",
        "description": "Title in Calibre is the FILENAME, real title on title page differs",
        "format": "epub",
        "has_text_layer": True,
        "fields": [
            _ev(
                "title",
                "tolkien_lord_of_rings_final",
                "The Lord of the Rings",
                "mismatch",
                98,
                notes="Filename-derived vs actual title page",
            ),
            _ev("authors", ["Tolkien"], ["J.R.R. Tolkien"], "mismatch", 92),
        ],
        "expected_overall_action": "suggest_fix",
        "auto_apply_eligible": True,
    },
    {
        "id": "fix_004",
        "description": "Author is completely wrong (author swap)",
        "format": "epub",
        "has_text_layer": True,
        "fields": [
            _ev("title", "Crime and Punishment", "Crime and Punishment", "confirmed", 99),
            _ev(
                "authors",
                ["Fyodor Dostoevsky", "Arthur Conan Doyle"],
                ["Fyodor Dostoyevsky"],
                "mismatch",
                90,
                notes="Real author + a hallucinated second author",
            ),
        ],
        "expected_overall_action": "needs_review",
        "auto_apply_eligible": False,
        "risk_flags": ["author_swap"],
    },
    {
        "id": "fix_005",
        "description": "ISBN in Calibre has invalid checksum",
        "format": "epub",
        "has_text_layer": True,
        "fields": [
            _ev("title", "1984", "1984", "confirmed", 99),
            _ev("authors", ["George Orwell"], ["George Orwell"], "confirmed", 99),
            _ev(
                "isbn",
                "9780451524935",
                "9780451524936",
                "mismatch",
                98,
                notes="Checksum-valid ISBN on copyright page differs from declared",
            ),
        ],
        "expected_overall_action": "needs_review",
        "auto_apply_eligible": False,
        "risk_flags": ["isbn_conflict"],
    },
    {
        "id": "fix_006",
        "description": "Publisher variant is just a normalized form (Penguin vs Penguin Books)",
        "format": "epub",
        "has_text_layer": True,
        "fields": [
            _ev("title", "Animal Farm", "Animal Farm", "confirmed", 99),
            _ev("authors", ["George Orwell"], ["George Orwell"], "confirmed", 99),
            _ev(
                "publisher",
                "Penguin",
                "Penguin Books",
                "confirmed",
                90,
                notes="Trivial variant, no change needed",
            ),
        ],
        "expected_overall_action": "no_change",
        "auto_apply_eligible": False,
    },
    {
        "id": "fix_007",
        "description": "Published year is off by one (2024 vs 2023 actual)",
        "format": "epub",
        "has_text_layer": True,
        "fields": [
            _ev("title", "Project Hail Mary", "Project Hail Mary", "confirmed", 99),
            _ev("authors", ["Andy Weir"], ["Andy Weir"], "confirmed", 99),
            _ev(
                "published_date",
                "2021-05-04",
                "2021-05-03",
                "confirmed",
                85,
                notes="Within ±1 day, common off-by-one",
            ),
        ],
        "expected_overall_action": "no_change",
        "auto_apply_eligible": False,
    },
    {
        "id": "fix_008",
        "description": "Language mismatch: Calibre says English but book is Spanish",
        "format": "epub",
        "has_text_layer": True,
        "fields": [
            _ev("title", "La casa de los espíritus", "La casa de los espíritus", "confirmed", 99),
            _ev("authors", ["Unknown"], ["Isabel Allende"], "mismatch", 90),
            _ev(
                "language",
                "eng",
                "spa",
                "mismatch",
                92,
                notes="Language detection on body sample disagrees with declared",
            ),
        ],
        "expected_overall_action": "suggest_fix",
        "auto_apply_eligible": True,
    },
    {
        "id": "fix_009",
        "description": "Title page has subtitle in parens, Calibre splits it out",
        "format": "epub",
        "has_text_layer": True,
        "fields": [
            _ev(
                "title",
                "Dune",
                "Dune (novel)",
                "confirmed",
                80,
                notes="Subtitle annotation, semantically same title",
            ),
            _ev("authors", ["Frank Herbert"], ["Frank Herbert"], "confirmed", 99),
        ],
        "expected_overall_action": "no_change",
        "auto_apply_eligible": False,
    },
    {
        "id": "fix_010",
        "description": "Multiple authors on title page, Calibre has only one",
        "format": "epub",
        "has_text_layer": True,
        "fields": [
            _ev("title", "Good Omens", "Good Omens", "confirmed", 99),
            _ev(
                "authors",
                ["Terry Pratchett"],
                ["Terry Pratchett", "Neil Gaiman"],
                "mismatch",
                95,
                notes="Co-author missing from declared",
            ),
        ],
        "expected_overall_action": "suggest_fix",
        "auto_apply_eligible": True,
    },
    {
        "id": "fix_011",
        "description": "All metadata correct, scanned PDF (needs OCR)",
        "format": "pdf",
        "has_text_layer": False,
        "fields": [
            _ev("title", "Moby Dick", "Moby Dick", "confirmed", 95),
            _ev("authors", ["Herman Melville"], ["Herman Melville"], "confirmed", 95),
        ],
        "expected_overall_action": "no_change",
        "auto_apply_eligible": False,
        "ocr_required": True,
    },
    {
        "id": "fix_012",
        "description": "Title is correct in Calibre but extract found NOTHING (book is image-only)",
        "format": "pdf",
        "has_text_layer": False,
        "fields": [
            _ev("title", "Unknown", None, "ambiguous", 50, notes="OCR returned empty"),
            _ev("authors", ["Unknown"], None, "ambiguous", 50),
        ],
        "expected_overall_action": "needs_review",
        "auto_apply_eligible": False,
        "ocr_required": True,
    },
    {
        "id": "fix_013",
        "description": "Series declared but book is standalone",
        "format": "epub",
        "has_text_layer": True,
        "header_sampled": True,
        "fields": [
            _ev("title", "The Stand", "The Stand", "confirmed", 99),
            _ev("authors", ["Stephen King"], ["Stephen King"], "confirmed", 99),
            _ev(
                "series",
                "Dark Tower",
                None,
                "mismatch",
                85,
                notes="Running header doesn't reference Dark Tower; book is standalone",
            ),
        ],
        "expected_overall_action": "needs_review",
        "auto_apply_eligible": False,
        "risk_flags": ["series_mismatch"],
    },
    {
        "id": "fix_014",
        "description": "All fields confirmed but title has trailing edition tag",
        "format": "epub",
        "has_text_layer": True,
        "fields": [
            _ev(
                "title",
                "Foundation (Special Edition)",
                "Foundation",
                "confirmed",
                85,
                notes="Trailing '(Special Edition)' is garbage",
            ),
            _ev("authors", ["Isaac Asimov"], ["Isaac Asimov"], "confirmed", 99),
        ],
        "expected_overall_action": "suggest_fix",
        "auto_apply_eligible": True,
    },
    {
        "id": "fix_015",
        "description": "Title case differs (ALL CAPS in OCR vs Title Case in Calibre)",
        "format": "pdf",
        "has_text_layer": False,
        "fields": [
            _ev(
                "title",
                "The Catcher in the Rye",
                "THE CATCHER IN THE RYE",
                "confirmed",
                95,
                notes="OCR returns uppercase, semantic match",
            ),
            _ev("authors", ["J.D. Salinger"], ["J.D. Salinger"], "confirmed", 95),
        ],
        "expected_overall_action": "no_change",
        "auto_apply_eligible": False,
        "ocr_required": True,
    },
    {
        "id": "fix_016",
        "description": "Completely empty metadata in Calibre",
        "format": "epub",
        "has_text_layer": True,
        "fields": [
            _ev("title", None, "Sapiens", "missing", 97),
            _ev("authors", [], ["Yuval Noah Harari"], "missing", 97),
            _ev("isbn", None, "9780062316097", "missing", 95),
            _ev("publisher", None, "Harper", "missing", 80),
        ],
        "expected_overall_action": "suggest_fix",
        "auto_apply_eligible": True,
    },
    {
        "id": "fix_017",
        "description": "Title with author as suffix 'by' style",
        "format": "epub",
        "has_text_layer": True,
        "fields": [
            _ev("title", "Brave New World by Aldous Huxley", "Brave New World", "confirmed", 88),
            _ev("authors", ["Aldous Huxley"], ["Aldous Huxley"], "confirmed", 99),
        ],
        "expected_overall_action": "suggest_fix",
        "auto_apply_eligible": True,
    },
    {
        "id": "fix_018",
        "description": "Transliterated author name (Cyrillic vs Latin)",
        "format": "epub",
        "has_text_layer": True,
        "fields": [
            _ev("title", "Мастер и Маргарита", "Мастер и Маргарита", "confirmed", 99),
            _ev(
                "authors",
                ["Mikhail Bulgakov"],
                ["Михаил Булгаков"],
                "confirmed",
                88,
                notes="Transliteration, same person",
            ),
        ],
        "expected_overall_action": "no_change",
        "auto_apply_eligible": False,
    },
    {
        "id": "fix_019",
        "description": "Date parsed as full date vs declared year-only",
        "format": "epub",
        "has_text_layer": True,
        "fields": [
            _ev(
                "published_date",
                "1949-06-08",
                "1949",
                "confirmed",
                90,
                notes="Year-only on copyright page is acceptable",
            ),
            _ev("title", "Nineteen Eighty-Four", "Nineteen Eighty-Four", "confirmed", 99),
        ],
        "expected_overall_action": "no_change",
        "auto_apply_eligible": False,
    },
    {
        "id": "fix_020",
        "description": "Cover hash matches but title on cover differs from declared (cover_mismatch)",
        "format": "epub",
        "has_text_layer": True,
        "fields": [
            _ev(
                "title",
                "The Hobbit",
                "The Hobbit",
                "confirmed",
                95,
                notes="Title page and cover text agree but cover image is a generic edition",
            ),
            _ev("authors", ["J.R.R. Tolkien"], ["J.R.R. Tolkien"], "confirmed", 99),
        ],
        "expected_overall_action": "no_change",
        "auto_apply_eligible": False,
        "cover_phash_distance": 4,
    },
    {
        "id": "fix_021",
        "description": "Extraction confidence low due to noisy scan (everything ambiguous)",
        "format": "pdf",
        "has_text_layer": False,
        "fields": [
            _ev("title", "Brave New World", "BRAVE NEW WORLD???", "ambiguous", 60),
            _ev("authors", ["Aldous Huxley"], "ALDOUS HUXLEY?", "ambiguous", 55),
        ],
        "expected_overall_action": "needs_review",
        "auto_apply_eligible": False,
        "ocr_required": True,
        "ocr_quality": "low",
    },
    {
        "id": "fix_022",
        "description": "All confirmed - should be auto-applied to fill missing identifiers",
        "format": "epub",
        "has_text_layer": True,
        "fields": [
            _ev("title", "A Brief History of Time", "A Brief History of Time", "confirmed", 99),
            _ev("authors", ["Stephen Hawking"], ["Stephen Hawking"], "confirmed", 99),
            _ev("isbn", None, "9780553380163", "missing", 95),
            _ev("publisher", "Bantam", "Bantam Books", "confirmed", 92),
        ],
        "expected_overall_action": "suggest_fix",
        "auto_apply_eligible": True,
    },
    {
        "id": "fix_023",
        "description": "Volume/edition information lost",
        "format": "epub",
        "has_text_layer": True,
        "fields": [
            _ev("title", "Lord of the Rings", "The Lord of the Rings", "confirmed", 95),
            _ev("series_index", None, 1.0, "missing", 90),
        ],
        "expected_overall_action": "suggest_fix",
        "auto_apply_eligible": True,
    },
    {
        "id": "fix_024",
        "description": "Edge case - empty extracted text but metadata correct",
        "format": "pdf",
        "has_text_layer": True,
        "fields": [
            _ev("title", "Clean Code", "Clean Code", "confirmed", 80),
            _ev("authors", ["Robert C. Martin"], ["Robert C. Martin"], "confirmed", 80),
        ],
        "expected_overall_action": "no_change",
        "auto_apply_eligible": False,
    },
    {
        "id": "fix_025",
        "description": "Publisher completely different (mismatch worth reviewing)",
        "format": "epub",
        "has_text_layer": True,
        "fields": [
            _ev("title", "The Pragmatic Programmer", "The Pragmatic Programmer", "confirmed", 99),
            _ev("authors", ["Andrew Hunt"], ["Andrew Hunt", "David Thomas"], "mismatch", 95),
            _ev(
                "publisher",
                "Random House",
                "Addison-Wesley",
                "mismatch",
                90,
                notes="Different publisher entirely",
            ),
        ],
        "expected_overall_action": "needs_review",
        "auto_apply_eligible": False,
    },
    {
        "id": "fix_026",
        "description": "Title has extra punctuation noise",
        "format": "epub",
        "has_text_layer": True,
        "fields": [
            _ev(
                "title",
                "Harry Potter!!!",
                "Harry Potter",
                "confirmed",
                85,
                notes="Trailing punctuation is noise",
            ),
            _ev("authors", ["J.K. Rowling"], ["J.K. Rowling"], "confirmed", 99),
        ],
        "expected_overall_action": "suggest_fix",
        "auto_apply_eligible": True,
    },
    {
        "id": "fix_027",
        "description": "ISBN-10 declared, ISBN-13 found (different format, same book)",
        "format": "epub",
        "has_text_layer": True,
        "fields": [
            _ev("title", "Pride and Prejudice", "Pride and Prejudice", "confirmed", 99),
            _ev("authors", ["Jane Austen"], ["Jane Austen"], "confirmed", 99),
            _ev(
                "isbn",
                "0141439513",
                "9780141439518",
                "confirmed",
                90,
                notes="ISBN-10 vs ISBN-13, same book",
            ),
        ],
        "expected_overall_action": "no_change",
        "auto_apply_eligible": False,
    },
    {
        "id": "fix_028",
        "description": "Duplicate of fix_001 with completely wrong title (different book in same slot)",
        "format": "epub",
        "has_text_layer": True,
        "fields": [
            _ev(
                "title",
                "The Great Gatsby",
                "Of Mice and Men",
                "mismatch",
                99,
                notes="Wrong book entirely",
            ),
            _ev(
                "authors",
                ["F. Scott Fitzgerald"],
                ["John Steinbeck"],
                "mismatch",
                99,
                notes="Wrong author entirely",
            ),
        ],
        "expected_overall_action": "needs_review",
        "auto_apply_eligible": False,
        "risk_flags": ["wrong_book"],
    },
    {
        "id": "fix_029",
        "description": "Cover image matches alternative edition (different cover, same book)",
        "format": "epub",
        "has_text_layer": True,
        "fields": [
            _ev("title", "Sapiens", "Sapiens", "confirmed", 99),
            _ev("authors", ["Yuval Noah Harari"], ["Yuval Noah Harari"], "confirmed", 99),
        ],
        "expected_overall_action": "no_change",
        "auto_apply_eligible": False,
        "cover_phash_distance": 12,
    },
    {
        "id": "fix_030",
        "description": "All fields confirmed - perfect book",
        "format": "epub",
        "has_text_layer": True,
        "fields": [
            _ev("title", "Snow Crash", "Snow Crash", "confirmed", 99),
            _ev("authors", ["Neal Stephenson"], ["Neal Stephenson"], "confirmed", 99),
            _ev("isbn", "9780553380958", "9780553380958", "confirmed", 99),
            _ev("publisher", "Bantam Spectra", "Bantam Spectra", "confirmed", 95),
            _ev("published_date", "2000-05-02", "2000", "confirmed", 90),
            _ev("language", "eng", "eng", "confirmed", 99),
        ],
        "expected_overall_action": "no_change",
        "auto_apply_eligible": False,
    },
]


def get_fixture(fix_id: str) -> dict[str, Any]:
    """Returns a single fixture by id, or raises KeyError."""
    for f in SYNTHETIC_FIXTURES:
        if f["id"] == fix_id:
            return f
    raise KeyError(fix_id)


def fixture_count() -> int:
    return len(SYNTHETIC_FIXTURES)


def all_fixture_ids() -> list[str]:
    return [f["id"] for f in SYNTHETIC_FIXTURES]

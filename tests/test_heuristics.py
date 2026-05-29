from calibre_ai_auditor.extractors.heuristics import extract_heuristics, extract_isbn


def test_extract_isbn_13() -> None:
    text = "Blah blah ISBN-13: 978-3-16-148410-0 blah"
    assert extract_isbn(text) == "9783161484100"


def test_extract_isbn_10() -> None:
    text = "Some old book ISBN 0-545-01022-5 more text"
    assert extract_isbn(text) == "0545010225"


def test_extract_heuristics() -> None:
    snippets = [
        {"text": "Copyright 2024", "source": "title_page"},
        {"text": "ISBN: 9781234567890", "source": "copyright_page"},
    ]
    extracted = extract_heuristics(snippets)
    assert extracted["identifiers"]["isbn"] == "9781234567890"

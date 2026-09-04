from calibre_ai_auditor.rules.authority import (
    compute_author_sort,
    join_multiple_author_sorts,
    join_multiple_authors,
    normalize_author_display,
)
from calibre_ai_auditor.rules.lexical import clean_title, normalize_title_and_subtitle
from calibre_ai_auditor.rules.periodicals import match_periodical


def test_clean_title():
    assert clean_title("Human Action [WeLib.org]") == "Human Action"
    assert clean_title("The Prince (Z-Library).epub") == "The Prince"
    assert clean_title("Capital in the 21st Century [oceanofpdf.com] [OCR]") == "Capital in the 21st Century"
    assert clean_title("Deep Learning v1.0.pdf") == "Deep Learning"
    assert clean_title("The Economist_print") == "The Economist"
    assert clean_title("Microsoft Word - Document.pdf") == "Document"


def test_normalize_title_and_subtitle():
    main, sub = normalize_title_and_subtitle("12 Rules for Life: An Antidote to Chaos")
    assert main == "12 Rules for Life"
    assert sub == "An Antidote to Chaos"

    main2, sub2 = normalize_title_and_subtitle("Meditations")
    assert main2 == "Meditations"
    assert sub2 is None


def test_normalize_author_display():
    assert normalize_author_display("Parker, Geoffrey") == "Geoffrey Parker"
    assert normalize_author_display("Thomas Aquinas, Saint") == "Saint Thomas Aquinas"
    assert normalize_author_display("Benedict XVI, Pope") == "Pope Benedict XVI"
    assert normalize_author_display("Economist, The") == "The Economist"
    assert normalize_author_display("Ludwig von Mises") == "Ludwig von Mises"


def test_compute_author_sort():
    assert compute_author_sort("Geoffrey Parker") == "Parker, Geoffrey"
    assert compute_author_sort("Ludwig von Mises") == "von Mises, Ludwig"
    assert compute_author_sort("Saint Thomas Aquinas") == "Thomas Aquinas, Saint"
    assert compute_author_sort("Pope Benedict XVI") == "Benedict XVI, Pope"
    assert compute_author_sort("The Economist") == "Economist, The"
    assert compute_author_sort("Der Spiegel") == "Spiegel, Der"
    assert compute_author_sort("Financial Times") == "Financial Times"


def test_multiple_authors():
    authors = ["Geoffrey Parker", "Ludwig von Mises"]
    assert join_multiple_authors(authors) == "Geoffrey Parker & Ludwig von Mises"
    assert join_multiple_author_sorts(authors) == "Parker, Geoffrey & von Mises, Ludwig"


def test_match_periodical():
    rule = match_periodical("The Economist - September 2, 2024")
    assert rule is not None
    assert rule.canonical_author == "The Economist"
    assert rule.canonical_sort == "Economist, The"
    assert "Hemeroteca" in rule.tags

    rule_ft = match_periodical("Financial Times Weekend Edition")
    assert rule_ft is not None
    assert rule_ft.canonical_author == "Financial Times"

    rule_unknown = match_periodical("Normal Book Title")
    assert rule_unknown is None

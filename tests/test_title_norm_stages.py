"""Title normalizers are stage-specific by design — do NOT unify them.

- calibre_title_sort: sort-key inversion ("The Hobbit" -> "Hobbit, The").
- clean_title: distributor-junk stripping, preserves display text.
- verification._normalize_text: match-time folding (lowercase, edition-tag strip).
- curation._normalize_str: cluster-time folding (article strip, keeps numbers).

Proven divergent on purpose; unifying any pair changes sort, match, or
cluster behavior. This test locks the distinction.
"""

from calibre_ai_auditor.calibre.direct_engine import calibre_title_sort
from calibre_ai_auditor.curation.duplicates import _normalize_str as cluster_norm
from calibre_ai_auditor.rules.lexical import clean_title
from calibre_ai_auditor.verification.rules import _normalize_text as match_norm


def test_sort_key_inverts_articles():
    assert calibre_title_sort("The Hobbit") == "Hobbit, The"
    assert calibre_title_sort("1984") == "1984"


def test_clean_preserves_display_text_but_strips_distributor_junk():
    assert clean_title("The Hobbit") == "The Hobbit"
    assert clean_title("Dune [retail]") == "Dune"


def test_match_and_cluster_fold_differently_by_design():
    # match strips parenthesized series/year; cluster keeps the numbers
    assert match_norm("Foundation (Book 1)") == "foundation"
    assert cluster_norm("Foundation (Book 1)") == "foundation book 1"
    # cluster strips leading articles; match keeps them
    assert cluster_norm("The Hobbit") == "hobbit"
    assert match_norm("The Hobbit") == "the hobbit"

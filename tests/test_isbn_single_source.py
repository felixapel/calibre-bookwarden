"""ISBN single-source: the three historical copies must agree."""

from calibre_ai_auditor.curation.duplicates import is_valid_isbn10, is_valid_isbn13
from calibre_ai_auditor.rules.isbn import canonical_isbn13, isbn10_checksum_valid, isbn13_checksum_valid
from calibre_ai_auditor.verification.identity_v2 import validate_isbn
from calibre_ai_auditor.verification.rules import _isbn10_checksum_valid, _isbn13_checksum_valid

VALID_13 = "9780306406157"
VALID_10 = "0306406152"
BAD_13 = "9780306406158"


def test_checksum_copies_agree():
    assert is_valid_isbn10(VALID_10) == isbn10_checksum_valid(VALID_10) == _isbn10_checksum_valid(VALID_10) is True
    assert is_valid_isbn13(VALID_13) == isbn13_checksum_valid(VALID_13) == _isbn13_checksum_valid(VALID_13) is True
    assert isbn13_checksum_valid(BAD_13) is False
    assert _isbn13_checksum_valid(BAD_13) is False


def test_canonical_matches_identity():
    assert validate_isbn(VALID_10) == canonical_isbn13(VALID_10) == VALID_13
    assert validate_isbn(VALID_13) == VALID_13
    assert validate_isbn(BAD_13) is None

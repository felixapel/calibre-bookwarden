from calibre_ai_auditor.evidence.field_rules import (
    resolve_authors,
    resolve_isbn,
    resolve_publisher,
    resolve_title,
)
from calibre_ai_auditor.evidence.models import EvidenceItem, EvidenceSourceType


def test_resolve_title_filename_low_confidence() -> None:
    items = [
        EvidenceItem(
            id="1",
            book_key="test",
            field="title",
            value="Filename Title",
            source_type=EvidenceSourceType.filename,
            source_name="file",
            priority=7,
            confidence=90,
        )
    ]
    res = resolve_title(items)
    assert res.selected_value == "Filename Title"
    assert res.confidence == 50
    assert res.requires_review is True
    assert "filename_only" in res.risk_flags


def test_resolve_authors_swap() -> None:
    items = [
        EvidenceItem(
            id="1",
            book_key="test",
            field="authors",
            value=["Real Author"],
            source_type=EvidenceSourceType.book_content,
            source_name="title_page",
            priority=1,
            confidence=95,
        ),
        EvidenceItem(
            id="2",
            book_key="test",
            field="authors",
            value=["Wrong Author"],
            source_type=EvidenceSourceType.external_provider,
            source_name="openlibrary",
            priority=5,
            confidence=90,
        ),
    ]
    res = resolve_authors(items)
    assert res.selected_value == ["Real Author"]
    assert res.requires_review is True
    assert "author_swap" in res.risk_flags


def test_resolve_isbn_invalid_checksum() -> None:
    items = [
        EvidenceItem(
            id="1",
            book_key="test",
            field="isbn",
            value="1234567890",  # Invalid checksum
            source_type=EvidenceSourceType.book_content,
            source_name="title_page",
            priority=1,
            confidence=95,
        )
    ]
    res = resolve_isbn(items)
    assert res.selected_value == "1234567890"
    assert res.requires_review is True
    assert "invalid_isbn_checksum" in res.risk_flags


def test_resolve_isbn_valid_checksum() -> None:
    items = [
        EvidenceItem(
            id="1",
            book_key="test",
            field="isbn",
            value="978-0-306-40615-7",  # Valid ISBN-13
            source_type=EvidenceSourceType.book_content,
            source_name="title_page",
            priority=1,
            confidence=95,
        )
    ]
    res = resolve_isbn(items)
    assert res.selected_value == "978-0-306-40615-7"
    assert res.requires_review is False
    assert "invalid_isbn_checksum" not in res.risk_flags


def test_resolve_publisher_only_external() -> None:
    items = [
        EvidenceItem(
            id="1",
            book_key="test",
            field="publisher",
            value="Some Publisher",
            source_type=EvidenceSourceType.external_provider,
            source_name="openlibrary",
            priority=5,
            confidence=90,
        )
    ]
    res = resolve_publisher(items)
    assert res.selected_value == "Some Publisher"
    assert res.requires_review is True
    assert res.confidence == 60

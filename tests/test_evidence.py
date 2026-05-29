from calibre_ai_auditor.evidence.models import EvidenceItem, EvidenceSourceType
from calibre_ai_auditor.evidence.priority import get_priority
from calibre_ai_auditor.evidence.resolver import build_resolution


def test_priority_ordering() -> None:
    assert get_priority(EvidenceSourceType.user_override) == 0
    assert get_priority(EvidenceSourceType.book_content) == 1
    assert get_priority(EvidenceSourceType.filename) == 7


def test_resolver_title_preference() -> None:
    items = [
        EvidenceItem(
            id="1",
            book_key="test",
            field="title",
            value="Filename Title",
            source_type=EvidenceSourceType.filename,
            source_name="file",
            priority=get_priority(EvidenceSourceType.filename),
            confidence=90,
        ),
        EvidenceItem(
            id="2",
            book_key="test",
            field="title",
            value="Content Title",
            source_type=EvidenceSourceType.book_content,
            source_name="title_page",
            priority=get_priority(EvidenceSourceType.book_content),
            confidence=95,
        ),
    ]

    res = build_resolution("test", items)
    assert "title" in res.resolved_fields
    assert res.resolved_fields["title"].selected_value == "Content Title"
    assert res.proposed_patch["title"] == "Content Title"
    assert res.recommended_action == "suggest_fix"


def test_resolver_author_swap_risk() -> None:
    items = [
        EvidenceItem(
            id="1",
            book_key="test",
            field="authors",
            value=["Real Author"],
            source_type=EvidenceSourceType.book_content,
            source_name="title_page",
            priority=get_priority(EvidenceSourceType.book_content),
            confidence=95,
        ),
        EvidenceItem(
            id="2",
            book_key="test",
            field="authors",
            value=["Wrong Author"],
            source_type=EvidenceSourceType.external_provider,
            source_name="openlibrary",
            priority=get_priority(EvidenceSourceType.external_provider),
            confidence=90,
        ),
    ]

    res = build_resolution("test", items)
    assert "author_swap" in res.risk_flags
    assert res.recommended_action == "needs_review"


def test_resolver_cover_mismatch_risk() -> None:
    items = [
        EvidenceItem(
            id="1",
            book_key="test",
            field="title",
            value="Selected Book Title",
            source_type=EvidenceSourceType.book_content,
            source_name="title_page",
            priority=get_priority(EvidenceSourceType.book_content),
            confidence=95,
        ),
        EvidenceItem(
            id="2",
            book_key="test",
            field="title",
            value="Completely Different Cover Title",
            source_type=EvidenceSourceType.book_content,
            source_name="vision_verifier",
            priority=get_priority(EvidenceSourceType.book_content),
            confidence=80,
        ),
    ]

    res = build_resolution("test", items)
    assert "cover_mismatch" in res.risk_flags
    assert res.recommended_action == "needs_review"

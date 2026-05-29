from calibre_ai_auditor.evidence.locks import evidence_from_field_locks
from calibre_ai_auditor.evidence.models import EvidenceSourceType
from calibre_ai_auditor.storage.models import BookRecord


def test_evidence_from_field_locks() -> None:
    book = BookRecord(
        book_key="calibre:1",
        run_id="run_test",
        field_locks={"title": "Locked Title", "authors": ["A. Author"]},
    )
    items = evidence_from_field_locks(book)
    assert len(items) == 2
    assert all(i.source_type == EvidenceSourceType.user_override for i in items)
    assert items[0].confidence == 100

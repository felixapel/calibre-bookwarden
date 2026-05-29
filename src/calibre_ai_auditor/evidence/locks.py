from calibre_ai_auditor.evidence.models import EvidenceItem, EvidenceSourceType
from calibre_ai_auditor.evidence.priority import get_priority
from calibre_ai_auditor.storage.models import BookRecord


def evidence_from_field_locks(book: BookRecord) -> list[EvidenceItem]:
    """Build highest-priority evidence items from user-locked fields."""
    locks: dict[str, object] = getattr(book, "field_locks", None) or {}
    if not isinstance(locks, dict):
        return []

    items: list[EvidenceItem] = []
    priority = get_priority(EvidenceSourceType.user_override)
    for field, value in locks.items():
        if value is None:
            continue
        items.append(
            EvidenceItem(
                id=f"lock_{book.book_key}_{field}".replace(":", "_"),
                book_key=book.book_key,
                field=str(field),
                value=value,
                source_type=EvidenceSourceType.user_override,
                source_name="user_lock",
                priority=priority,
                confidence=100,
                quote_or_reason="Field locked by user in review UI",
            )
        )
    return items

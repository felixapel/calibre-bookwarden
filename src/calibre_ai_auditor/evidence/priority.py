from calibre_ai_auditor.evidence.models import EvidenceSourceType

SOURCE_PRIORITY = {
    EvidenceSourceType.user_override: 0,
    EvidenceSourceType.book_content: 1,
    EvidenceSourceType.cover: 2,
    EvidenceSourceType.embedded_metadata: 3,
    EvidenceSourceType.current_calibre: 4,
    EvidenceSourceType.external_provider: 5,
    EvidenceSourceType.llm_inference: 6,
    EvidenceSourceType.filename: 7,
    EvidenceSourceType.file_system: 8,
}


def get_priority(source_type: EvidenceSourceType) -> int:
    """Returns the priority value (lower is higher priority) for a given source type."""
    return SOURCE_PRIORITY.get(source_type, 99)

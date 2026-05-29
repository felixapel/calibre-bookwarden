from calibre_ai_auditor.evidence.field_rules import resolve_field
from calibre_ai_auditor.evidence.models import EvidenceItem, MetadataResolution


def build_resolution(book_key: str, evidence_items: list[EvidenceItem]) -> MetadataResolution:
    """
    Deterministically resolves metadata fields based on collected evidence items.
    Produces a MetadataResolution object containing the proposed patch and confidence.
    """
    # Group items by field
    grouped_items: dict[str, list[EvidenceItem]] = {}
    for item in evidence_items:
        if not item.value:
            continue
        grouped_items.setdefault(item.field, []).append(item)

    resolution = MetadataResolution(book_key=book_key)
    all_risk_flags = set()
    total_confidence = 0
    field_count = 0

    for field_name, items in grouped_items.items():
        resolved_field = resolve_field(field_name, items)
        resolution.resolved_fields[field_name] = resolved_field

        if resolved_field.selected_value is not None:
            resolution.proposed_patch[field_name] = resolved_field.selected_value

        all_risk_flags.update(resolved_field.risk_flags)

        # Accumulate confidence for fields we actually resolved
        if resolved_field.selected_value is not None:
            total_confidence += resolved_field.confidence
            field_count += 1

    resolution.risk_flags = list(all_risk_flags)

    if field_count > 0:
        resolution.overall_confidence = total_confidence // field_count
    else:
        resolution.overall_confidence = 0

    # Determine recommended action
    if not resolution.proposed_patch:
        resolution.recommended_action = "no_change"
    elif resolution.risk_flags or any(
        f.requires_review for f in resolution.resolved_fields.values()
    ):
        resolution.recommended_action = "needs_review"
    elif resolution.overall_confidence >= 85:
        resolution.recommended_action = "suggest_fix"
    elif resolution.overall_confidence >= 70:
        resolution.recommended_action = "needs_review"
    else:
        resolution.recommended_action = "defer"

    return resolution

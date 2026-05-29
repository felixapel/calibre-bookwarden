import re

from calibre_ai_auditor.evidence.models import EvidenceItem, EvidenceSourceType, ResolvedField


def resolve_title(items: list[EvidenceItem]) -> ResolvedField:
    if not items:
        return ResolvedField(field="title", selected_value=None)

    sorted_items = sorted(items, key=lambda x: x.priority)
    top = sorted_items[0]

    requires_review = False
    risk_flags = list(top.risk_flags)
    confidence = top.confidence
    reason = f"Selected from {top.source_type.value} ({top.source_name})"

    if top.source_type == EvidenceSourceType.filename:
        confidence = min(confidence, 50)
        requires_review = True
        reason = "Title derived only from filename. Review recommended."
        risk_flags.append("filename_only")

    # Check for vision cover mismatches
    for alt in items:
        if alt.source_name == "vision_verifier" and alt.value:
            norm_top = re.sub(r"[^\w\s]", "", str(top.value).lower()).strip()
            norm_alt = re.sub(r"[^\w\s]", "", str(alt.value).lower()).strip()
            if norm_top and norm_alt and (norm_top not in norm_alt and norm_alt not in norm_top):
                risk_flags.append("cover_mismatch")
                requires_review = True
                confidence = min(confidence, 70)
                reason += ". Warning: Cover title mismatch detected."
                break

    return ResolvedField(
        field="title",
        selected_value=top.value,
        selected_source=top,
        confidence=confidence,
        alternatives=sorted_items[1:],
        reason=reason,
        requires_review=requires_review,
        risk_flags=risk_flags,
    )


def resolve_authors(items: list[EvidenceItem]) -> ResolvedField:
    if not items:
        return ResolvedField(field="authors", selected_value=[])

    sorted_items = sorted(items, key=lambda x: x.priority)
    top = sorted_items[0]

    requires_review = False
    risk_flags = list(top.risk_flags)
    confidence = top.confidence
    reason = f"Selected from {top.source_type.value} ({top.source_name})"

    top_authors_set = set(top.value) if isinstance(top.value, list) else {top.value}
    for alt in sorted_items[1:]:
        if alt.priority <= 5:
            alt_authors_set = set(alt.value) if isinstance(alt.value, list) else {alt.value}
            if (
                top_authors_set
                and alt_authors_set
                and not top_authors_set.intersection(alt_authors_set)
            ):
                risk_flags.append("author_swap")
                requires_review = True
                confidence = min(confidence, 75)
                reason += f". Warning: Conflict with {alt.source_type.value}."

    # Check for vision cover mismatches
    for alt in items:
        if alt.source_name == "vision_verifier" and alt.value:
            top_authors = {
                re.sub(r"[^\w\s]", "", str(a).lower()).strip()
                for a in (top.value if isinstance(top.value, list) else [top.value])
                if a
            }
            alt_authors = {
                re.sub(r"[^\w\s]", "", str(a).lower()).strip()
                for a in (alt.value if isinstance(alt.value, list) else [alt.value])
                if a
            }
            if top_authors and alt_authors and not top_authors.intersection(alt_authors):
                risk_flags.append("cover_mismatch")
                requires_review = True
                confidence = min(confidence, 70)
                reason += ". Warning: Cover author mismatch detected."
                break

    return ResolvedField(
        field="authors",
        selected_value=top.value,
        selected_source=top,
        confidence=confidence,
        alternatives=sorted_items[1:],
        reason=reason,
        requires_review=requires_review,
        risk_flags=list(set(risk_flags)),
    )


def _validate_isbn(isbn: str) -> bool:
    clean = re.sub(r"[\s-]", "", isbn)
    if len(clean) == 10:
        # Checksum ISBN-10
        total = 0
        for i in range(9):
            if not clean[i].isdigit():
                return False
            total += int(clean[i]) * (10 - i)
        last = 10 if clean[9].upper() == "X" else int(clean[9]) if clean[9].isdigit() else -1
        if last == -1:
            return False
        total += last
        return total % 11 == 0
    elif len(clean) == 13:
        # Checksum ISBN-13
        if not clean.isdigit():
            return False
        total = 0
        for i in range(12):
            multiplier = 1 if i % 2 == 0 else 3
            total += int(clean[i]) * multiplier
        last = int(clean[12])
        return (10 - (total % 10)) % 10 == last
    return False


def resolve_isbn(items: list[EvidenceItem]) -> ResolvedField:
    if not items:
        return ResolvedField(field="isbn", selected_value=None)

    sorted_items = sorted(items, key=lambda x: x.priority)
    top = None
    for item in sorted_items:
        if item.value and _validate_isbn(item.value):
            top = item
            break

    if not top:
        # Fallback to the top item anyway, even if invalid, but mark review
        top = sorted_items[0]

    requires_review = False
    risk_flags = list(top.risk_flags)
    confidence = top.confidence
    reason = f"Selected from {top.source_type.value} ({top.source_name})"

    if not _validate_isbn(top.value):
        risk_flags.append("invalid_isbn_checksum")
        requires_review = True
        confidence = min(confidence, 50)
        reason += ". Warning: Invalid ISBN checksum."

    top_isbn = re.sub(r"[\s-]", "", str(top.value))
    for alt in sorted_items:
        if alt.id == top.id:
            continue
        alt_isbn = re.sub(r"[\s-]", "", str(alt.value)) if alt.value else ""
        if alt_isbn and top_isbn and alt_isbn != top_isbn:
            risk_flags.append("isbn_conflict")
            requires_review = True
            confidence = min(confidence, 60)
            reason += f". Warning: Conflict with {alt.source_type.value}."
            break

    return ResolvedField(
        field="isbn",
        selected_value=top.value,
        selected_source=top,
        confidence=confidence,
        alternatives=[i for i in sorted_items if i.id != top.id],
        reason=reason,
        requires_review=requires_review,
        risk_flags=list(set(risk_flags)),
    )


def resolve_publisher(items: list[EvidenceItem]) -> ResolvedField:
    if not items:
        return ResolvedField(field="publisher", selected_value=None)

    sorted_items = sorted(items, key=lambda x: x.priority)
    top = sorted_items[0]

    requires_review = False
    risk_flags = list(top.risk_flags)
    confidence = top.confidence
    reason = f"Selected from {top.source_type.value} ({top.source_name})"

    if top.source_type == EvidenceSourceType.external_provider:
        provider_count = sum(
            1
            for i in items
            if i.source_type == EvidenceSourceType.external_provider and i.value == top.value
        )
        if provider_count < 2:
            requires_review = True
            confidence = min(confidence, 60)
            reason = "Publisher suggested only by external provider. Review required."

    return ResolvedField(
        field="publisher",
        selected_value=top.value,
        selected_source=top,
        confidence=confidence,
        alternatives=sorted_items[1:],
        reason=reason,
        requires_review=requires_review,
        risk_flags=risk_flags,
    )


def resolve_publication_date(items: list[EvidenceItem]) -> ResolvedField:
    if not items:
        return ResolvedField(field="published_date", selected_value=None)

    sorted_items = sorted(items, key=lambda x: x.priority)
    top = sorted_items[0]
    requires_review = False

    if top.source_type == EvidenceSourceType.external_provider:
        requires_review = True

    return ResolvedField(
        field="published_date",
        selected_value=top.value,
        selected_source=top,
        confidence=top.confidence,
        alternatives=sorted_items[1:],
        reason=f"Selected from {top.source_type.value} ({top.source_name})",
        requires_review=requires_review,
    )


def resolve_language(items: list[EvidenceItem]) -> ResolvedField:
    if not items:
        return ResolvedField(field="language", selected_value=None)

    sorted_items = sorted(items, key=lambda x: x.priority)
    top = sorted_items[0]
    return ResolvedField(
        field="language",
        selected_value=top.value,
        selected_source=top,
        confidence=top.confidence,
        alternatives=sorted_items[1:],
        reason=f"Selected from {top.source_type.value} ({top.source_name})",
        requires_review=False,
    )


def resolve_series(items: list[EvidenceItem]) -> ResolvedField:
    if not items:
        return ResolvedField(field="series", selected_value=None)

    sorted_items = sorted(items, key=lambda x: x.priority)
    top = sorted_items[0]

    requires_review = False
    if top.source_type == EvidenceSourceType.external_provider:
        requires_review = True

    return ResolvedField(
        field="series",
        selected_value=top.value,
        selected_source=top,
        confidence=top.confidence,
        alternatives=sorted_items[1:],
        reason=f"Selected from {top.source_type.value} ({top.source_name})",
        requires_review=requires_review,
    )


def resolve_description(items: list[EvidenceItem]) -> ResolvedField:
    if not items:
        return ResolvedField(field="description", selected_value=None)

    sorted_items = sorted(items, key=lambda x: x.priority)
    top = sorted_items[0]

    requires_review = False
    if top.source_type == EvidenceSourceType.external_provider:
        requires_review = True

    return ResolvedField(
        field="description",
        selected_value=top.value,
        selected_source=top,
        confidence=top.confidence,
        alternatives=sorted_items[1:],
        reason=f"Selected from {top.source_type.value} ({top.source_name})",
        requires_review=requires_review,
    )


def resolve_field(field_name: str, items: list[EvidenceItem]) -> ResolvedField:
    if field_name == "title":
        return resolve_title(items)
    elif field_name == "authors":
        return resolve_authors(items)
    elif field_name == "isbn":
        return resolve_isbn(items)
    elif field_name == "publisher":
        return resolve_publisher(items)
    elif field_name == "published_date":
        return resolve_publication_date(items)
    elif field_name == "language":
        return resolve_language(items)
    elif field_name == "series":
        return resolve_series(items)
    elif field_name in ("description", "comments"):
        return resolve_description(items)

    # Generic fallback
    if not items:
        return ResolvedField(field=field_name, selected_value=None)
    sorted_items = sorted(items, key=lambda x: x.priority)
    top = sorted_items[0]
    return ResolvedField(
        field=field_name,
        selected_value=top.value,
        selected_source=top,
        confidence=top.confidence,
        alternatives=sorted_items[1:],
        reason=f"Selected highest priority source: {top.source_type.value}",
        requires_review=top.priority >= 5,  # Need review if only external/llm
    )

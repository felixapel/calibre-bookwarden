from typing import Any

from calibre_ai_auditor.storage.models import BookRecord, EvidencePackage


def build_review_context(book: BookRecord, package: EvidencePackage | None) -> dict[str, Any]:
    """Build Jinja context for HTML/PDF review packets."""
    meta = book.current_metadata or {}
    ctx: dict[str, Any] = {
        "title": meta.get("title") or book.book_key,
        "book_key": book.book_key,
        "run_id": book.run_id,
        "status": book.status,
        "authors": meta.get("authors") or [],
        "identifiers": meta.get("identifiers") or {},
        "tags": meta.get("tags") or [],
        "extracted": {},
        "candidates": [],
        "snippets": [],
        "decision": None,
        "risk_flags": [],
        "resolved_fields": [],
    }
    if not package:
        return ctx

    ctx["extracted"] = package.extracted or {}
    ctx["candidates"] = package.candidates or []
    ctx["snippets"] = package.snippets or []
    ctx["risk_flags"] = package.risk_flags or []
    ctx["decision"] = package.decision

    decision = package.decision or {}
    patch = decision.get("proposed_patch") or {}
    for field, value in patch.items():
        ctx["resolved_fields"].append(
            {
                "field": field,
                "value": value,
                "confidence": decision.get("confidence"),
            }
        )
    return ctx

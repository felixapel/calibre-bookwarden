import logging
from typing import Any, cast

logger = logging.getLogger(__name__)


def check_risks(current: dict[str, Any], patch: dict[str, Any]) -> list[str]:
    """
    Deterministic rules to flag high-risk suggestions.
    """
    flags = []

    # 1. Author swap
    current_authors = set(current.get("authors", []))
    patch_authors = set(patch.get("authors", []))

    if current_authors and patch_authors and not current_authors.intersection(patch_authors):
        flags.append("author_swap")

    # 2. ISBN conflict
    current_isbn = current.get("identifiers", {}).get("isbn")
    patch_isbn = patch.get("identifiers", {}).get("isbn")

    if current_isbn and patch_isbn and current_isbn != patch_isbn:
        flags.append("isbn_conflict")

    # 3. Title ambiguity is handled by the verification rules engine
    # (fuzzy title match); this legacy pass was a no-op and is removed.
    return flags


def apply_confidence_thresholds(verdict: dict[str, Any]) -> str:
    """
    Adjust recommended action based on confidence scores.
    """
    confidence = verdict.get("confidence", 0)
    action = cast(str, verdict.get("recommended_action", "no_change"))
    risk_flags = verdict.get("risk_flags", [])

    if action == "suggest_fix":
        if confidence < 70:
            return "defer"
        if confidence < 90 or risk_flags:
            return "needs_review"

    return action

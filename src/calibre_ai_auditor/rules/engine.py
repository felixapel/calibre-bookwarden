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

    # 3. Title ambiguity (very different titles)
    current_title = current.get("title", "").lower()
    patch_title = patch.get("title", "").lower()

    # Very crude check: if neither title contains a significant word from the other
    # This should be replaced with a proper fuzzy match later
    if (
        current_title
        and patch_title
        and len(current_title) > 5
        and len(patch_title) > 5
        and patch_title not in current_title
        and current_title not in patch_title
    ):
        # We could add more logic here, but let's keep it simple for v0.2
        pass

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

from calibre_ai_auditor.rules.engine import apply_confidence_thresholds, check_risks


def test_check_risks_author_swap() -> None:
    current = {"authors": ["Frank Herbert"]}
    patch = {"authors": ["Brian Herbert"]}
    risks = check_risks(current, patch)
    assert "author_swap" in risks


def test_check_risks_no_author_swap() -> None:
    current = {"authors": ["Frank Herbert"]}
    patch = {"authors": ["Frank Herbert", "Someone Else"]}
    risks = check_risks(current, patch)
    assert "author_swap" not in risks


def test_check_risks_isbn_conflict() -> None:
    current = {"identifiers": {"isbn": "12345"}}
    patch = {"identifiers": {"isbn": "67890"}}
    risks = check_risks(current, patch)
    assert "isbn_conflict" in risks


def test_apply_confidence_thresholds_low() -> None:
    verdict = {"recommended_action": "suggest_fix", "confidence": 50, "risk_flags": []}
    assert apply_confidence_thresholds(verdict) == "defer"


def test_apply_confidence_thresholds_risk() -> None:
    verdict = {"recommended_action": "suggest_fix", "confidence": 95, "risk_flags": ["author_swap"]}
    assert apply_confidence_thresholds(verdict) == "needs_review"


def test_apply_confidence_thresholds_safe() -> None:
    verdict = {"recommended_action": "suggest_fix", "confidence": 95, "risk_flags": []}
    assert apply_confidence_thresholds(verdict) == "suggest_fix"

"""Calibration smoke test — verifies the calibration pipeline can be exercised locally.

These tests don't require a real Calibre CLI; they use the synthetic
golden-truth fixtures to ensure the engine + JSON output work end-to-end.
The real calibration happens on Unraid via the runbook at
docs/calibration/v1.0_calibration_runbook.md.

Mark these tests with the `calibration` marker so they can be excluded
from the default test run.
"""

from __future__ import annotations

import json
import tempfile
from pathlib import Path

import pytest

from calibre_ai_auditor.verification.engine import (
    ContentVerificationEngine,
    DeclaredMetadata,
    ObservationSet,
)


pytestmark = pytest.mark.benchmark


def test_pilot_json_structure_is_parsable() -> None:
    """The runbook expects /tmp/v1_pilot_100.json to have a known structure.
    Verify we can build that structure from synthetic fixtures."""
    from tests.fixtures.synthetic_library.gold_truth import SYNTHETIC_FIXTURES
    from tests.test_verification_synthetic import _build_declared, _build_observed

    engine = ContentVerificationEngine()
    verdicts = []
    for fixture in SYNTHETIC_FIXTURES[:10]:
        v = engine.verify(
            book_key=fixture["id"],
            run_id="calibration_pilot",
            declared=_build_declared(fixture),
            observed=_build_observed(fixture),
        )
        verdicts.append(v.model_dump())

    # Pilot JSON has the shape: {run_id, elapsed_seconds, verdicts: [...]}
    output = {
        "run_id": "calibration_pilot",
        "elapsed_seconds": 0.05,
        "verdicts": verdicts,
    }

    # Round-trip through JSON (use default=str to handle datetime fields)
    with tempfile.NamedTemporaryFile(mode="w", suffix=".json", delete=False) as f:
        json.dump(output, f, default=str)
        path = Path(f.name)

    data = json.loads(path.read_text())
    assert "run_id" in data
    assert "elapsed_seconds" in data
    assert "verdicts" in data
    assert len(data["verdicts"]) == 10

    # Every verdict has the expected action field
    for v in data["verdicts"]:
        assert "action" in v
        assert v["action"] in ("no_change", "suggest_fix", "needs_review", "defer")
        assert "field_verdicts" in v
        assert "overall_confidence" in v
        assert "auto_apply_eligible" in v


def test_action_distribution_computes_correctly() -> None:
    """Verify the Counter-based analysis pattern from the runbook works."""
    from tests.fixtures.synthetic_library.gold_truth import SYNTHETIC_FIXTURES
    from tests.test_verification_synthetic import _build_declared, _build_observed

    engine = ContentVerificationEngine()
    verdicts = []
    for fixture in SYNTHETIC_FIXTURES:
        v = engine.verify(
            book_key=fixture["id"],
            run_id="dist_check",
            declared=_build_declared(fixture),
            observed=_build_observed(fixture),
        )
        verdicts.append(v)

    # Action breakdown
    from collections import Counter
    actions = Counter(v.action.value for v in verdicts)
    eligible = sum(1 for v in verdicts if v.auto_apply_eligible)
    total = len(verdicts)

    # Sanity: every action value is one of the 4 expected
    for action in actions:
        assert action in ("no_change", "suggest_fix", "needs_review", "defer")

    # Auto-apply eligible percentage should be in [0, 100]
    pct = 100.0 * eligible / total
    assert 0 <= pct <= 100

    # Risk flag distribution
    flags = Counter()
    for v in verdicts:
        for f in v.risk_flags:
            flags[f] += 1
    # Flags must be one of the known set
    known_flags = {
        "author_swap",
        "isbn_conflict",
        "edition_ambiguous",
        "cover_mismatch",
        "wrong_book",
        "series_mismatch",
        "publisher_mismatch",
        "title_noisy",
        "language_mismatch",
    }
    for flag in flags:
        assert flag in known_flags


def test_per_book_throughput_at_pilot_scale() -> None:
    """Verify the engine can process 30 books (pilot size) in <5 seconds locally.
    Real Unraid will be faster; this is just a smoke test."""
    import time

    from tests.fixtures.synthetic_library.gold_truth import SYNTHETIC_FIXTURES
    from tests.test_verification_synthetic import _build_declared, _build_observed

    engine = ContentVerificationEngine()

    started = time.monotonic()
    for fixture in SYNTHETIC_FIXTURES:
        engine.verify(
            book_key=fixture["id"],
            run_id="throughput_check",
            declared=_build_declared(fixture),
            observed=_build_observed(fixture),
        )
    elapsed = time.monotonic() - started

    # Pilot must complete in <5s locally; real Unraid should be much faster
    assert elapsed < 5.0, f"Pilot took {elapsed:.2f}s — too slow for production"


def test_calibration_report_template_filled() -> None:
    """Sanity-check that the runbook template file exists and is parseable."""
    runbook = Path(__file__).parent.parent.parent / "docs" / "calibration" / "v1.0_calibration_runbook.md"
    assert runbook.exists(), f"Runbook missing: {runbook}"

    content = runbook.read_text()
    # Must have all 6 phases
    for phase in ("Phase 1", "Phase 2", "Phase 3", "Phase 4", "Phase 5", "Phase 6"):
        assert phase in content, f"Runbook missing section: {phase}"
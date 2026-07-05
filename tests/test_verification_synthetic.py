"""Regression backbone: every synthetic fixture must produce the gold-truth verdict."""

from __future__ import annotations

import pytest

from calibre_ai_auditor.verification.engine import (
    ContentVerificationEngine,
    DeclaredMetadata,
    ObservationSet,
)
from tests.fixtures.gold_truth import VERIFICATION_CONTRACT
from tests.fixtures.synthetic_library.gold_truth import SYNTHETIC_FIXTURES


def _build_declared(field_entry: dict) -> DeclaredMetadata:
    """Build DeclaredMetadata from a fixture field entry's declared value."""
    fields = {}
    for entry in field_entry["fields"]:
        fields[entry["field"]] = entry["declared"]
    return DeclaredMetadata(
        title=fields.get("title"),
        authors=fields.get("authors") or [],
        publisher=fields.get("publisher"),
        published_date=fields.get("published_date"),
        language=fields.get("language"),
        series=fields.get("series"),
        series_index=fields.get("series_index"),
        isbn=fields.get("isbn"),
    )


def _build_observed(field_entry: dict) -> ObservationSet:
    """Build ObservationSet from a fixture field entry's observed value."""
    fields = {}
    for entry in field_entry["fields"]:
        fields[entry["field"]] = entry["observed"]

    # Headers / copyright / body — synthetic fixtures may not include raw text,
    # so we leave them None unless the fixture sets has_text_layer=False.
    has_text = field_entry.get("has_text_layer", True)

    # OCR quality defaults to "high" unless fixture sets "low" via ocr_quality field.
    evidence_quality = field_entry.get("ocr_quality", "high")
    if evidence_quality == "low":
        quality_str = "low"
    elif has_text:
        quality_str = "high"
    else:
        # Scanned PDF with no signal about OCR quality → assume medium unless overridden
        quality_str = field_entry.get("ocr_quality", "medium")

    return ObservationSet(
        title_page_text=("[placeholder title page text]" if has_text else None),
        copyright_page_text=("[placeholder copyright page]" if has_text else None),
        header_running_text=("[running header sample]" if field_entry.get("header_sampled") else None),
        body_sample=("[placeholder body sample]" if has_text else None),
        isbn_extracted=fields.get("isbn"),
        publisher_extracted=fields.get("publisher"),
        date_extracted=fields.get("published_date"),
        language_detected=fields.get("language"),
        title_extracted=fields.get("title"),
        authors_extracted=fields.get("authors"),
        series_extracted=fields.get("series"),
        series_index_extracted=fields.get("series_index"),
        cover_phash_distance=field_entry.get("cover_phash_distance"),
        evidence_quality=quality_str,
    )


@pytest.fixture
def engine() -> ContentVerificationEngine:
    return ContentVerificationEngine()


@pytest.mark.parametrize(
    "fixture",
    SYNTHETIC_FIXTURES,
    ids=lambda f: f["id"],
)
def test_synthetic_fixture_matches_contract(
    fixture: dict, engine: ContentVerificationEngine
) -> None:
    declared = _build_declared(fixture)
    observed = _build_observed(fixture)
    verdict = engine.verify(
        book_key=fixture["id"],
        run_id="test_run",
        declared=declared,
        observed=observed,
    )
    contract = VERIFICATION_CONTRACT[fixture["id"]]

    # 1. Overall action must match
    assert verdict.action.value == contract["overall_action"], (
        f"{fixture['id']}: expected action {contract['overall_action']}, "
        f"got {verdict.action.value}. Reasons: {verdict.reasons}"
    )

    # 2. Auto-apply eligibility must match
    assert verdict.auto_apply_eligible == contract["auto_apply"], (
        f"{fixture['id']}: auto_apply mismatch. "
        f"expected {contract['auto_apply']}, got {verdict.auto_apply_eligible}. "
        f"Risk flags: {verdict.risk_flags}, conf: {verdict.overall_confidence}"
    )

    # 3. Per-field verdicts must match (within contract tolerance)
    for field_name, field_contract in contract["per_field"].items():
        fv = verdict.field_verdicts.get(field_name)
        assert fv is not None, f"{fixture['id']}: missing field verdict for {field_name}"
        assert fv.verdict.value == field_contract["verdict"], (
            f"{fixture['id']}.{field_name}: expected verdict "
            f"{field_contract['verdict']}, got {fv.verdict.value}. "
            f"Reason: {fv.reason}"
        )
        assert fv.confidence >= field_contract["min_conf"], (
            f"{fixture['id']}.{field_name}: confidence "
            f"{fv.confidence} below contract minimum {field_contract['min_conf']}"
        )

    # 4. Required risk flags must be present
    if "risk_flags_present" in contract:
        for required_flag in contract["risk_flags_present"]:
            assert required_flag in verdict.risk_flags, (
                f"{fixture['id']}: required risk flag {required_flag!r} missing. "
                f"Have: {verdict.risk_flags}"
            )


def test_fixture_count_matches_contract() -> None:
    """Sanity check: contract covers every fixture."""
    assert len(VERIFICATION_CONTRACT) == len(SYNTHETIC_FIXTURES)
    for fx in SYNTHETIC_FIXTURES:
        assert fx["id"] in VERIFICATION_CONTRACT, f"missing contract for {fx['id']}"
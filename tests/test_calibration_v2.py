from __future__ import annotations

import json
from datetime import UTC, datetime, timedelta
from pathlib import Path

from calibre_ai_auditor.config.settings import Settings
from calibre_ai_auditor.verification.calibration_v2 import (
    CalibrationReportV2,
    calibration_gate_from_settings,
    create_report_from_labeled_corpus,
)

LEGACY_V2_KEYS = ("observations_with_timing", "total_review_seconds", "total_egress_bytes")


def _legacy_v1_sealed_payload(now) -> dict:
    """Fabricate a pre-migration artifact: sealed over the v1 key shape only.

    The seal is computed over the report dump minus the v2 aggregate keys —
    exactly the payload shape old code sealed (shared-field serialization
    is unchanged by the migration).
    """
    import hashlib as _hashlib
    import json as _json

    report = CalibrationReportV2(
        schema_version=1,
        corpus_sha256="b" * 64,
        sample_size=200,
        tier_a_decisions=120,
        false_positive_count=0,
        false_auto_apply_count=0,
        evaluated_at=now,
        expires_at=now + timedelta(days=30),
    )
    payload = report.model_dump(mode="json", exclude={"report_sha256"} | set(LEGACY_V2_KEYS))
    encoded = _json.dumps(payload, sort_keys=True, separators=(",", ":"), ensure_ascii=True).encode()
    payload["report_sha256"] = _hashlib.sha256(encoded).hexdigest()
    return payload


def _report(*, false_auto_applies: int = 0, expires_delta: timedelta = timedelta(days=30)) -> CalibrationReportV2:
    now = datetime.now(UTC)
    return CalibrationReportV2(
        corpus_sha256="a" * 64,
        sample_size=200,
        tier_a_decisions=120,
        false_positive_count=0,
        false_auto_apply_count=false_auto_applies,
        evaluated_at=now,
        expires_at=now + expires_delta,
    ).seal()


def _settings(path: Path) -> Settings:
    settings = Settings()
    settings.manifestation_v2.auto_apply.enabled = True
    settings.manifestation_v2.auto_apply.calibration_report = path
    return settings


def test_valid_sealed_calibration_report_opens_the_gate(tmp_path: Path) -> None:
    path = tmp_path / "calibration.json"
    path.write_text(json.dumps(_report().model_dump(mode="json")))

    decision = calibration_gate_from_settings(_settings(path))

    assert decision.valid is True
    assert decision.report_sha256 is not None
    assert decision.reasons == []


def test_tampered_or_false_auto_apply_report_fails_closed(tmp_path: Path) -> None:
    path = tmp_path / "calibration.json"
    payload = _report().model_dump(mode="json")
    payload["false_auto_apply_count"] = 1
    path.write_text(json.dumps(payload))

    decision = calibration_gate_from_settings(_settings(path))

    assert decision.valid is False
    assert "seal" in " ".join(decision.reasons)

    path.write_text(json.dumps(_report(false_auto_applies=1).model_dump(mode="json")))
    decision = calibration_gate_from_settings(_settings(path))
    assert decision.valid is False
    assert "false auto-apply" in " ".join(decision.reasons)


def test_expired_report_and_disabled_feature_flag_fail_closed(tmp_path: Path) -> None:
    path = tmp_path / "calibration.json"
    path.write_text(json.dumps(_report(expires_delta=timedelta(seconds=-1)).model_dump(mode="json")))
    settings = _settings(path)

    expired = calibration_gate_from_settings(settings)
    settings.manifestation_v2.auto_apply.enabled = False
    disabled = calibration_gate_from_settings(settings)

    assert expired.valid is False
    assert "expired" in " ".join(expired.reasons)
    assert disabled.valid is False
    assert disabled.reasons == ["Tier A auto-apply feature flag is disabled"]


def test_labeled_corpus_derives_false_positive_counts_instead_of_accepting_claimed_totals(tmp_path: Path) -> None:
    corpus = tmp_path / "labels.json"
    corpus.write_text(
        json.dumps(
            {
                "schema_version": 1,
                "policy_version": "manifestation-v2",
                "observations": [
                    {
                        "evidence_id": "ev-1",
                        "tier": "A",
                        "would_auto_apply": True,
                        "identity_correct": True,
                        "patch_correct": True,
                    },
                    {
                        "evidence_id": "ev-2",
                        "tier": "A",
                        "would_auto_apply": True,
                        "identity_correct": False,
                        "patch_correct": False,
                    },
                    {
                        "evidence_id": "ev-3",
                        "tier": "B",
                        "would_auto_apply": False,
                        "identity_correct": False,
                        "patch_correct": False,
                    },
                ],
            }
        )
    )

    report = create_report_from_labeled_corpus(corpus)

    assert report.verify_seal()
    assert report.sample_size == 3
    assert report.tier_a_decisions == 2
    assert report.false_positive_count == 1
    assert report.false_auto_apply_count == 1


def test_pre_migration_v1_seal_still_verifies_and_opens_gate(tmp_path: Path) -> None:
    now = datetime.now(UTC)
    path = tmp_path / "legacy.json"
    path.write_text(json.dumps(_legacy_v1_sealed_payload(now)))

    report = CalibrationReportV2.model_validate(json.loads(path.read_text()))
    assert report.schema_version == 1
    assert report.verify_seal() is True

    decision = calibration_gate_from_settings(_settings(path))
    assert decision.valid is True
    assert decision.reasons == []


def test_v1_hybrid_with_non_default_v2_keys_fails_closed(tmp_path: Path) -> None:
    now = datetime.now(UTC)
    payload = _legacy_v1_sealed_payload(now)
    payload["total_egress_bytes"] = 999  # v1 stamp must not carry v2 data
    path = tmp_path / "hybrid.json"
    path.write_text(json.dumps(payload))

    report = CalibrationReportV2.model_validate(json.loads(path.read_text()))
    assert report.verify_seal() is False


def test_v2_corpus_timing_is_derived_not_trusted(tmp_path: Path) -> None:
    corpus = tmp_path / "labels.json"
    corpus.write_text(
        json.dumps(
            {
                "schema_version": 2,
                "policy_version": "manifestation-v2",
                "observations": [
                    {
                        "evidence_id": "ev-1",
                        "tier": "A",
                        "would_auto_apply": True,
                        "identity_correct": True,
                        "patch_correct": True,
                        "review_seconds": 12.5,
                        "egress_bytes": 1024,
                    },
                    {
                        "evidence_id": "ev-2",
                        "tier": "B",
                        "would_auto_apply": False,
                        "identity_correct": True,
                        "patch_correct": True,
                    },
                ],
            }
        )
    )

    report = create_report_from_labeled_corpus(corpus)

    assert report.schema_version == 2
    assert report.verify_seal()
    assert report.observations_with_timing == 1
    assert report.total_review_seconds == 12.5
    assert report.total_egress_bytes == 1024


def test_v1_corpus_without_timing_yields_zero_aggregates(tmp_path: Path) -> None:
    corpus = tmp_path / "labels.json"
    corpus.write_text(
        json.dumps(
            {
                "schema_version": 1,
                "policy_version": "manifestation-v2",
                "observations": [
                    {
                        "evidence_id": "ev-1",
                        "tier": "A",
                        "would_auto_apply": True,
                        "identity_correct": True,
                        "patch_correct": True,
                    },
                ],
            }
        )
    )

    report = create_report_from_labeled_corpus(corpus)

    assert report.schema_version == 2
    assert report.verify_seal()
    assert report.observations_with_timing == 0
    assert report.total_review_seconds == 0
    assert report.total_egress_bytes == 0

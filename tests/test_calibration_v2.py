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

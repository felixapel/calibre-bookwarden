"""Advisory calibration metrics for manifestation V2.

The SHA-256 values provide integrity checking, not authentication, and this
module is deliberately not connected to the writer authorization boundary.
"""

from __future__ import annotations

import hashlib
import json
import os
import re
import tempfile
from datetime import UTC, datetime, timedelta
from pathlib import Path
from typing import Literal

from pydantic import BaseModel, ConfigDict, Field, ValidationError, field_validator, model_validator

from calibre_ai_auditor.config.settings import Settings

SHA256_RE = re.compile(r"^[0-9a-f]{64}$")
MAX_REPORT_BYTES = 1024 * 1024
MAX_CORPUS_BYTES = 32 * 1024 * 1024


class CalibrationObservationV2(BaseModel):
    model_config = ConfigDict(extra="forbid")

    evidence_id: str = Field(min_length=1)
    tier: Literal["A", "B", "C"]
    would_auto_apply: bool
    identity_correct: bool
    patch_correct: bool
    # v2 operational telemetry (optional so v1 corpora validate unchanged).
    review_seconds: float | None = Field(default=None, ge=0)
    egress_bytes: int | None = Field(default=None, ge=0)


class CalibrationCorpusV2(BaseModel):
    model_config = ConfigDict(extra="forbid")

    schema_version: int = 1
    policy_version: str = "manifestation-v2"
    observations: list[CalibrationObservationV2] = Field(min_length=1)

    @model_validator(mode="after")
    def _unique_evidence_ids(self) -> CalibrationCorpusV2:
        evidence_ids = [item.evidence_id for item in self.observations]
        if len(evidence_ids) != len(set(evidence_ids)):
            raise ValueError("calibration evidence_id values must be unique")
        return self


CALIBRATION_SCHEMA_VERSION = 2
CALIBRATION_SCHEMA_VERSIONS = (1, 2)
# Keys introduced in v2. The legacy seal path excludes exactly these so
# pre-migration v1 seals keep verifying byte-identically (see verify_seal).
_V2_REPORT_KEYS = frozenset({"observations_with_timing", "total_review_seconds", "total_egress_bytes"})


class CalibrationReportV2(BaseModel):
    model_config = ConfigDict(extra="forbid")

    schema_version: int = CALIBRATION_SCHEMA_VERSION
    policy_version: str = "manifestation-v2"
    corpus_sha256: str
    sample_size: int = Field(ge=1)
    tier_a_decisions: int = Field(ge=0)
    false_positive_count: int = Field(ge=0)
    false_auto_apply_count: int = Field(ge=0)
    # v2 derived operational aggregates (derived from observations, like the
    # correctness counts above — never trusted from input).
    observations_with_timing: int = Field(default=0, ge=0)
    total_review_seconds: float = Field(default=0.0, ge=0)
    total_egress_bytes: int = Field(default=0, ge=0)
    evaluated_at: datetime
    expires_at: datetime
    report_sha256: str | None = None

    @field_validator("corpus_sha256")
    @classmethod
    def _valid_corpus_sha256(cls, value: str) -> str:
        lowered = value.lower()
        if not SHA256_RE.fullmatch(lowered):
            raise ValueError("corpus_sha256 must contain exactly 64 hexadecimal characters")
        return lowered

    @field_validator("report_sha256")
    @classmethod
    def _valid_report_sha256(cls, value: str | None) -> str | None:
        if value is None:
            return None
        lowered = value.lower()
        if not SHA256_RE.fullmatch(lowered):
            raise ValueError("report_sha256 must contain exactly 64 hexadecimal characters")
        return lowered

    def _seal_value(self, *, exclude: set[str] | None = None) -> str:
        payload = self.model_dump(mode="json", exclude={"report_sha256"} | (exclude or set()))
        encoded = json.dumps(payload, sort_keys=True, separators=(",", ":"), ensure_ascii=True).encode()
        return hashlib.sha256(encoded).hexdigest()

    def seal(self) -> CalibrationReportV2:
        return self.model_copy(update={"report_sha256": self._seal_value()})

    def _v2_keys_at_default(self) -> bool:
        return self.observations_with_timing == 0 and self.total_review_seconds == 0 and self.total_egress_bytes == 0

    def verify_seal(self) -> bool:
        if self.report_sha256 is None:
            return False
        if self.report_sha256 == self._seal_value():
            return True
        # Legacy path: pre-migration v1 seals were computed over a payload
        # without the v2 aggregate keys. Accept that exact shape only for
        # genuine v1 files (schema 1 + v2 keys at default); anything else
        # carrying non-default v2 data under a v1 stamp is malformed.
        if self.schema_version == 1 and self._v2_keys_at_default():
            return self.report_sha256 == self._seal_value(exclude=set(_V2_REPORT_KEYS))
        return False


class CalibrationGateDecision(BaseModel):
    model_config = ConfigDict(extra="forbid")

    valid: bool
    report_sha256: str | None = None
    reasons: list[str] = Field(default_factory=list)


def _read_report(path: Path) -> CalibrationReportV2:
    absolute = path.absolute()
    current = Path(absolute.anchor)
    for component in absolute.parts[1:]:
        current /= component
        if current.is_symlink():
            raise ValueError("calibration report path contains a symlink")
    if not absolute.is_file():
        raise ValueError("calibration report is not a regular file")
    if absolute.stat().st_size > MAX_REPORT_BYTES:
        raise ValueError("calibration report exceeds the size limit")
    try:
        payload = json.loads(absolute.read_bytes())
        return CalibrationReportV2.model_validate(payload)
    except (OSError, UnicodeDecodeError, json.JSONDecodeError, ValidationError) as exc:
        raise ValueError("calibration report is invalid") from exc


def create_report_from_labeled_corpus(
    corpus_path: Path,
    *,
    valid_days: int = 30,
    now: datetime | None = None,
) -> CalibrationReportV2:
    if not 1 <= valid_days <= 365:
        raise ValueError("calibration validity must be between 1 and 365 days")
    absolute = corpus_path.absolute()
    current_path = Path(absolute.anchor)
    for component in absolute.parts[1:]:
        current_path /= component
        if current_path.is_symlink():
            raise ValueError("calibration corpus path contains a symlink")
    if not absolute.is_file() or absolute.stat().st_size > MAX_CORPUS_BYTES:
        raise ValueError("calibration corpus is missing or exceeds the size limit")
    corpus_bytes = absolute.read_bytes()
    try:
        corpus = CalibrationCorpusV2.model_validate(json.loads(corpus_bytes))
    except (UnicodeDecodeError, json.JSONDecodeError, ValidationError) as exc:
        raise ValueError("calibration corpus is invalid") from exc
    if corpus.policy_version != "manifestation-v2" or corpus.schema_version not in CALIBRATION_SCHEMA_VERSIONS:
        raise ValueError("calibration corpus policy or schema version does not match")
    evaluated_at = now or datetime.now(UTC)
    tier_a = [item for item in corpus.observations if item.tier == "A"]
    false_positives = sum(not item.identity_correct for item in tier_a)
    false_auto_applies = sum(
        item.would_auto_apply and (not item.identity_correct or not item.patch_correct) for item in corpus.observations
    )
    timed = [item for item in corpus.observations if item.review_seconds is not None]
    return CalibrationReportV2(
        schema_version=CALIBRATION_SCHEMA_VERSION,
        corpus_sha256=hashlib.sha256(corpus_bytes).hexdigest(),
        sample_size=len(corpus.observations),
        tier_a_decisions=len(tier_a),
        false_positive_count=false_positives,
        false_auto_apply_count=false_auto_applies,
        observations_with_timing=len(timed),
        total_review_seconds=round(sum(item.review_seconds or 0.0 for item in timed), 3),
        total_egress_bytes=sum(item.egress_bytes or 0 for item in corpus.observations),
        evaluated_at=evaluated_at,
        expires_at=evaluated_at + timedelta(days=valid_days),
    ).seal()


def write_calibration_report(report: CalibrationReportV2, output_path: Path) -> None:
    if not report.verify_seal():
        raise ValueError("only a valid checksummed calibration report can be written")
    output = output_path.absolute()
    parent = output.parent
    if not parent.is_dir() or parent.is_symlink() or output.is_symlink():
        raise ValueError("calibration report output path is unsafe")
    payload = json.dumps(report.model_dump(mode="json"), indent=2, sort_keys=True).encode() + b"\n"
    temporary_name: str | None = None
    try:
        with tempfile.NamedTemporaryFile(prefix=".calibration-", dir=parent, delete=False) as stream:
            temporary_name = stream.name
            os.chmod(temporary_name, 0o600)
            stream.write(payload)
            stream.flush()
            os.fsync(stream.fileno())
        os.replace(temporary_name, output)
        directory_fd = os.open(parent, os.O_RDONLY)
        try:
            os.fsync(directory_fd)
        finally:
            os.close(directory_fd)
    finally:
        if temporary_name is not None:
            Path(temporary_name).unlink(missing_ok=True)


def calibration_gate_from_settings(
    settings: Settings,
    *,
    now: datetime | None = None,
) -> CalibrationGateDecision:
    """Validate advisory metrics; a valid result never authorizes a write."""
    gate = settings.manifestation_v2.auto_apply
    if not gate.enabled:
        return CalibrationGateDecision(
            valid=False,
            reasons=["Tier A auto-apply feature flag is disabled"],
        )
    if gate.calibration_report is None:
        return CalibrationGateDecision(valid=False, reasons=["calibration report path is not configured"])
    try:
        report = _read_report(gate.calibration_report)
    except ValueError as exc:
        return CalibrationGateDecision(valid=False, reasons=[str(exc)])
    if not report.verify_seal():
        return CalibrationGateDecision(valid=False, reasons=["calibration report seal is invalid"])

    current = now or datetime.now(UTC)
    reasons: list[str] = []
    if report.policy_version != "manifestation-v2" or report.schema_version not in CALIBRATION_SCHEMA_VERSIONS:
        reasons.append("calibration policy or schema version does not match")
    if report.sample_size < gate.min_sample_size:
        reasons.append("calibration sample is below the configured minimum")
    if report.tier_a_decisions < gate.min_tier_a_decisions:
        reasons.append("calibration has too few Tier A decisions")
    if report.false_auto_apply_count != 0:
        reasons.append("calibration contains at least one false auto-apply")
    false_positive_rate = report.false_positive_count / max(report.tier_a_decisions, 1)
    if false_positive_rate > gate.max_false_positive_rate:
        reasons.append("calibration false-positive rate exceeds the configured maximum")
    if report.evaluated_at.tzinfo is None or report.expires_at.tzinfo is None:
        reasons.append("calibration timestamps must be timezone-aware")
    else:
        if report.evaluated_at > current:
            reasons.append("calibration evaluation timestamp is in the future")
        if report.expires_at <= current:
            reasons.append("calibration report is expired")
        if current - report.evaluated_at > timedelta(days=gate.max_report_age_days):
            reasons.append("calibration report is older than the configured maximum")
    return CalibrationGateDecision(
        valid=not reasons,
        report_sha256=report.report_sha256,
        reasons=reasons,
    )

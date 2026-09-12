"""Versioned, deterministic identity contracts for library-wide verification.

The current Calibre record is deliberately absent from the sources that may
promote a decision.  It is the value being audited, not corroborating evidence.
LLM and vision observations are also non-authoritative: they may add review
context, but never satisfy a Tier A requirement.
"""

from __future__ import annotations

import json
import re
import unicodedata
from enum import StrEnum
from typing import Any

from pydantic import BaseModel, ConfigDict, Field, field_validator

SHA256_RE = re.compile(r"^[0-9a-f]{64}$")
ALLOWED_PATCH_FIELDS = frozenset(
    {
        "title",
        "authors",
        "identifiers",
        "languages",
        "publisher",
        "pubdate",
        "series",
        "series_index",
        "edition_statement",
        "cover",
    }
)


class IdentityTier(StrEnum):
    tier_a = "A"
    tier_b = "B"
    tier_c = "C"


class EvidenceSourceKind(StrEnum):
    calibre_current = "calibre_current"
    embedded_metadata = "embedded_metadata"
    content_native = "content_native"
    ocr_observation = "ocr_observation"
    ocr_consensus = "ocr_consensus"
    vision = "vision"
    provider_structured = "provider_structured"
    official_page = "official_page"
    national_library = "national_library"
    calibre_fetch = "calibre_fetch"
    llm = "llm"


class FormatEvidenceStatus(StrEnum):
    readable = "readable"
    unsupported = "unsupported"
    drm = "drm"
    corrupt = "corrupt"
    error = "error"


class FieldDecisionStatus(StrEnum):
    unchanged = "unchanged"
    auto = "auto"
    review = "review"
    blocked = "blocked"


INTERNAL_PROMOTION_KINDS = frozenset(
    {
        EvidenceSourceKind.embedded_metadata,
        EvidenceSourceKind.content_native,
        EvidenceSourceKind.ocr_consensus,
    }
)
INTERNAL_IDENTITY_KINDS = frozenset(
    {
        EvidenceSourceKind.content_native,
        EvidenceSourceKind.ocr_consensus,
    }
)
EXTERNAL_PROMOTION_KINDS = frozenset(
    {
        EvidenceSourceKind.provider_structured,
        EvidenceSourceKind.official_page,
        EvidenceSourceKind.national_library,
    }
)
OFFICIAL_KINDS = frozenset(
    {
        EvidenceSourceKind.official_page,
        EvidenceSourceKind.national_library,
    }
)


def validate_isbn(value: str) -> str | None:
    """Return a canonical ISBN-13 only when the supplied checksum is valid."""
    from calibre_ai_auditor.rules.isbn import canonical_isbn13

    return canonical_isbn13(value)


def _normalize_identifiers(value: dict[str, str]) -> dict[str, str]:
    normalized: dict[str, str] = {}
    for raw_namespace, raw_value in value.items():
        namespace = raw_namespace.strip().lower()
        candidate = str(raw_value).strip()
        if namespace == "isbn":
            valid = validate_isbn(candidate)
            if valid:
                normalized[namespace] = valid
        elif namespace and candidate:
            normalized[namespace] = candidate
    return normalized


class FormatEvidence(BaseModel):
    model_config = ConfigDict(extra="forbid")

    path: str = Field(min_length=1)
    format: str = Field(min_length=1)
    sha256: str
    status: FormatEvidenceStatus
    identifiers: dict[str, str] = Field(default_factory=dict)
    title: str | None = None
    authors: list[str] = Field(default_factory=list)
    languages: list[str] = Field(default_factory=list)
    evidence_ids: list[str] = Field(default_factory=list)
    error: str | None = None

    @field_validator("sha256")
    @classmethod
    def _valid_sha256(cls, value: str) -> str:
        lowered = value.lower()
        if not SHA256_RE.fullmatch(lowered):
            raise ValueError("sha256 must contain exactly 64 hexadecimal characters")
        return lowered

    @field_validator("format")
    @classmethod
    def _canonical_format(cls, value: str) -> str:
        return value.strip().upper()

    @field_validator("identifiers")
    @classmethod
    def _canonical_identifiers(cls, value: dict[str, str]) -> dict[str, str]:
        return _normalize_identifiers(value)


class SourceEvidence(BaseModel):
    model_config = ConfigDict(extra="forbid")

    evidence_id: str = Field(min_length=1)
    root_id: str = Field(min_length=1)
    independence_root: str | None = None
    source_kind: EvidenceSourceKind
    field: str = Field(min_length=1)
    value: Any
    manifestation_ids: dict[str, str] = Field(default_factory=dict)
    locator: str | None = None
    artifact_sha256: str | None = None
    source_url: str | None = None
    authoritative: bool = False

    @property
    def effective_root(self) -> str:
        return self.independence_root or self.root_id

    @field_validator("artifact_sha256")
    @classmethod
    def _valid_optional_sha256(cls, value: str | None) -> str | None:
        if value is None:
            return None
        lowered = value.lower()
        if not SHA256_RE.fullmatch(lowered):
            raise ValueError("artifact_sha256 must contain exactly 64 hexadecimal characters")
        return lowered

    @field_validator("manifestation_ids")
    @classmethod
    def _canonical_manifestation_ids(cls, value: dict[str, str]) -> dict[str, str]:
        return _normalize_identifiers(value)


class FieldDecision(BaseModel):
    model_config = ConfigDict(extra="forbid")

    field: str
    current_value: Any = None
    resolved_value: Any = None
    status: FieldDecisionStatus
    evidence_ids: list[str] = Field(default_factory=list)
    root_ids: list[str] = Field(default_factory=list)
    reasons: list[str] = Field(default_factory=list)


class CoverPatch(BaseModel):
    """A locally materialized cover bound to the exact manifestation."""

    model_config = ConfigDict(extra="forbid")

    artifact_path: str = Field(min_length=1)
    artifact_sha256: str
    manifestation_isbn: str

    @field_validator("artifact_sha256")
    @classmethod
    def _valid_artifact_sha256(cls, value: str) -> str:
        lowered = value.lower()
        if not SHA256_RE.fullmatch(lowered):
            raise ValueError("artifact_sha256 must contain exactly 64 hexadecimal characters")
        return lowered

    @field_validator("manifestation_isbn")
    @classmethod
    def _valid_manifestation_isbn(cls, value: str) -> str:
        canonical = validate_isbn(value)
        if canonical is None:
            raise ValueError("manifestation_isbn must be checksum-valid")
        return canonical


class CanonicalPatch(BaseModel):
    """Only fields with explicit Calibre adapters may cross the writer boundary."""

    model_config = ConfigDict(extra="forbid")

    title: str | None = None
    authors: list[str] | None = None
    identifiers: dict[str, str] | None = None
    languages: list[str] | None = None
    publisher: str | None = None
    pubdate: str | None = None
    series: str | None = None
    series_index: float | None = None
    edition_statement: str | None = None
    cover: CoverPatch | None = None

    @field_validator("identifiers")
    @classmethod
    def _canonical_patch_identifiers(cls, value: dict[str, str] | None) -> dict[str, str] | None:
        if value is None:
            return None
        normalized = _normalize_identifiers(value)
        if value and not normalized:
            raise ValueError("identifiers contains no valid namespaced values")
        return normalized


class ManifestationResolution(BaseModel):
    model_config = ConfigDict(extra="forbid")

    tier: IdentityTier
    manifestation_ids: dict[str, str] = Field(default_factory=dict)
    field_decisions: dict[str, FieldDecision] = Field(default_factory=dict)
    auto_patch: dict[str, Any] = Field(default_factory=dict)
    risk_flags: list[str] = Field(default_factory=list)
    reasons: list[str] = Field(default_factory=list)


def _text_key(value: str) -> str:
    normalized = unicodedata.normalize("NFKC", value).casefold()
    return " ".join(re.findall(r"\w+", normalized, flags=re.UNICODE))


def _value_key(field: str, value: Any) -> str:
    if field in {"title", "publisher", "series", "edition_statement"} and isinstance(value, str):
        return _text_key(value)
    if field == "authors" and isinstance(value, (list, str)):
        authors = value if isinstance(value, list) else value.split("&")
        normalized = sorted(_text_key(str(item)) for item in authors if str(item).strip())
        return json.dumps(normalized, separators=(",", ":"))
    if field == "languages" and isinstance(value, (list, str)):
        languages = value if isinstance(value, list) else value.replace(";", ",").split(",")
        normalized = sorted(str(item).strip().lower() for item in languages if str(item).strip())
        return json.dumps(normalized, separators=(",", ":"))
    if field == "identifiers" and isinstance(value, dict):
        value = _normalize_identifiers({str(key): str(item) for key, item in value.items()})
    return json.dumps(value, sort_keys=True, ensure_ascii=True, separators=(",", ":"), default=str)


def _non_authoritative_resolution(
    tier: IdentityTier,
    *,
    manifestation_ids: dict[str, str],
    risk_flag: str,
    reason: str,
    evidence: list[SourceEvidence],
    current_metadata: dict[str, Any],
) -> ManifestationResolution:
    status = FieldDecisionStatus.blocked if tier is IdentityTier.tier_c else FieldDecisionStatus.review
    decisions = {
        field: FieldDecision(
            field=field,
            current_value=current_metadata.get(field),
            status=status,
            evidence_ids=[item.evidence_id for item in evidence if item.field == field],
            reasons=[reason],
        )
        for field in sorted({item.field for item in evidence if item.field in ALLOWED_PATCH_FIELDS})
    }
    return ManifestationResolution(
        tier=tier,
        manifestation_ids=manifestation_ids,
        field_decisions=decisions,
        risk_flags=[risk_flag],
        reasons=[reason],
    )


def _core_value_present(field: str, value: Any) -> bool:
    if field == "title":
        return isinstance(value, str) and bool(value.strip())
    return isinstance(value, list) and bool(value)


def _formats_have_core_conflict(formats: list[FormatEvidence]) -> bool:
    for field in ("title", "authors", "languages"):
        values = [getattr(item, field) for item in formats]
        keys = {_value_key(field, value) for value in values if _core_value_present(field, value)}
        if len(keys) > 1:
            return True
    return False


def _formats_have_complete_core(formats: list[FormatEvidence]) -> bool:
    return all(
        _core_value_present(field, getattr(item, field))
        for item in formats
        for field in ("title", "authors", "languages")
    )


def _external_core_matches(
    formats: list[FormatEvidence],
    evidence: list[SourceEvidence],
    namespace: str,
    identifier: str,
) -> bool:
    expected = {
        "title": _value_key("title", formats[0].title),
        "authors": _value_key("authors", formats[0].authors),
        "languages": _value_key("languages", formats[0].languages),
    }
    roots = {
        item.effective_root
        for item in evidence
        if item.authoritative
        and item.source_kind in EXTERNAL_PROMOTION_KINDS
        and item.manifestation_ids.get(namespace) == identifier
        and item.field == "identifiers"
    }
    for root_id in roots:
        fields = {
            item.field: _value_key(item.field, item.value)
            for item in evidence
            if item.effective_root == root_id and item.field in expected and item.authoritative
        }
        if fields == expected:
            return True
    return False


def _external_core_conflicts(
    formats: list[FormatEvidence],
    evidence: list[SourceEvidence],
    namespace: str,
    identifier: str,
) -> bool:
    """Detect an explicit core-field conflict from any exact-ID external root."""
    expected = {
        "title": _value_key("title", formats[0].title),
        "authors": _value_key("authors", formats[0].authors),
        "languages": _value_key("languages", formats[0].languages),
    }
    roots = {
        item.effective_root
        for item in evidence
        if item.authoritative
        and item.source_kind in EXTERNAL_PROMOTION_KINDS
        and item.manifestation_ids.get(namespace) == identifier
        and item.field == "identifiers"
    }
    return any(
        _value_key(item.field, item.value) != expected[item.field]
        for item in evidence
        if item.authoritative
        and item.source_kind in EXTERNAL_PROMOTION_KINDS
        and item.effective_root in roots
        and item.field in expected
    )


def _resolve_fields(
    *,
    evidence: list[SourceEvidence],
    current_metadata: dict[str, Any],
    manifestation_ids: dict[str, str],
) -> tuple[dict[str, FieldDecision], dict[str, Any]]:
    decisions: dict[str, FieldDecision] = {}
    patch: dict[str, Any] = {}
    target_id = next(iter(manifestation_ids.items()))
    fields = sorted({item.field for item in evidence if item.field in ALLOWED_PATCH_FIELDS})
    for field in fields:
        eligible = [
            item
            for item in evidence
            if item.field == field
            and item.authoritative
            and item.source_kind in INTERNAL_PROMOTION_KINDS | EXTERNAL_PROMOTION_KINDS
            and item.manifestation_ids.get(target_id[0]) == target_id[1]
        ]
        by_value: dict[str, list[SourceEvidence]] = {}
        for item in eligible:
            by_value.setdefault(_value_key(field, item.value), []).append(item)
        if len(by_value) != 1:
            decisions[field] = FieldDecision(
                field=field,
                current_value=current_metadata.get(field),
                status=FieldDecisionStatus.review,
                evidence_ids=[item.evidence_id for item in eligible],
                root_ids=sorted({item.root_id for item in eligible}),
                reasons=["independent evidence is missing or conflicting"],
            )
            continue
        supporting = next(iter(by_value.values()))
        roots = {item.effective_root for item in supporting}
        has_preferred_root = any(item.source_kind in INTERNAL_PROMOTION_KINDS | OFFICIAL_KINDS for item in supporting)
        resolved = supporting[0].value
        target_value = resolved
        if field == "identifiers" and isinstance(resolved, dict):
            current_identifiers = (
                _normalize_identifiers(
                    {str(namespace): str(value) for namespace, value in current_metadata.get(field, {}).items()}
                )
                if isinstance(current_metadata.get(field), dict)
                else {}
            )
            resolved_identifiers = _normalize_identifiers(
                {str(namespace): str(value) for namespace, value in resolved.items()}
            )
            # Evidence can authorize changing only the namespaces it observes.
            # Preserve unrelated Calibre identifiers instead of deleting them.
            target_value = {**current_identifiers, **resolved_identifiers}
        if len(roots) < 2 or not has_preferred_root:
            status = FieldDecisionStatus.review
            reasons = ["field lacks two independent roots including internal or official evidence"]
        elif _value_key(field, current_metadata.get(field)) == _value_key(field, target_value):
            status = FieldDecisionStatus.unchanged
            reasons = ["current Calibre value matches the evidence consensus"]
        else:
            status = FieldDecisionStatus.auto
            reasons = ["two independent roots support the exact manifestation value"]
            patch[field] = target_value
        decisions[field] = FieldDecision(
            field=field,
            current_value=current_metadata.get(field),
            resolved_value=target_value,
            status=status,
            evidence_ids=[item.evidence_id for item in supporting],
            root_ids=sorted(roots),
            reasons=reasons,
        )
    canonical = CanonicalPatch.model_validate(patch).model_dump(exclude_none=True)
    return decisions, canonical


def resolve_manifestation(
    *,
    formats: list[FormatEvidence],
    evidence: list[SourceEvidence],
    current_metadata: dict[str, Any],
) -> ManifestationResolution:
    """Resolve an exact manifestation without probabilistic promotion."""
    readable = [item for item in formats if item.status is FormatEvidenceStatus.readable]
    format_ids = {
        (namespace, value)
        for item in readable
        for namespace, value in item.identifiers.items()
        if namespace == "isbn" and validate_isbn(value)
    }
    if len(format_ids) > 1:
        return _non_authoritative_resolution(
            IdentityTier.tier_c,
            manifestation_ids={},
            risk_flag="format_manifestation_conflict",
            reason="readable formats identify different manifestations",
            evidence=evidence,
            current_metadata=current_metadata,
        )
    if len(readable) != len(formats):
        return _non_authoritative_resolution(
            IdentityTier.tier_b,
            manifestation_ids=dict(format_ids),
            risk_flag="incomplete_format_evidence",
            reason="at least one format is unsupported, protected, corrupt, or unreadable",
            evidence=evidence,
            current_metadata=current_metadata,
        )
    if not format_ids:
        return _non_authoritative_resolution(
            IdentityTier.tier_b,
            manifestation_ids={},
            risk_flag="missing_manifestation_identifier",
            reason="no valid manifestation identifier was found in edition-bearing content",
            evidence=evidence,
            current_metadata=current_metadata,
        )
    namespace, identifier = next(iter(format_ids))
    manifestation_ids = {namespace: identifier}
    internal_confirmation = any(
        item.field == "identifiers"
        and item.authoritative
        and item.source_kind in INTERNAL_IDENTITY_KINDS
        and item.manifestation_ids.get(namespace) == identifier
        and isinstance(item.value, dict)
        and _normalize_identifiers({str(key): str(value) for key, value in item.value.items()}).get(namespace)
        == identifier
        for item in evidence
    )
    if not internal_confirmation:
        return _non_authoritative_resolution(
            IdentityTier.tier_b,
            manifestation_ids=manifestation_ids,
            risk_flag="missing_internal_manifestation_confirmation",
            reason="the identifier is not anchored to internal edition-bearing evidence",
            evidence=evidence,
            current_metadata=current_metadata,
        )
    external_confirmation = any(
        item.field == "identifiers"
        and item.authoritative
        and item.source_kind in EXTERNAL_PROMOTION_KINDS
        and item.manifestation_ids.get(namespace) == identifier
        for item in evidence
    )
    if not external_confirmation:
        return _non_authoritative_resolution(
            IdentityTier.tier_b,
            manifestation_ids=manifestation_ids,
            risk_flag="missing_external_manifestation_confirmation",
            reason="no structured external root confirms the internal manifestation identifier",
            evidence=evidence,
            current_metadata=current_metadata,
        )
    if _formats_have_core_conflict(readable):
        return _non_authoritative_resolution(
            IdentityTier.tier_c,
            manifestation_ids=manifestation_ids,
            risk_flag="format_expression_conflict",
            reason="readable formats disagree on title, authors, or language",
            evidence=evidence,
            current_metadata=current_metadata,
        )
    if not _formats_have_complete_core(readable):
        return _non_authoritative_resolution(
            IdentityTier.tier_b,
            manifestation_ids=manifestation_ids,
            risk_flag="incomplete_internal_core_evidence",
            reason="title, authors, or language are missing from at least one readable format",
            evidence=evidence,
            current_metadata=current_metadata,
        )
    if _external_core_conflicts(readable, evidence, namespace, identifier):
        return _non_authoritative_resolution(
            IdentityTier.tier_c,
            manifestation_ids=manifestation_ids,
            risk_flag="external_expression_conflict",
            reason="an exact-identifier external root conflicts on title, authors, or language",
            evidence=evidence,
            current_metadata=current_metadata,
        )
    if not _external_core_matches(readable, evidence, namespace, identifier):
        return _non_authoritative_resolution(
            IdentityTier.tier_b,
            manifestation_ids=manifestation_ids,
            risk_flag="missing_external_core_confirmation",
            reason="no single structured root confirms identifier, title, authors, and language",
            evidence=evidence,
            current_metadata=current_metadata,
        )
    decisions, patch = _resolve_fields(
        evidence=evidence,
        current_metadata=current_metadata,
        manifestation_ids=manifestation_ids,
    )
    return ManifestationResolution(
        tier=IdentityTier.tier_a,
        manifestation_ids=manifestation_ids,
        field_decisions=decisions,
        auto_patch=patch,
        reasons=["internal content and a structured external root identify the same manifestation"],
    )

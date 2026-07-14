from __future__ import annotations

from datetime import UTC, datetime

import pytest
from pydantic import ValidationError

from calibre_ai_auditor.verification.identity_v2 import (
    CanonicalPatch,
    EvidenceSourceKind,
    FieldDecisionStatus,
    FormatEvidence,
    FormatEvidenceStatus,
    IdentityTier,
    ManifestationResolution,
    SourceEvidence,
    resolve_manifestation,
    validate_isbn,
)
from calibre_ai_auditor.verification.pipeline_v2 import (
    BookAuditState,
    BookSnapshot,
    EvidencePackageV2,
)

ISBN = "9780306406157"
OTHER_ISBN = "9783161484100"
SHA = "a" * 64


def _format(
    name: str,
    *,
    isbn: str | None = ISBN,
    status: FormatEvidenceStatus = FormatEvidenceStatus.readable,
) -> FormatEvidence:
    return FormatEvidence(
        path=f"/library/Example ({name})/book.{name.lower()}",
        format=name,
        sha256=SHA,
        status=status,
        identifiers={"isbn": isbn} if isbn else {},
        title="The Example Book",
        authors=["Ada Author"],
        languages=["eng"],
    )


def _evidence(
    evidence_id: str,
    root_id: str,
    field: str,
    value: object,
    *,
    kind: EvidenceSourceKind,
    isbn: str = ISBN,
    authoritative: bool = True,
) -> SourceEvidence:
    return SourceEvidence(
        evidence_id=evidence_id,
        root_id=root_id,
        source_kind=kind,
        field=field,
        value=value,
        manifestation_ids={"isbn": isbn},
        artifact_sha256=SHA,
        authoritative=authoritative,
    )


def _exact_manifestation_evidence() -> list[SourceEvidence]:
    return [
        _evidence(
            "internal-id",
            "content:epub",
            "identifiers",
            {"isbn": ISBN},
            kind=EvidenceSourceKind.content_native,
        ),
        _evidence(
            "internal-title",
            "content:epub",
            "title",
            "The Example Book",
            kind=EvidenceSourceKind.content_native,
        ),
        _evidence(
            "internal-authors",
            "content:epub",
            "authors",
            ["Ada Author"],
            kind=EvidenceSourceKind.content_native,
        ),
        _evidence(
            "internal-language",
            "content:epub",
            "languages",
            ["eng"],
            kind=EvidenceSourceKind.content_native,
        ),
        _evidence(
            "provider-id",
            "google-books:volume-1",
            "identifiers",
            {"isbn": ISBN},
            kind=EvidenceSourceKind.provider_structured,
        ),
        _evidence(
            "provider-title",
            "google-books:volume-1",
            "title",
            "The Example Book",
            kind=EvidenceSourceKind.provider_structured,
        ),
        _evidence(
            "provider-authors",
            "google-books:volume-1",
            "authors",
            ["Ada Author"],
            kind=EvidenceSourceKind.provider_structured,
        ),
        _evidence(
            "provider-language",
            "google-books:volume-1",
            "languages",
            ["eng"],
            kind=EvidenceSourceKind.provider_structured,
        ),
    ]


def test_seal_rejects_fabricated_tier_a_from_one_ocr_source() -> None:
    path = "/library/book.epub"
    snapshot = BookSnapshot(
        book_key="calibre:1",
        calibre_book_id=1,
        current_metadata={"title": "Wrong title"},
        files=[path],
        library_root="/library",
        snapshot_sha256="0" * 64,
    )
    snapshot = snapshot.model_copy(update={"snapshot_sha256": snapshot.calculated_sha256()})
    package = EvidencePackageV2(
        evidence_id="fabricated-tier-a",
        run_id="run-fabricated",
        book_key="calibre:1",
        created_at=datetime.now(UTC),
        state=BookAuditState.shadowed,
        snapshot=snapshot,
        formats=[
            FormatEvidence(
                path=path,
                format="EPUB",
                sha256=SHA,
                status=FormatEvidenceStatus.readable,
                identifiers={"isbn": ISBN},
                title="The Example Book",
                authors=["Ada Author"],
                languages=["eng"],
            )
        ],
        source_evidence=[
            _evidence(
                "single-ocr",
                "ocr-engine",
                "identifiers",
                {"isbn": ISBN},
                kind=EvidenceSourceKind.ocr_consensus,
            )
        ],
        identity=ManifestationResolution(
            tier=IdentityTier.tier_a,
            manifestation_ids={"isbn": ISBN},
            auto_patch={"title": "The Example Book"},
        ),
    )

    with pytest.raises(ValueError, match="invariants"):
        package.seal()


@pytest.mark.parametrize(
    ("value", "expected"),
    [
        ("978-0-306-40615-7", "9780306406157"),
        ("0-306-40615-2", "9780306406157"),
        ("9780306406158", None),
        ("not-an-isbn", None),
    ],
)
def test_validate_isbn_normalizes_only_checksum_valid_values(value: str, expected: str | None) -> None:
    assert validate_isbn(value) == expected


def test_exact_internal_identifier_and_structured_source_yield_tier_a() -> None:
    result = resolve_manifestation(
        formats=[_format("EPUB"), _format("PDF")],
        evidence=_exact_manifestation_evidence(),
        current_metadata={"title": "Wrong title", "publisher": None},
    )

    assert result.tier is IdentityTier.tier_a
    assert result.manifestation_ids == {"isbn": ISBN}
    assert result.field_decisions["title"].status is FieldDecisionStatus.auto
    assert result.field_decisions["title"].resolved_value == "The Example Book"


def test_embedded_identifier_alone_cannot_anchor_exact_manifestation() -> None:
    evidence = _exact_manifestation_evidence()
    evidence[0] = evidence[0].model_copy(update={"source_kind": EvidenceSourceKind.embedded_metadata})

    result = resolve_manifestation(
        formats=[_format("EPUB")],
        evidence=evidence,
        current_metadata={"title": "Wrong title"},
    )

    assert result.tier is IdentityTier.tier_b
    assert result.auto_patch == {}
    assert "missing_internal_manifestation_confirmation" in result.risk_flags


def test_identifier_correction_preserves_unobserved_namespaces() -> None:
    result = resolve_manifestation(
        formats=[_format("EPUB")],
        evidence=_exact_manifestation_evidence(),
        current_metadata={
            "title": "The Example Book",
            "identifiers": {"isbn": OTHER_ISBN, "asin": "B012345678"},
        },
    )

    assert result.tier is IdentityTier.tier_a
    assert result.auto_patch["identifiers"] == {
        "asin": "B012345678",
        "isbn": ISBN,
    }


def test_missing_manifestation_identifier_never_yields_tier_a() -> None:
    result = resolve_manifestation(
        formats=[_format("EPUB", isbn=None)],
        evidence=[
            _evidence(
                "title",
                "content:epub",
                "title",
                "The Example Book",
                kind=EvidenceSourceKind.content_native,
            )
        ],
        current_metadata={"title": "Wrong title"},
    )

    assert result.tier is IdentityTier.tier_b
    assert result.auto_patch == {}
    assert "missing_manifestation_identifier" in result.risk_flags


def test_conflicting_format_identifiers_block_the_entire_book() -> None:
    result = resolve_manifestation(
        formats=[_format("EPUB"), _format("PDF", isbn=OTHER_ISBN)],
        evidence=_exact_manifestation_evidence(),
        current_metadata={"title": "Wrong title"},
    )

    assert result.tier is IdentityTier.tier_c
    assert result.auto_patch == {}
    assert "format_manifestation_conflict" in result.risk_flags


def test_unreadable_format_prevents_tier_a_without_being_silent() -> None:
    result = resolve_manifestation(
        formats=[_format("EPUB"), _format("PDF", status=FormatEvidenceStatus.drm)],
        evidence=_exact_manifestation_evidence(),
        current_metadata={"title": "Wrong title"},
    )

    assert result.tier is IdentityTier.tier_b
    assert result.auto_patch == {}
    assert "incomplete_format_evidence" in result.risk_flags


def test_missing_internal_core_field_is_reviewable_uncertainty_not_a_conflict() -> None:
    incomplete = _format("PDF")
    incomplete.languages = []

    result = resolve_manifestation(
        formats=[incomplete],
        evidence=_exact_manifestation_evidence(),
        current_metadata={"title": "Wrong title"},
    )

    assert result.tier is IdentityTier.tier_b
    assert result.auto_patch == {}
    assert "incomplete_internal_core_evidence" in result.risk_flags


def test_duplicate_provider_root_does_not_create_field_consensus() -> None:
    evidence = _exact_manifestation_evidence()
    evidence.extend(
        [
            _evidence(
                "publisher-a",
                "google-books:volume-1",
                "publisher",
                "Correct Press",
                kind=EvidenceSourceKind.provider_structured,
            ),
            _evidence(
                "publisher-b",
                "google-books:volume-1",
                "publisher",
                "Correct Press",
                kind=EvidenceSourceKind.provider_structured,
            ),
        ]
    )

    result = resolve_manifestation(
        formats=[_format("EPUB")],
        evidence=evidence,
        current_metadata={"title": "The Example Book", "publisher": "Wrong Press"},
    )

    assert result.tier is IdentityTier.tier_a
    assert result.field_decisions["publisher"].status is FieldDecisionStatus.review
    assert "publisher" not in result.auto_patch


def test_llm_output_cannot_supply_the_external_identity_root() -> None:
    evidence = [item for item in _exact_manifestation_evidence() if not item.root_id.startswith("google-books")]
    evidence.append(
        _evidence(
            "llm-id",
            "llm:response-1",
            "identifiers",
            {"isbn": ISBN},
            kind=EvidenceSourceKind.llm,
        )
    )

    result = resolve_manifestation(
        formats=[_format("EPUB")],
        evidence=evidence,
        current_metadata={"title": "Wrong title"},
    )

    assert result.tier is IdentityTier.tier_b
    assert result.auto_patch == {}
    assert "missing_external_manifestation_confirmation" in result.risk_flags


def test_conflicting_exact_external_record_blocks_tier_a() -> None:
    evidence = _exact_manifestation_evidence()
    evidence.extend(
        [
            _evidence(
                "other-provider-id",
                "national-library:record-2",
                "identifiers",
                {"isbn": ISBN},
                kind=EvidenceSourceKind.national_library,
            ),
            _evidence(
                "other-provider-title",
                "national-library:record-2",
                "title",
                "A Conflicting Title",
                kind=EvidenceSourceKind.national_library,
            ),
        ]
    )

    result = resolve_manifestation(
        formats=[_format("EPUB")],
        evidence=evidence,
        current_metadata={"title": "Wrong title"},
    )

    assert result.tier is IdentityTier.tier_c
    assert result.auto_patch == {}
    assert "external_expression_conflict" in result.risk_flags


def test_canonical_patch_rejects_legacy_aliases_and_forbidden_fields() -> None:
    with pytest.raises(ValidationError):
        CanonicalPatch.model_validate({"isbn": ISBN})
    with pytest.raises(ValidationError):
        CanonicalPatch.model_validate({"tags": ["fiction"]})
    with pytest.raises(ValidationError):
        CanonicalPatch.model_validate(
            {
                "cover": {
                    "source_url": "https://example.test/cover.jpg",
                    "manifestation_isbn": ISBN,
                }
            }
        )

    patch = CanonicalPatch.model_validate({"identifiers": {"isbn": ISBN}, "languages": ["eng"]})
    assert patch.model_dump(exclude_none=True) == {
        "identifiers": {"isbn": ISBN},
        "languages": ["eng"],
    }

    cover_patch = CanonicalPatch.model_validate(
        {
            "cover": {
                "artifact_path": "/artifacts/covers/exact.jpg",
                "artifact_sha256": SHA,
                "manifestation_isbn": ISBN,
            }
        }
    )
    assert cover_patch.cover is not None
    assert cover_patch.cover.manifestation_isbn == ISBN

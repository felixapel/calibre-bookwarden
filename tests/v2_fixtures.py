"""Valid exact-manifestation fixtures derived through the production resolver."""

from __future__ import annotations

from datetime import UTC, datetime
from pathlib import Path
from typing import Any

from calibre_ai_auditor.verification.identity_v2 import (
    EvidenceSourceKind,
    FormatEvidence,
    FormatEvidenceStatus,
    IdentityTier,
    SourceEvidence,
    resolve_manifestation,
)
from calibre_ai_auditor.verification.pipeline_v2 import (
    BookAuditState,
    BookSnapshot,
    BookSourceDescriptor,
    EvidencePackageV2,
)

ISBN = "9780306406157"


def build_exact_tier_a_package(
    *,
    evidence_id: str,
    run_id: str,
    book_id: int,
    library_root: str,
    files: list[str],
    current_metadata: dict[str, Any],
    resolved_patch: dict[str, Any],
    file_sha256: str | dict[str, str] = "a" * 64,
    created_at: datetime | None = None,
    state: BookAuditState = BookAuditState.shadowed,
) -> EvidencePackageV2:
    """Build a Tier A package only by satisfying the real deterministic resolver."""
    if not files:
        raise ValueError("an exact-manifestation fixture requires at least one format")
    fixture_current = dict(current_metadata)
    current_identifiers = dict(fixture_current.get("identifiers") or {})
    current_identifiers.setdefault("isbn", ISBN)
    fixture_current["identifiers"] = current_identifiers
    target_title = str(resolved_patch.get("title") or fixture_current.get("title") or "Exact Book")
    target_authors = list(resolved_patch.get("authors") or fixture_current.get("authors") or ["Exact Author"])
    target_languages = list(resolved_patch.get("languages") or fixture_current.get("languages") or ["eng"])
    content_root = f"content:{evidence_id}"
    provider_root = f"provider:{evidence_id}"

    def source(
        suffix: str,
        root_id: str,
        field: str,
        value: Any,
        kind: EvidenceSourceKind,
    ) -> SourceEvidence:
        return SourceEvidence(
            evidence_id=f"{evidence_id}:{suffix}",
            root_id=root_id,
            source_kind=kind,
            field=field,
            value=value,
            manifestation_ids={"isbn": ISBN},
            authoritative=True,
        )

    sources = [
        source(
            "internal-id",
            content_root,
            "identifiers",
            {"isbn": ISBN},
            EvidenceSourceKind.content_native,
        ),
        source(
            "provider-id",
            provider_root,
            "identifiers",
            {"isbn": ISBN},
            EvidenceSourceKind.provider_structured,
        ),
        source(
            "provider-title",
            provider_root,
            "title",
            target_title,
            EvidenceSourceKind.provider_structured,
        ),
        source(
            "provider-authors",
            provider_root,
            "authors",
            target_authors,
            EvidenceSourceKind.provider_structured,
        ),
        source(
            "provider-languages",
            provider_root,
            "languages",
            target_languages,
            EvidenceSourceKind.provider_structured,
        ),
    ]
    external_core_fields = {"title", "authors", "languages"}
    for field, value in resolved_patch.items():
        if field == "identifiers":
            continue
        sources.append(
            source(
                f"internal-{field}",
                content_root,
                field,
                value,
                EvidenceSourceKind.content_native,
            )
        )
        if field not in external_core_fields:
            sources.append(
                source(
                    f"provider-{field}",
                    provider_root,
                    field,
                    value,
                    EvidenceSourceKind.provider_structured,
                )
            )

    formats = [
        FormatEvidence(
            path=path,
            format=Path(path).suffix.removeprefix(".") or "EPUB",
            sha256=file_sha256[path] if isinstance(file_sha256, dict) else file_sha256,
            status=FormatEvidenceStatus.readable,
            identifiers={"isbn": ISBN},
            title=target_title,
            authors=target_authors,
            languages=target_languages,
            evidence_ids=[item.evidence_id for item in sources if item.root_id == content_root],
        )
        for path in files
    ]
    identity = resolve_manifestation(
        formats=formats,
        evidence=sources,
        current_metadata=fixture_current,
    )
    if identity.tier is not IdentityTier.tier_a or identity.auto_patch != resolved_patch:
        raise ValueError("fixture evidence did not derive the requested exact Tier A patch")

    snapshot = BookSnapshot(
        book_key=f"calibre:{book_id}",
        calibre_book_id=book_id,
        current_metadata=fixture_current,
        files=files,
        library_root=library_root,
        snapshot_sha256="0" * 64,
    )
    snapshot = snapshot.model_copy(update={"snapshot_sha256": snapshot.calculated_sha256()})
    return EvidencePackageV2(
        evidence_id=evidence_id,
        run_id=run_id,
        book_key=snapshot.book_key,
        created_at=created_at or datetime.now(UTC),
        state=state,
        snapshot=snapshot,
        formats=formats,
        source_evidence=sources,
        identity=identity,
    ).seal()


def as_remote_package(
    package: EvidencePackageV2,
    *,
    fingerprint: str = "c" * 64,
) -> EvidencePackageV2:
    """Rebind a valid fixture to logical Content Server references."""
    book_key = f"calibre-server:{fingerprint}:{package.snapshot.calibre_book_id}"
    references = [f"{book_key}:{item.format.upper()}" for item in package.formats]
    formats = [
        item.model_copy(update={"path": reference}) for item, reference in zip(package.formats, references, strict=True)
    ]
    snapshot = package.snapshot.model_copy(
        update={
            "book_key": book_key,
            "files": references,
            "library_root": None,
            "source": BookSourceDescriptor(
                kind="calibre_content_server",
                fingerprint=fingerprint,
            ),
            "source_revision_sha256": "e" * 64,
            "snapshot_sha256": "0" * 64,
        }
    )
    snapshot = snapshot.model_copy(update={"snapshot_sha256": snapshot.calculated_sha256()})
    return package.model_copy(
        update={
            "book_key": book_key,
            "snapshot": snapshot,
            "formats": formats,
            "package_sha256": None,
        }
    ).seal()

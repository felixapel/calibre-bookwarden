from __future__ import annotations

from pathlib import Path
from typing import Any

import pytest

from calibre_ai_auditor.extractors.multiformat import FormatInspection
from calibre_ai_auditor.verification.identity_v2 import (
    EvidenceSourceKind,
    FormatEvidence,
    FormatEvidenceStatus,
    IdentityTier,
    SourceEvidence,
)
from calibre_ai_auditor.verification.pipeline_v2 import (
    AuditMode,
    BookAuditState,
    BookSnapshot,
    EvidenceEnrichment,
    LibraryAuditPipeline,
    LibraryRunStatus,
)

ISBN = "9780306406157"
SHA = "a" * 64


class _FakeCalibre:
    def __init__(self, files: dict[int, list[Path]]) -> None:
        self.files = files

    def list_books(self) -> list[dict[str, Any]]:
        return [
            {"id": book_id, "title": f"Wrong {book_id}", "formats": [str(path) for path in paths]}
            for book_id, paths in sorted(self.files.items(), reverse=True)
        ]

    def show_metadata(self, book_id: int) -> dict[str, Any]:
        return {
            "id": book_id,
            "title": f"Wrong {book_id}",
            "authors": "Ada Author",
            "formats": [str(path) for path in self.files[book_id]],
        }


def _internal_inspection(path: Path) -> FormatInspection:
    root_id = f"format:{path.name}"
    common = {
        "root_id": root_id,
        "source_kind": EvidenceSourceKind.content_native,
        "manifestation_ids": {"isbn": ISBN},
        "artifact_sha256": SHA,
        "authoritative": True,
    }
    evidence = [
        SourceEvidence(evidence_id=f"{path.name}-id", field="identifiers", value={"isbn": ISBN}, **common),
        SourceEvidence(evidence_id=f"{path.name}-title", field="title", value="The Exact Book", **common),
        SourceEvidence(evidence_id=f"{path.name}-authors", field="authors", value=["Ada Author"], **common),
        SourceEvidence(evidence_id=f"{path.name}-language", field="languages", value=["eng"], **common),
    ]
    return FormatInspection(
        format_evidence=FormatEvidence(
            path=str(path),
            format=path.suffix.lstrip(".") or "EPUB",
            sha256=SHA,
            status=FormatEvidenceStatus.readable,
            identifiers={"isbn": ISBN},
            title="The Exact Book",
            authors=["Ada Author"],
            languages=["eng"],
            evidence_ids=[item.evidence_id for item in evidence],
        ),
        evidence=evidence,
    )


class _ExactProviderEvidence:
    async def collect(self, _book: object, _inspections: object) -> list[SourceEvidence]:
        common = {
            "root_id": "google-books:exact-volume",
            "source_kind": EvidenceSourceKind.provider_structured,
            "manifestation_ids": {"isbn": ISBN},
            "artifact_sha256": "b" * 64,
            "authoritative": True,
        }
        return [
            SourceEvidence(evidence_id="provider-id", field="identifiers", value={"isbn": ISBN}, **common),
            SourceEvidence(evidence_id="provider-title", field="title", value="The Exact Book", **common),
            SourceEvidence(evidence_id="provider-authors", field="authors", value=["Ada Author"], **common),
            SourceEvidence(evidence_id="provider-language", field="languages", value=["eng"], **common),
        ]


class _RecordingApplier:
    def __init__(self) -> None:
        self.packages: list[object] = []

    async def apply(self, package: object) -> str:
        self.packages.append(package)
        return "applied"


@pytest.mark.asyncio
async def test_shadow_pipeline_processes_all_formats_and_one_book_at_a_time(tmp_path: Path) -> None:
    first_formats = [tmp_path / "one.epub", tmp_path / "one.pdf"]
    second_formats = [tmp_path / "two.epub"]
    cli = _FakeCalibre({2: second_formats, 1: first_formats})
    inspected: list[Path] = []
    events: list[tuple[str, BookAuditState]] = []

    def inspector(path: Path) -> FormatInspection:
        inspected.append(path)
        return _internal_inspection(path)

    pipeline = LibraryAuditPipeline(
        cli=cli,
        inspector=inspector,
        evidence_enricher=_ExactProviderEvidence(),
        state_callback=lambda key, state: events.append((key, state)),
    )

    result = await pipeline.run(mode=AuditMode.shadow, run_id="verify_test")

    assert result.status is LibraryRunStatus.completed
    assert result.snapshot.book_keys == ["calibre:1", "calibre:2"]
    assert inspected == [*first_formats, *second_formats]
    assert [package.book_key for package in result.packages] == ["calibre:1", "calibre:2"]
    assert all(package.identity.tier is IdentityTier.tier_a for package in result.packages)
    assert all(package.state is BookAuditState.shadowed for package in result.packages)
    assert all(package.verify_seal() for package in result.packages)
    assert events.index(("calibre:1", BookAuditState.shadowed)) < events.index(
        ("calibre:2", BookAuditState.snapshotting)
    )


@pytest.mark.asyncio
async def test_auto_mode_is_rejected_until_feature_and_calibration_gates_are_enabled(tmp_path: Path) -> None:
    pipeline = LibraryAuditPipeline(
        cli=_FakeCalibre({1: [tmp_path / "one.epub"]}),
        inspector=_internal_inspection,
        evidence_enricher=_ExactProviderEvidence(),
    )

    with pytest.raises(ValueError, match="calibration"):
        await pipeline.run(mode=AuditMode.tier_a_auto, run_id="verify_test")


@pytest.mark.asyncio
async def test_auto_mode_remains_unavailable_even_with_advisory_gates(tmp_path: Path) -> None:
    applier = _RecordingApplier()
    pipeline = LibraryAuditPipeline(
        cli=_FakeCalibre({1: [tmp_path / "one.epub"]}),
        inspector=_internal_inspection,
        evidence_enricher=_ExactProviderEvidence(),
        applier=applier,
        auto_apply_enabled=True,
        calibration_valid=True,
    )

    with pytest.raises(ValueError, match="cryptographically authenticated"):
        await pipeline.run(mode=AuditMode.tier_a_auto, run_id="verify_test")

    assert applier.packages == []


@pytest.mark.asyncio
async def test_snapshot_preserves_writer_special_fields(tmp_path: Path) -> None:
    path = tmp_path / "one.epub"

    class SpecialMetadataCalibre(_FakeCalibre):
        def show_metadata(self, book_id: int) -> dict[str, Any]:
            metadata = super().show_metadata(book_id)
            metadata["#edition"] = "First edition"
            metadata["cover"] = "/library/Exact Book/cover.jpg"
            return metadata

    result = await LibraryAuditPipeline(
        cli=SpecialMetadataCalibre({1: [path]}),
        inspector=_internal_inspection,
        evidence_enricher=_ExactProviderEvidence(),
    ).run(mode=AuditMode.shadow, run_id="verify_test")

    assert result.packages[0].snapshot.current_metadata["edition_statement"] == "First edition"
    assert result.packages[0].snapshot.current_metadata["cover"] == "/library/Exact Book/cover.jpg"


@pytest.mark.asyncio
async def test_per_book_extraction_failure_is_recorded_and_next_book_continues(tmp_path: Path) -> None:
    first = tmp_path / "one.epub"
    second = tmp_path / "two.epub"

    def inspector(path: Path) -> FormatInspection:
        if path == first:
            raise RuntimeError("parser exploded")
        return _internal_inspection(path)

    pipeline = LibraryAuditPipeline(
        cli=_FakeCalibre({1: [first], 2: [second]}),
        inspector=inspector,
        evidence_enricher=_ExactProviderEvidence(),
    )

    result = await pipeline.run(mode=AuditMode.shadow, run_id="verify_test")

    assert result.status is LibraryRunStatus.completed_with_errors
    assert result.packages[0].state is BookAuditState.failed
    assert "parser exploded" in (result.packages[0].error or "")
    assert result.packages[1].state is BookAuditState.shadowed


@pytest.mark.asyncio
async def test_pipeline_seals_privacy_receipts_from_enrichment(tmp_path: Path) -> None:
    book_path = tmp_path / "one.epub"

    class ReceiptEnricher:
        async def collect(
            self,
            _book: BookSnapshot,
            _inspections: list[FormatInspection],
        ) -> EvidenceEnrichment:
            return EvidenceEnrichment(
                privacy_receipts=[{"provider": "test", "text_chars": 12}],
            )

    result = await LibraryAuditPipeline(
        cli=_FakeCalibre({1: [book_path]}),
        inspector=_internal_inspection,
        evidence_enricher=ReceiptEnricher(),
    ).run(run_id="run_receipt")

    assert result.packages[0].privacy_receipts == [{"provider": "test", "text_chars": 12}]
    assert result.packages[0].verify_seal()


@pytest.mark.asyncio
async def test_pipeline_skips_already_completed_book_keys(tmp_path: Path) -> None:
    inspected: list[str] = []

    def inspector(path: Path) -> FormatInspection:
        inspected.append(path.name)
        return _internal_inspection(path)

    result = await LibraryAuditPipeline(
        cli=_FakeCalibre({1: [tmp_path / "one.epub"], 2: [tmp_path / "two.epub"]}),
        inspector=inspector,
        evidence_enricher=_ExactProviderEvidence(),
    ).run(run_id="resume_run", skip_book_keys={"calibre:1"})

    assert result.snapshot.book_keys == ["calibre:1", "calibre:2"]
    assert [item.book_key for item in result.packages] == ["calibre:2"]
    assert inspected == ["two.epub"]


@pytest.mark.asyncio
async def test_pipeline_rejects_library_symlink_before_extraction_or_egress(tmp_path: Path) -> None:
    library = tmp_path / "library"
    library.mkdir()
    outside = tmp_path / "outside.epub"
    outside.write_bytes(b"private outside content")
    linked = library / "linked.epub"
    linked.symlink_to(outside)
    cli = _FakeCalibre({1: [linked]})
    cli.library_path = library
    inspected: list[Path] = []

    result = await LibraryAuditPipeline(
        cli=cli,
        inspector=lambda path: inspected.append(path) or _internal_inspection(path),
        evidence_enricher=_ExactProviderEvidence(),
    ).run(run_id="unsafe_path_run")

    assert result.status is LibraryRunStatus.completed_with_errors
    assert result.packages[0].state is BookAuditState.failed
    assert "symlink" in (result.packages[0].error or "")
    assert inspected == []


@pytest.mark.asyncio
async def test_default_extractor_rejects_parent_swapped_after_path_validation(tmp_path: Path) -> None:
    library = tmp_path / "library"
    original_parent = library / "author"
    held_parent = library / "author-original"
    outside_parent = tmp_path / "outside"
    original_parent.mkdir(parents=True)
    outside_parent.mkdir()
    ebook = original_parent / "book.txt"
    ebook.write_bytes(b"authorized book bytes")
    (outside_parent / "book.txt").write_bytes(b"replacement outside library")
    cli = _FakeCalibre({1: [ebook]})
    cli.library_path = library

    class ParentSwapPipeline(LibraryAuditPipeline):
        def _validated_file(self, raw_path: str) -> Path:
            validated = super()._validated_file(raw_path)
            original_parent.rename(held_parent)
            original_parent.symlink_to(outside_parent, target_is_directory=True)
            return validated

    result = await ParentSwapPipeline(cli=cli).run(run_id="parent_swap_run")

    assert result.status is LibraryRunStatus.completed_with_errors
    assert result.packages[0].state is BookAuditState.failed
    assert "symlink" in (result.packages[0].error or "")

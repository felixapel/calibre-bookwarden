"""One-book-at-a-time orchestration for canonical library verification."""

from __future__ import annotations

import hashlib
import inspect
import json
import os
from collections.abc import Callable
from datetime import UTC, datetime
from enum import StrEnum
from pathlib import Path
from typing import Any, Literal, Protocol, cast
from uuid import uuid4

from pydantic import BaseModel, ConfigDict, Field, field_validator

from calibre_ai_auditor.extractors.multiformat import FormatInspection, inspect_format
from calibre_ai_auditor.security.files import SecurePathError, ensure_secure_directory, open_file_beneath
from calibre_ai_auditor.verification.identity_v2 import (
    FormatEvidence,
    IdentityTier,
    ManifestationResolution,
    SourceEvidence,
    resolve_manifestation,
)


class AuditMode(StrEnum):
    shadow = "shadow"
    tier_a_auto = "tier_a_auto"


class BookAuditState(StrEnum):
    pending = "pending"
    snapshotting = "snapshotting"
    extracting = "extracting"
    resolving = "resolving"
    decided = "decided"
    shadowed = "shadowed"
    applying = "applying"
    verified = "verified"
    review = "review"
    deferred = "deferred"
    failed = "failed"
    source_changed = "source_changed"
    blocked_recovery = "blocked_recovery"


class LibraryRunStatus(StrEnum):
    running = "running"
    completed = "completed"
    completed_with_errors = "completed_with_errors"
    blocked_recovery = "blocked_recovery"


class EvidenceEnrichment(BaseModel):
    """Evidence plus auditable side effects produced by optional enrichers."""

    model_config = ConfigDict(extra="forbid")

    evidence: list[SourceEvidence] = Field(default_factory=list)
    privacy_receipts: list[dict[str, Any]] = Field(default_factory=list)
    warnings: list[str] = Field(default_factory=list)


TERMINAL_BOOK_STATES = frozenset(
    {
        BookAuditState.shadowed,
        BookAuditState.verified,
        BookAuditState.review,
        BookAuditState.deferred,
        BookAuditState.failed,
        BookAuditState.source_changed,
        BookAuditState.blocked_recovery,
    }
)


class CalibreReader(Protocol):
    def list_books(self) -> list[dict[str, Any]]: ...

    def show_metadata(self, book_id: int) -> dict[str, Any]: ...


class EvidenceEnricher(Protocol):
    async def collect(
        self,
        book: BookSnapshot,
        inspections: list[FormatInspection],
    ) -> EvidenceEnrichment | list[SourceEvidence]: ...


class PackageApplier(Protocol):
    async def apply(self, package: EvidencePackageV2) -> str: ...


class NullEvidenceEnricher:
    async def collect(
        self,
        _book: BookSnapshot,
        _inspections: list[FormatInspection],
    ) -> list[SourceEvidence]:
        return []


class BookSourceDescriptor(BaseModel):
    """Stable identity for evidence read through a remote Calibre boundary."""

    model_config = ConfigDict(extra="forbid")

    kind: Literal["calibre_content_server"]
    fingerprint: str
    access_mode: Literal["read_only"] = "read_only"

    @field_validator("fingerprint")
    @classmethod
    def validate_fingerprint(cls, value: str) -> str:
        canonical = value.strip().lower()
        if len(canonical) != 64 or any(character not in "0123456789abcdef" for character in canonical):
            raise ValueError("source fingerprint must be a lowercase SHA-256 digest")
        return canonical


class BookSnapshot(BaseModel):
    model_config = ConfigDict(extra="forbid")

    book_key: str
    calibre_book_id: int
    current_metadata: dict[str, Any]
    files: list[str]
    library_root: str | None = None
    source: BookSourceDescriptor | None = None
    source_revision_sha256: str | None = None
    snapshot_sha256: str

    @field_validator("source_revision_sha256")
    @classmethod
    def validate_source_revision(cls, value: str | None) -> str | None:
        if value is None:
            return None
        canonical = value.strip().lower()
        if len(canonical) != 64 or any(character not in "0123456789abcdef" for character in canonical):
            raise ValueError("source revision must be a lowercase SHA-256 digest")
        return canonical

    def calculated_sha256(self) -> str:
        payload: dict[str, Any] = {
            "book_key": self.book_key,
            "current_metadata": self.current_metadata,
            "files": self.files,
            "library_root": self.library_root,
        }
        if self.source is not None:
            payload["source"] = self.source.model_dump(mode="json")
            payload["source_revision_sha256"] = self.source_revision_sha256
        return _canonical_json_hash(payload)

    def verify_hash(self) -> bool:
        return self.snapshot_sha256 == self.calculated_sha256()


class LibrarySnapshot(BaseModel):
    model_config = ConfigDict(extra="forbid")

    run_id: str
    created_at: datetime
    book_keys: list[str]
    snapshot_sha256: str


class EvidencePackageV2(BaseModel):
    model_config = ConfigDict(extra="forbid")

    schema_version: Literal[2] = 2
    policy_version: Literal["manifestation-v2"] = "manifestation-v2"
    evidence_id: str
    run_id: str
    book_key: str
    created_at: datetime = Field(default_factory=lambda: datetime.now(UTC))
    state: BookAuditState
    snapshot: BookSnapshot
    formats: list[FormatEvidence] = Field(default_factory=list)
    source_evidence: list[SourceEvidence] = Field(default_factory=list)
    identity: ManifestationResolution
    privacy_receipts: list[dict[str, Any]] = Field(default_factory=list)
    warnings: list[str] = Field(default_factory=list)
    error: str | None = None
    package_sha256: str | None = None

    def verify_invariants(self) -> bool:
        source = self.snapshot.source
        expected_book_key = (
            f"calibre:{self.snapshot.calibre_book_id}"
            if source is None
            else f"calibre-server:{source.fingerprint}:{self.snapshot.calibre_book_id}"
        )
        if (
            self.book_key != self.snapshot.book_key
            or self.book_key != expected_book_key
            or not self.snapshot.verify_hash()
            or len(self.snapshot.files) != len(set(self.snapshot.files))
        ):
            return False
        if source is not None:
            prefix = f"{expected_book_key}:"
            if (
                self.snapshot.library_root is not None
                or self.snapshot.source_revision_sha256 is None
                or any(
                    not path.startswith(prefix)
                    or not path.removeprefix(prefix)
                    or any(
                        character not in "ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
                        for character in path.removeprefix(prefix)
                    )
                    for path in self.snapshot.files
                )
            ):
                return False
        elif self.snapshot.source_revision_sha256 is not None:
            return False
        elif self.snapshot.library_root is not None:
            root = Path(os.path.normpath(os.path.abspath(self.snapshot.library_root)))
            if str(root) != self.snapshot.library_root or any(
                str(candidate := Path(os.path.normpath(os.path.abspath(path)))) != path
                or not candidate.is_relative_to(root)
                for path in self.snapshot.files
            ):
                return False
        format_paths = [item.path for item in self.formats]
        pathless_states = {BookAuditState.failed, BookAuditState.source_changed}
        if self.state not in pathless_states and format_paths != self.snapshot.files:
            return False
        if len(format_paths) != len(set(format_paths)):
            return False
        evidence_ids = [item.evidence_id for item in self.source_evidence]
        if len(evidence_ids) != len(set(evidence_ids)):
            return False
        if self.identity.tier is IdentityTier.tier_a:
            derived_identity = resolve_manifestation(
                formats=self.formats,
                evidence=self.source_evidence,
                current_metadata=self.snapshot.current_metadata,
            )
            if derived_identity.model_dump(mode="json") != self.identity.model_dump(mode="json"):
                return False
        return True

    def _seal_value(self) -> str:
        payload = self.model_dump(mode="json", exclude={"package_sha256"})
        # Preserve the V2 seal of pre-remote local evidence packages.
        if self.snapshot.source is None:
            payload["snapshot"].pop("source", None)
            payload["snapshot"].pop("source_revision_sha256", None)
        encoded = json.dumps(payload, sort_keys=True, separators=(",", ":"), ensure_ascii=True).encode()
        return hashlib.sha256(encoded).hexdigest()

    def seal(self) -> EvidencePackageV2:
        if not self.verify_invariants():
            raise ValueError("V2 evidence package violates its internal snapshot invariants")
        return self.model_copy(update={"package_sha256": self._seal_value()})

    def verify_seal(self) -> bool:
        return (
            self.verify_invariants() and self.package_sha256 is not None and self.package_sha256 == self._seal_value()
        )


class LibraryAuditResult(BaseModel):
    model_config = ConfigDict(extra="forbid")

    run_id: str
    status: LibraryRunStatus
    snapshot: LibrarySnapshot
    packages: list[EvidencePackageV2] = Field(default_factory=list)


def _canonical_json_hash(value: object) -> str:
    encoded = json.dumps(value, sort_keys=True, separators=(",", ":"), ensure_ascii=True, default=str).encode()
    return hashlib.sha256(encoded).hexdigest()


def _normalize_authors(value: Any) -> list[str]:
    if isinstance(value, list):
        return [str(item).strip() for item in value if str(item).strip()]
    if isinstance(value, str):
        return [item.strip() for item in value.split("&") if item.strip()]
    return []


def _normalize_languages(value: Any) -> list[str]:
    if isinstance(value, list):
        return [str(item).strip().lower() for item in value if str(item).strip()]
    if isinstance(value, str):
        return [item.strip().lower() for item in value.replace(";", ",").split(",") if item.strip()]
    return []


def _canonical_current(raw: dict[str, Any]) -> dict[str, Any]:
    identifiers = raw.get("identifiers") if isinstance(raw.get("identifiers"), dict) else {}
    return {
        "title": raw.get("title"),
        "authors": _normalize_authors(raw.get("authors")),
        "identifiers": identifiers,
        "languages": _normalize_languages(raw.get("languages", raw.get("language"))),
        "publisher": raw.get("publisher"),
        "pubdate": raw.get("pubdate", raw.get("published_date")),
        "series": raw.get("series"),
        "series_index": raw.get("series_index"),
        "edition_statement": raw.get("#edition", raw.get("edition_statement")),
        "cover": raw.get("cover"),
    }


def _format_paths(raw: Any) -> list[str]:
    if isinstance(raw, list):
        paths: list[str] = []
        for item in raw:
            if isinstance(item, str) and item.strip():
                paths.append(item.strip())
            elif isinstance(item, dict) and isinstance(item.get("path"), str):
                paths.append(item["path"].strip())
        return paths
    if isinstance(raw, str) and raw.strip():
        return [item.strip() for item in raw.split(",") if item.strip()]
    return []


class SourceChangedError(RuntimeError):
    def __init__(self, snapshot: BookSnapshot) -> None:
        super().__init__("Calibre source changed while the book was being inspected")
        self.snapshot = snapshot


class LibraryAuditPipeline:
    def __init__(
        self,
        *,
        cli: CalibreReader,
        inspector: Callable[[Path], FormatInspection] = inspect_format,
        evidence_enricher: EvidenceEnricher | None = None,
        applier: PackageApplier | None = None,
        state_callback: Callable[[str, BookAuditState], Any] | None = None,
        package_callback: Callable[[EvidencePackageV2], Any] | None = None,
        auto_apply_enabled: bool = False,
        calibration_valid: bool = False,
        scratch_root: Path | None = None,
    ) -> None:
        self.cli = cli
        self.evidence_enricher = evidence_enricher or NullEvidenceEnricher()
        self.applier = applier
        self.state_callback = state_callback
        self.package_callback = package_callback
        self.auto_apply_enabled = auto_apply_enabled
        self.calibration_valid = calibration_valid
        self._active_book: str | None = None
        raw_library_root = getattr(cli, "library_path", None)
        source_kind = getattr(cli, "source_kind", None)
        self.source: BookSourceDescriptor | None
        self.scratch_root: Path | None
        self.library_root: Path | None
        self.remote_cli: Any | None
        if source_kind == "calibre_content_server":
            self.source = BookSourceDescriptor(
                kind="calibre_content_server",
                fingerprint=getattr(cli, "fingerprint", ""),
            )
            if scratch_root is None:
                raise ValueError("remote Content Server verification requires a scratch root")
            self.scratch_root = ensure_secure_directory(scratch_root)
            self.library_root = None
            self.remote_cli = cli
        else:
            self.source = None
            self.scratch_root = None
            self.library_root = Path(os.path.abspath(raw_library_root)) if raw_library_root else None
            self.remote_cli = None
        self.inspector = (
            (lambda path: inspect_format(path, library_root=self.library_root))
            if inspector is inspect_format
            else inspector
        )

    async def _emit(self, book_key: str, state: BookAuditState) -> None:
        if self.state_callback is None:
            return
        result = self.state_callback(book_key, state)
        if inspect.isawaitable(result):
            await result

    def _book_key(self, book_id: int) -> str:
        if self.source is None:
            return f"calibre:{book_id}"
        return f"calibre-server:{self.source.fingerprint}:{book_id}"

    def _file_references(self, book_id: int, raw_formats: object) -> list[str]:
        if self.remote_cli is None:
            return _format_paths(raw_formats)
        references = cast(list[str], self.remote_cli.format_references(book_id, raw_formats))
        if len(references) != len(set(references)):
            raise ValueError("Calibre returned duplicate remote format references")
        return references

    def _new_snapshot(
        self,
        *,
        book_id: int,
        current: dict[str, Any],
        files: list[str],
        source_revision_sha256: str | None = None,
    ) -> BookSnapshot:
        library_root = str(self.library_root) if self.library_root is not None else None
        snapshot = BookSnapshot(
            book_key=self._book_key(book_id),
            calibre_book_id=book_id,
            current_metadata=current,
            files=files,
            library_root=library_root,
            source=self.source,
            source_revision_sha256=source_revision_sha256,
            snapshot_sha256="0" * 64,
        )
        return snapshot.model_copy(update={"snapshot_sha256": snapshot.calculated_sha256()})

    def _book_snapshot(self, raw_book: dict[str, Any]) -> BookSnapshot:
        book_id = int(raw_book["id"])
        current_raw = self.cli.show_metadata(book_id)
        files = self._file_references(book_id, current_raw.get("formats") or raw_book.get("formats"))
        current = _canonical_current(current_raw)
        revision = _canonical_json_hash(current_raw) if self.source is not None else None
        return self._new_snapshot(
            book_id=book_id,
            current=current,
            files=files,
            source_revision_sha256=revision,
        )

    def _validated_file(self, raw_path: str) -> Path:
        path = Path(raw_path)
        # Test/integration readers may not expose a configured library root.
        # The production Calibre reader always does; only that boundary can
        # make a meaningful containment assertion.
        if self.library_root is None:
            return path
        if not path.is_absolute():
            raise ValueError("Calibre returned a non-absolute ebook path")
        try:
            with open_file_beneath(self.library_root, path):
                pass
        except (OSError, SecurePathError) as exc:
            raise ValueError("Calibre returned an unreadable or symlinked ebook path") from exc
        return path

    def _failed_package(self, raw_book: dict[str, Any], error: Exception) -> EvidencePackageV2:
        book_id = int(raw_book["id"])
        try:
            snapshot = self._book_snapshot(raw_book)
        except Exception:
            current = _canonical_current(raw_book)
            files = [] if self.source is not None else _format_paths(raw_book.get("formats"))
            revision = _canonical_json_hash(raw_book) if self.source is not None else None
            snapshot = self._new_snapshot(
                book_id=book_id,
                current=current,
                files=files,
                source_revision_sha256=revision,
            )
        message = (
            "Remote content inspection failed"
            if self.source is not None
            else (str(error)[:500] or type(error).__name__)
        )
        return EvidencePackageV2(
            evidence_id=f"evidence_{uuid4().hex}",
            run_id=self._run_id,
            book_key=snapshot.book_key,
            state=BookAuditState.failed,
            snapshot=snapshot,
            identity=ManifestationResolution(
                tier=IdentityTier.tier_c,
                risk_flags=["pipeline_error"],
                reasons=[message],
            ),
            error=message,
        ).seal()

    def _source_changed_package(self, snapshot: BookSnapshot) -> EvidencePackageV2:
        return EvidencePackageV2(
            evidence_id=f"evidence_{uuid4().hex}",
            run_id=self._run_id,
            book_key=snapshot.book_key,
            state=BookAuditState.source_changed,
            snapshot=snapshot,
            identity=ManifestationResolution(
                tier=IdentityTier.tier_c,
                risk_flags=["source_changed"],
                reasons=["Calibre source changed while the book was being inspected"],
            ),
            error="Calibre source changed while the book was being inspected",
        ).seal()

    async def _inspect_remote(
        self,
        snapshot: BookSnapshot,
    ) -> tuple[list[FormatInspection], EvidenceEnrichment]:
        assert self.remote_cli is not None
        assert self.scratch_root is not None
        inspections: list[FormatInspection] = []
        evidence_by_id: dict[str, SourceEvidence] = {}
        privacy_receipts: list[dict[str, Any]] = []
        receipt_hashes: set[str] = set()
        warnings: list[str] = []

        def merge(raw: EvidenceEnrichment | list[SourceEvidence]) -> None:
            enrichment = raw if isinstance(raw, EvidenceEnrichment) else EvidenceEnrichment(evidence=raw)
            for evidence in enrichment.evidence:
                existing = evidence_by_id.get(evidence.evidence_id)
                if existing is not None and existing != evidence:
                    raise ValueError("remote enricher returned conflicting evidence identifiers")
                evidence_by_id[evidence.evidence_id] = evidence
            for receipt in enrichment.privacy_receipts:
                receipt_hash = _canonical_json_hash(receipt)
                if receipt_hash not in receipt_hashes:
                    receipt_hashes.add(receipt_hash)
                    privacy_receipts.append(receipt)
            for warning in enrichment.warnings:
                if warning not in warnings:
                    warnings.append(warning)

        materialized_collector = getattr(self.evidence_enricher, "collect_materialized", None)
        for reference in snapshot.files:
            with self.remote_cli.export_format(
                snapshot.calibre_book_id,
                self.remote_cli.format_from_reference(reference),
                scratch_root=self.scratch_root,
            ) as path:
                inspection = self.inspector(path)
                if callable(materialized_collector):
                    merge(await materialized_collector(snapshot, inspection))
                inspections.append(
                    inspection.model_copy(
                        update={"format_evidence": inspection.format_evidence.model_copy(update={"path": reference})}
                    )
                )
        aggregate_collector = getattr(self.evidence_enricher, "collect_aggregate", None)
        if callable(aggregate_collector):
            merge(await aggregate_collector(snapshot, inspections))
        else:
            merge(await self.evidence_enricher.collect(snapshot, inspections))
        combined_enrichment = EvidenceEnrichment(
            evidence=list(evidence_by_id.values()),
            privacy_receipts=privacy_receipts,
            warnings=warnings,
        )
        current_raw = self.cli.show_metadata(snapshot.calibre_book_id)
        current = _canonical_current(current_raw)
        files = self._file_references(
            snapshot.calibre_book_id,
            current_raw.get("formats"),
        )
        if (
            current != snapshot.current_metadata
            or files != snapshot.files
            or _canonical_json_hash(current_raw) != snapshot.source_revision_sha256
        ):
            raise SourceChangedError(snapshot)
        return inspections, combined_enrichment

    async def _process_book(
        self,
        raw_book: dict[str, Any],
        *,
        mode: AuditMode,
    ) -> EvidencePackageV2:
        book_key = self._book_key(int(raw_book["id"]))
        await self._emit(book_key, BookAuditState.snapshotting)
        snapshot = self._book_snapshot(raw_book)
        await self._emit(book_key, BookAuditState.extracting)
        if self.remote_cli is not None:
            inspections, enrichment = await self._inspect_remote(snapshot)
        else:
            inspections = [self.inspector(self._validated_file(path)) for path in snapshot.files]
            raw_enrichment = await self.evidence_enricher.collect(snapshot, inspections)
            enrichment = (
                raw_enrichment
                if isinstance(raw_enrichment, EvidenceEnrichment)
                else EvidenceEnrichment(evidence=raw_enrichment)
            )
        source_evidence = [item for inspection in inspections for item in inspection.evidence]
        source_evidence.extend(enrichment.evidence)
        await self._emit(book_key, BookAuditState.resolving)
        identity = resolve_manifestation(
            formats=[inspection.format_evidence for inspection in inspections],
            evidence=source_evidence,
            current_metadata=snapshot.current_metadata,
        )
        evidence_id = f"evidence_{uuid4().hex}"
        await self._emit(book_key, BookAuditState.decided)
        if identity.tier is IdentityTier.tier_c:
            state = BookAuditState.deferred
        elif identity.tier is IdentityTier.tier_b:
            state = BookAuditState.review
        elif mode is AuditMode.shadow:
            state = BookAuditState.shadowed
        else:
            raise ValueError("automatic V2 application is unavailable")
        package = EvidencePackageV2(
            evidence_id=evidence_id,
            run_id=self._run_id,
            book_key=book_key,
            state=state,
            snapshot=snapshot,
            formats=[inspection.format_evidence for inspection in inspections],
            source_evidence=source_evidence,
            identity=identity,
            privacy_receipts=enrichment.privacy_receipts,
            warnings=enrichment.warnings,
        ).seal()
        await self._emit(book_key, state)
        return package

    async def run(
        self,
        *,
        mode: AuditMode = AuditMode.shadow,
        run_id: str | None = None,
        limit: int = 0,
        skip_book_keys: set[str] | frozenset[str] | None = None,
    ) -> LibraryAuditResult:
        if mode is AuditMode.tier_a_auto:
            raise ValueError(
                "Tier A auto-apply is unavailable until calibration reports are cryptographically authenticated"
            )
        self._run_id = run_id or f"verify_{datetime.now(UTC).strftime('%Y%m%d_%H%M%S')}_{uuid4().hex[:8]}"
        books = sorted(self.cli.list_books(), key=lambda item: int(item["id"]))
        if limit > 0:
            books = books[:limit]
        book_keys = [self._book_key(int(item["id"])) for item in books]
        snapshot = LibrarySnapshot(
            run_id=self._run_id,
            created_at=datetime.now(UTC),
            book_keys=book_keys,
            snapshot_sha256=_canonical_json_hash(book_keys),
        )
        packages: list[EvidencePackageV2] = []
        has_errors = False
        skipped = skip_book_keys or set()
        for raw_book in books:
            book_key = self._book_key(int(raw_book["id"]))
            if book_key in skipped:
                continue
            if self._active_book is not None:
                raise RuntimeError(f"book {self._active_book} is still active")
            self._active_book = book_key
            try:
                package = await self._process_book(raw_book, mode=mode)
            except SourceChangedError as exc:
                package = self._source_changed_package(exc.snapshot)
                await self._emit(book_key, BookAuditState.source_changed)
            except Exception as exc:
                has_errors = True
                package = self._failed_package(raw_book, exc)
                await self._emit(book_key, BookAuditState.failed)
            finally:
                self._active_book = None
            if package.state not in TERMINAL_BOOK_STATES:
                raise RuntimeError(f"book {book_key} did not reach a terminal state")
            packages.append(package)
            if self.package_callback is not None:
                callback_result = self.package_callback(package)
                if inspect.isawaitable(callback_result):
                    await callback_result
        status = LibraryRunStatus.completed_with_errors if has_errors else LibraryRunStatus.completed
        return LibraryAuditResult(run_id=self._run_id, status=status, snapshot=snapshot, packages=packages)

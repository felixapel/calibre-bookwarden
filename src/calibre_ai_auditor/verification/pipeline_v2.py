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
from typing import Any, Literal, Protocol
from uuid import uuid4

from pydantic import BaseModel, ConfigDict, Field

from calibre_ai_auditor.extractors.multiformat import FormatInspection, inspect_format
from calibre_ai_auditor.security.files import SecurePathError, open_file_beneath
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


class BookSnapshot(BaseModel):
    model_config = ConfigDict(extra="forbid")

    book_key: str
    calibre_book_id: int
    current_metadata: dict[str, Any]
    files: list[str]
    library_root: str | None = None
    snapshot_sha256: str

    def calculated_sha256(self) -> str:
        return _canonical_json_hash(
            {
                "book_key": self.book_key,
                "current_metadata": self.current_metadata,
                "files": self.files,
                "library_root": self.library_root,
            }
        )

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
        expected_book_key = f"calibre:{self.snapshot.calibre_book_id}"
        if (
            self.book_key != self.snapshot.book_key
            or self.book_key != expected_book_key
            or not self.snapshot.verify_hash()
            or len(self.snapshot.files) != len(set(self.snapshot.files))
        ):
            return False
        if self.snapshot.library_root is not None:
            root = Path(os.path.normpath(os.path.abspath(self.snapshot.library_root)))
            if str(root) != self.snapshot.library_root or any(
                str(candidate := Path(os.path.normpath(os.path.abspath(path)))) != path
                or not candidate.is_relative_to(root)
                for path in self.snapshot.files
            ):
                return False
        format_paths = [item.path for item in self.formats]
        if self.state is not BookAuditState.failed and format_paths != self.snapshot.files:
            return False
        if len(format_paths) != len(set(format_paths)):
            return False
        evidence_ids = [item.evidence_id for item in self.source_evidence]
        return len(evidence_ids) == len(set(evidence_ids))

    def _seal_value(self) -> str:
        payload = self.model_dump(mode="json", exclude={"package_sha256"})
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
        self.library_root = Path(os.path.abspath(raw_library_root)) if raw_library_root else None
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

    def _book_snapshot(self, raw_book: dict[str, Any]) -> BookSnapshot:
        book_id = int(raw_book["id"])
        current_raw = self.cli.show_metadata(book_id)
        files = _format_paths(current_raw.get("formats") or raw_book.get("formats"))
        current = _canonical_current(current_raw)
        library_root = str(self.library_root) if self.library_root is not None else None
        digest = _canonical_json_hash(
            {
                "book_key": f"calibre:{book_id}",
                "current_metadata": current,
                "files": files,
                "library_root": library_root,
            }
        )
        return BookSnapshot(
            book_key=f"calibre:{book_id}",
            calibre_book_id=book_id,
            current_metadata=current,
            files=files,
            library_root=library_root,
            snapshot_sha256=digest,
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
            files = _format_paths(raw_book.get("formats"))
            library_root = str(self.library_root) if self.library_root is not None else None
            snapshot = BookSnapshot(
                book_key=f"calibre:{book_id}",
                calibre_book_id=book_id,
                current_metadata=current,
                files=files,
                library_root=library_root,
                snapshot_sha256=_canonical_json_hash(
                    {
                        "book_key": f"calibre:{book_id}",
                        "current_metadata": current,
                        "files": files,
                        "library_root": library_root,
                    }
                ),
            )
        message = str(error)[:500] or type(error).__name__
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

    async def _process_book(
        self,
        raw_book: dict[str, Any],
        *,
        mode: AuditMode,
    ) -> EvidencePackageV2:
        book_key = f"calibre:{int(raw_book['id'])}"
        await self._emit(book_key, BookAuditState.snapshotting)
        snapshot = self._book_snapshot(raw_book)
        await self._emit(book_key, BookAuditState.extracting)
        inspections = [self.inspector(self._validated_file(path)) for path in snapshot.files]
        source_evidence = [item for inspection in inspections for item in inspection.evidence]
        raw_enrichment = await self.evidence_enricher.collect(snapshot, inspections)
        enrichment = (
            raw_enrichment
            if isinstance(raw_enrichment, EvidenceEnrichment)
            else EvidenceEnrichment(evidence=raw_enrichment)
        )
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
        book_keys = [f"calibre:{int(item['id'])}" for item in books]
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
            book_key = f"calibre:{int(raw_book['id'])}"
            if book_key in skipped:
                continue
            if self._active_book is not None:
                raise RuntimeError(f"book {self._active_book} is still active")
            self._active_book = book_key
            try:
                package = await self._process_book(raw_book, mode=mode)
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

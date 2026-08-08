"""Application service for persisted manifestation V2 library audits."""

from __future__ import annotations

from pathlib import Path
from typing import Any, cast

from sqlalchemy.engine import Engine

from calibre_ai_auditor.config.settings import Settings
from calibre_ai_auditor.llm.router import LLMRouter
from calibre_ai_auditor.ocr.recognition_v2 import OCRRecognitionEnricher, VisionRecognitionEnricher
from calibre_ai_auditor.ocr.vision import VisionVerifier
from calibre_ai_auditor.providers.evidence_v2 import (
    CompositeEvidenceEnricher,
    MinimalEvidenceLLMEnricher,
    StructuredEvidenceEnricher,
)
from calibre_ai_auditor.verification.ocr_router import PaddleOCRProvider, SuryaProvider, TesseractProvider
from calibre_ai_auditor.verification.persistence_v2 import SQLAuditStore
from calibre_ai_auditor.verification.pipeline_v2 import (
    AuditMode,
    EvidenceEnricher,
    LibraryAuditPipeline,
    LibraryAuditResult,
    PackageApplier,
)


class FrozenLibraryReader:
    """Freeze library membership while delegating per-book metadata reads."""

    def __init__(self, cli: Any, books: list[dict[str, Any]]) -> None:
        self.cli = cli
        self.books = [dict(item) for item in books]

    def list_books(self) -> list[dict[str, Any]]:
        return [dict(item) for item in self.books]

    def show_metadata(self, book_id: int) -> dict[str, Any]:
        return cast(dict[str, Any], self.cli.show_metadata(book_id))

    @property
    def source_kind(self) -> str | None:
        return cast(str | None, getattr(self.cli, "source_kind", None))

    @property
    def fingerprint(self) -> str | None:
        return cast(str | None, getattr(self.cli, "fingerprint", None))

    def format_references(self, book_id: int, raw_formats: object) -> list[str]:
        return cast(list[str], self.cli.format_references(book_id, raw_formats))

    def format_from_reference(self, reference: str) -> str:
        return cast(str, self.cli.format_from_reference(reference))

    def export_format(self, book_id: int, format_name: str, *, scratch_root: Path) -> Any:
        return self.cli.export_format(book_id, format_name, scratch_root=scratch_root)

    @property
    def library_path(self) -> Path | None:
        raw = getattr(self.cli, "library_path", None)
        return Path(raw) if raw is not None else None


def build_v2_enricher(
    settings: Settings,
    *,
    use_llm: bool,
    use_ocr: bool = True,
    use_vision: bool = False,
    run_allows_remote_text: bool,
    run_allows_remote_images: bool = False,
    use_public_providers: bool = True,
) -> EvidenceEnricher:
    enrichers: list[Any] = []
    ocr_settings = settings.recognition_v2.ocr
    if use_ocr and ocr_settings.enabled:
        ocr_backends: list[Any] = []
        for backend in ocr_settings.backends:
            if backend == "tesseract":
                ocr_backends.append(TesseractProvider())
            elif backend == "paddleocr":
                ocr_backends.append(
                    PaddleOCRProvider(
                        lang=ocr_settings.language,
                        use_gpu=ocr_settings.paddleocr_use_gpu,
                    )
                )
            elif backend == "surya":
                ocr_backends.append(SuryaProvider(device=ocr_settings.surya_device))
        enrichers.append(
            OCRRecognitionEnricher(
                backends=ocr_backends,
                max_pages=ocr_settings.max_pages,
                language=ocr_settings.language,
            )
        )
    if use_vision:
        if not settings.recognition_v2.vision.enabled:
            raise ValueError("V2 vision requires recognition_v2.vision.enabled=true")
        verifier = VisionVerifier(settings)
        try:
            provider = verifier.router.get_provider_for_task("vision")
            vision_backend: Any | None = _VisionVerifierBackend(
                verifier=verifier,
                name=provider.name,
                is_local=bool(provider.is_local),
            )
        except (RuntimeError, ValueError):
            vision_backend = None
        enrichers.append(
            VisionRecognitionEnricher(
                backend=vision_backend,
                allow_remote_images=settings.privacy.allow_remote_images,
                run_allows_remote_images=run_allows_remote_images,
                max_images=settings.privacy.max_remote_images,
            )
        )
    # Recognition runs first so a checksum-valid candidate can drive an exact
    # provider lookup without becoming authoritative on its own.
    if use_public_providers:
        enrichers.append(StructuredEvidenceEnricher.from_settings(settings))
    if use_llm:
        enrichers.append(
            MinimalEvidenceLLMEnricher(
                router=LLMRouter(settings),
                model=settings.judge_model,
                allow_remote_text=settings.privacy.allow_remote_text,
                run_allows_remote_text=run_allows_remote_text,
                max_chars=settings.privacy.max_remote_chars,
            )
        )
    return CompositeEvidenceEnricher(enrichers)


class _VisionVerifierBackend:
    def __init__(self, *, verifier: VisionVerifier, name: str, is_local: bool) -> None:
        self.verifier = verifier
        self.name = name
        self.is_local = is_local

    async def recognize_cover(self, cover_path: Path) -> dict[str, Any] | None:
        return await self.verifier.verify_cover(cover_path)


async def run_persisted_library_audit(
    *,
    cli: Any,
    database_engine: Engine,
    run_id: str,
    limit: int,
    mode: AuditMode,
    evidence_enricher: EvidenceEnricher,
    use_llm: bool,
    books: list[dict[str, Any]] | None = None,
    settings: Settings | None = None,
    applier: PackageApplier | None = None,
    scratch_root: Path | None = None,
) -> LibraryAuditResult:
    if mode is AuditMode.tier_a_auto:
        raise ValueError(
            "Tier A auto-apply is unavailable until calibration reports are cryptographically authenticated"
        )
    books = sorted(books if books is not None else cli.list_books(), key=lambda item: int(item["id"]))
    if limit > 0:
        books = books[:limit]
    source_kind = getattr(cli, "source_kind", None)
    if source_kind in {"calibre_content_server", "offline_calibre_snapshot"}:
        fingerprint = str(getattr(cli, "fingerprint", ""))
        prefix = "calibre-server" if source_kind == "calibre_content_server" else "calibre-offline"
        book_keys = [f"{prefix}:{fingerprint}:{int(item['id'])}" for item in books]
    else:
        book_keys = [f"calibre:{int(item['id'])}" for item in books]
    store = SQLAuditStore(database_engine, run_id)
    store.start(book_keys=book_keys, mode=mode.value, use_llm=use_llm)
    reader = FrozenLibraryReader(cli, books)
    pipeline = LibraryAuditPipeline(
        cli=reader,
        evidence_enricher=evidence_enricher,
        state_callback=store.record_state,
        package_callback=store.record_package,
        applier=applier,
        auto_apply_enabled=False,
        calibration_valid=False,
        scratch_root=scratch_root,
    )
    try:
        result = await pipeline.run(
            mode=mode,
            run_id=run_id,
            skip_book_keys=store.resumable_book_keys(),
        )
    except Exception:
        store.finish("failed")
        raise
    store.finish(result.status.value)
    return result

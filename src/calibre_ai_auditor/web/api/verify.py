"""Verify API for Manifestation V2 and the legacy V1 engine.

Endpoints:
  POST /api/verify           — start a verify run (returns run_id)
  GET  /api/verify/{run_id}  — get progress + verdicts for a run
  GET  /api/verify/runs      — list recent runs with summary stats

POST defaults to the exact-edition, all-format, sealed V2 contract. Clients may
explicitly select V1 for the legacy ContentVerificationEngine response shape.
"""

import asyncio
import logging
from collections.abc import Generator
from datetime import UTC, datetime
from pathlib import Path
from typing import Annotated, Any, Literal, cast
from uuid import uuid4

from fastapi import APIRouter, Body, Depends, HTTPException
from pydantic import BaseModel
from sqlalchemy.engine import Engine
from sqlmodel import Session, col, desc, select

from calibre_ai_auditor.calibre.cli import CalibreCLI
from calibre_ai_auditor.comics.pipeline import enrich_comic_observations
from calibre_ai_auditor.config.settings import Settings, load_settings
from calibre_ai_auditor.extractors.heuristics import extract_heuristics
from calibre_ai_auditor.extractors.text import extract_snippets
from calibre_ai_auditor.storage.db import get_engine
from calibre_ai_auditor.storage.models import VerificationResult, VerificationRun
from calibre_ai_auditor.verification.engine import (
    ContentVerificationEngine,
    DeclaredMetadata,
    ObservationSet,
)
from calibre_ai_auditor.verification.metrics import get_metrics
from calibre_ai_auditor.verification.resumable import ResumableRunStore

logger = logging.getLogger(__name__)
router = APIRouter(prefix="/verify", tags=["Verify"])


class VerifyRequest(BaseModel):
    library: str | None = None
    limit: int | None = None
    use_llm: bool = False
    use_ocr: bool = True
    use_vision: bool = False
    pipeline: Literal["v2", "v1"] = "v2"
    run_id: str | None = None
    allow_remote_text: bool = False
    allow_remote_images: bool = False


class VerifyStartResponse(BaseModel):
    run_id: str
    started_at: datetime
    total: int
    status: str = "running"


class VerifyRunSummary(BaseModel):
    run_id: str
    status: str
    pipeline_version: str = "v1"
    mode: str = "legacy"
    started_at: datetime
    finished_at: datetime | None = None
    total: int
    completed: int
    counts: dict[str, int]


class VerifyRunDetail(VerifyRunSummary):
    verdicts: list[dict[str, Any]]


class VerifyStartEnvelope(BaseModel):
    status: Literal["success"] = "success"
    data: VerifyStartResponse


class VerifyRunsEnvelope(BaseModel):
    status: Literal["success"] = "success"
    data: dict[Literal["runs"], list[VerifyRunSummary]]


class VerifyRunEnvelope(BaseModel):
    status: Literal["success"] = "success"
    data: VerifyRunDetail


def get_settings() -> Settings:
    return load_settings()


def get_session(
    settings: Annotated[Settings, Depends(get_settings)],
) -> Generator[Session, None, None]:
    with Session(get_engine(settings)) as session:
        yield session


@router.post("", response_model=VerifyStartEnvelope)
async def start_verify(
    settings: Annotated[Settings, Depends(get_settings)],
    session: Annotated[Session, Depends(get_session)],
    req: VerifyRequest = Body(default_factory=VerifyRequest),
) -> VerifyStartEnvelope:
    lib_path = Path(req.library) if req.library else settings.library.path
    if not lib_path:
        raise HTTPException(status_code=400, detail="No library path configured")

    if not lib_path.exists():
        raise HTTPException(status_code=404, detail=f"Library path not found: {lib_path}")

    cli = CalibreCLI(lib_path)
    books = cli.list_books()
    if req.limit:
        books = books[: req.limit]

    run_id = req.run_id or f"verify_{datetime.now().strftime('%Y%m%d_%H%M%S')}_{uuid4().hex[:8]}"
    started_at = datetime.now(UTC)
    metrics = get_metrics()
    metrics.set_run_progress(run_id, total=len(books), completed=0)

    if req.pipeline == "v2":
        from calibre_ai_auditor.verification.durable_v2 import (
            execute_claimed_v2_library_audit,
            new_worker_id,
        )
        from calibre_ai_auditor.verification.persistence_v2 import SQLAuditStore
        from calibre_ai_auditor.verification.pipeline_v2 import AuditMode
        from calibre_ai_auditor.verification.service_v2 import build_v2_enricher

        v2_database_engine = cast(Engine, session.get_bind())
        # Validate enricher construction early so the client gets 422, not a
        # silent background failure after the run id is issued.
        try:
            build_v2_enricher(
                settings,
                use_llm=req.use_llm,
                use_ocr=req.use_ocr,
                use_vision=req.use_vision,
                run_allows_remote_text=req.allow_remote_text,
                run_allows_remote_images=req.allow_remote_images,
            )
        except ValueError as exc:
            raise HTTPException(status_code=422, detail=str(exc)) from None
        store = SQLAuditStore(v2_database_engine, run_id)
        try:
            store.start(
                book_keys=[f"calibre:{int(book['id'])}" for book in books],
                mode=AuditMode.shadow.value,
                use_llm=req.use_llm,
            )
        except ValueError as exc:
            raise HTTPException(status_code=409, detail=str(exc)) from None

        worker_id = new_worker_id("api")

        async def _run_v2() -> None:
            try:
                await execute_claimed_v2_library_audit(
                    cli=cli,
                    database_engine=v2_database_engine,
                    run_id=run_id,
                    owner=worker_id,
                    use_llm=req.use_llm,
                    use_ocr=req.use_ocr,
                    use_vision=req.use_vision,
                    allow_remote_text=req.allow_remote_text,
                    allow_remote_images=req.allow_remote_images,
                    books=books,
                    settings=settings,
                )
            except Exception:
                logger.exception("Manifestation V2 verify run %s failed", run_id)

        # Task is still in-process, but progress + membership + lease are durable;
        # app lifespan recovery reclaims orphaned leases after restart.
        asyncio.create_task(_run_v2())
        return VerifyStartEnvelope(
            data=VerifyStartResponse(
                run_id=run_id,
                started_at=started_at,
                total=len(books),
                status="running",
            )
        )

    # Run synchronously in the background (FastAPI threadpool) so the
    # caller can poll /verify/{run_id} for progress.
    session.add(
        VerificationRun(
            run_id=run_id,
            started_at=started_at,
            total=len(books),
            counts={"no_change": 0, "suggest_fix": 0, "needs_review": 0, "defer": 0},
            use_llm=req.use_llm,
        )
    )
    session.commit()
    database_engine = session.get_bind()

    async def _run() -> None:
        engine = ContentVerificationEngine()
        store = ResumableRunStore(run_id, [f"calibre:{b['id']}" for b in books])

        for i, book in enumerate(books, start=1):
            try:
                book_key = f"calibre:{book['id']}"
                authors_str = book.get("authors", "")
                if isinstance(authors_str, str):
                    authors_list = [a.strip() for a in authors_str.split("&") if a.strip()]
                else:
                    authors_list = []

                snippet_text = ""
                for fmt in book.get("formats", [])[:1]:
                    file_path = Path(fmt)
                    if file_path.exists():
                        snippets = extract_snippets(file_path, max_pages=3)
                        snippet_text = "\n".join(s.text for s in snippets)
                        break

                heuristics = extract_heuristics(
                    [{"text": snippet_text, "source": "first_pages"}] if snippet_text else []
                )

                # Use central pipeline to replace all scattered comic_meta / cover_vision / manual merge logic
                # (ComicInfo + vision for cbz + Komf; comic fills gaps preferring base values)
                d_title = book.get("title")
                d_authors = authors_list
                d_series = book.get("series")
                d_series_index = book.get("series_index")
                d_volume = book.get("volume")
                d_chapter = book.get("chapter")
                d_series_pos = book.get("series_position")
                d_publisher = book.get("publisher")
                d_pubdate = book.get("pubdate")
                d_isbn = (book.get("identifiers") or {}).get("isbn") if book.get("identifiers") else None

                raw_decl = {
                    "title": d_title,
                    "authors": d_authors,
                    "publisher": d_publisher,
                    "published_date": d_pubdate,
                    "language": book.get("languages"),
                    "series": d_series,
                    "series_index": d_series_index,
                    "isbn": d_isbn,
                    "volume": d_volume,
                    "chapter": d_chapter,
                    "series_position": d_series_pos,
                }
                raw_obs = {
                    "title": heuristics.get("title"),
                    "authors": heuristics.get("authors") or [],
                    "publisher": heuristics.get("publisher"),
                    "published_date": heuristics.get("published_date"),
                    "language": heuristics.get("language"),
                    "series": heuristics.get("series"),
                    "series_index": heuristics.get("series_index"),
                    "isbn": (heuristics.get("identifiers") or {}).get("isbn"),
                    "volume": heuristics.get("volume"),
                    "chapter": heuristics.get("chapter"),
                    "series_position": heuristics.get("series_position"),
                }

                first_file_path_for_comic = None
                for fmt in book.get("formats", [])[:1]:
                    fp = Path(fmt)
                    if fp.exists():
                        first_file_path_for_comic = fp
                        break
                is_cbz = bool(
                    first_file_path_for_comic and first_file_path_for_comic.suffix.lower() in (".cbz", ".cbr")
                )
                enriched_decl, enriched_obs = await enrich_comic_observations(
                    settings, first_file_path_for_comic if is_cbz else None, None, raw_decl, raw_obs
                )

                declared = DeclaredMetadata(
                    title=enriched_decl.get("title") or d_title,
                    authors=enriched_decl.get("authors") or d_authors,
                    publisher=enriched_decl.get("publisher") or d_publisher,
                    published_date=enriched_decl.get("published_date") or d_pubdate,
                    language=enriched_decl.get("language") or book.get("languages"),
                    series=enriched_decl.get("series") or d_series,
                    series_index=enriched_decl.get("series_index") or d_series_index,
                    volume=enriched_decl.get("volume"),
                    chapter=enriched_decl.get("chapter"),
                    series_position=enriched_decl.get("series_position"),
                    isbn=enriched_decl.get("isbn") or d_isbn,
                )

                observed = ObservationSet(
                    title_page_text=snippet_text[:5000] if snippet_text else None,
                    body_sample=snippet_text[:10000] if snippet_text else None,
                    title_extracted=enriched_obs.get("title") or heuristics.get("title"),
                    authors_extracted=enriched_obs.get("authors") or heuristics.get("authors") or [],
                    isbn_extracted=enriched_obs.get("isbn") or (heuristics.get("identifiers") or {}).get("isbn"),
                    publisher_extracted=enriched_obs.get("publisher") or heuristics.get("publisher"),
                    date_extracted=enriched_obs.get("published_date") or heuristics.get("published_date"),
                    language_detected=enriched_obs.get("language") or heuristics.get("language"),
                    series_extracted=enriched_obs.get("series") or heuristics.get("series"),
                    series_index_extracted=enriched_obs.get("series_index") or heuristics.get("series_index"),
                    volume_extracted=enriched_obs.get("volume"),
                    chapter_extracted=enriched_obs.get("chapter"),
                    series_position_extracted=enriched_obs.get("series_position"),
                    cover_vision=enriched_obs.get("cover_vision"),
                    evidence_quality="high" if len(snippet_text) > 500 else "low",
                )

                verdict = engine.verify(
                    book_key=book_key,
                    run_id=run_id,
                    declared=declared,
                    observed=observed,
                )

                with Session(database_engine) as result_session:
                    durable_run = result_session.exec(
                        select(VerificationRun).where(VerificationRun.run_id == run_id)
                    ).one()
                    counts = dict(durable_run.counts)
                    counts[verdict.action.value] = counts.get(verdict.action.value, 0) + 1
                    durable_run.counts = counts
                    durable_run.completed = i
                    result_session.add(
                        VerificationResult(
                            result_id=str(uuid4()),
                            run_id=run_id,
                            book_key=book_key,
                            verdict=verdict.model_dump(mode="json"),
                        )
                    )
                    result_session.add(durable_run)
                    result_session.commit()
                metrics.record_book_action(verdict.action.value, run_id)
                await store.mark_completed(book_key)
                metrics.set_run_progress(run_id, total=len(books), completed=i)
            except Exception as e:  # pragma: no cover — defensive
                logger.warning("Verify failed for book %s: %s", book.get("id"), e)
                await store.mark_failed(f"calibre:{book['id']}")

        with Session(database_engine) as finish_session:
            durable_run = finish_session.exec(select(VerificationRun).where(VerificationRun.run_id == run_id)).one()
            durable_run.status = "completed"
            durable_run.finished_at = datetime.now(UTC)
            finish_session.add(durable_run)
            finish_session.commit()
            logger.info("Verify run %s completed: %s", run_id, durable_run.counts)

    # Fire-and-forget; the run id is returned to the caller
    asyncio.create_task(_run())

    return VerifyStartEnvelope(
        data=VerifyStartResponse(
            run_id=run_id,
            started_at=started_at,
            total=len(books),
            status="running",
        )
    )


@router.get("/runs", response_model=VerifyRunsEnvelope)
async def list_verify_runs(session: Annotated[Session, Depends(get_session)]) -> VerifyRunsEnvelope:
    """List all verify runs with summary stats."""
    runs = session.exec(select(VerificationRun).order_by(desc(VerificationRun.started_at)).limit(100)).all()
    return VerifyRunsEnvelope(data={"runs": [VerifyRunSummary.model_validate(run) for run in runs]})


@router.get("/{run_id}", response_model=VerifyRunEnvelope)
async def get_verify_run(run_id: str, session: Annotated[Session, Depends(get_session)]) -> VerifyRunEnvelope:
    """Get progress + per-book verdicts for a verify run."""
    durable_run = session.exec(select(VerificationRun).where(VerificationRun.run_id == run_id)).first()
    if durable_run is None:
        raise HTTPException(status_code=404, detail=f"Verify run not found: {run_id}")
    results = session.exec(
        select(VerificationResult).where(VerificationResult.run_id == run_id).order_by(col(VerificationResult.id))
    ).all()
    verdicts: list[dict[str, Any]] = []
    for result in results:
        if result.evidence_id:
            from calibre_ai_auditor.storage.models import EvidencePackage
            from calibre_ai_auditor.verification.pipeline_v2 import EvidencePackageV2

            package = session.exec(
                select(EvidencePackage).where(EvidencePackage.evidence_id == result.evidence_id)
            ).first()
            if package and package.observations:
                try:
                    parsed = EvidencePackageV2.model_validate(package.observations)
                    if parsed.verify_seal():
                        verdicts.append(parsed.model_dump(mode="json"))
                        continue
                except ValueError:
                    logger.warning("Rejected invalid V2 evidence package %s", result.evidence_id)
            verdicts.append(
                {
                    "schema_version": 2,
                    "evidence_id": result.evidence_id,
                    "book_key": result.book_key,
                    "state": "failed",
                    "warnings": ["invalid_or_missing_evidence_seal"],
                }
            )
            continue
        verdicts.append(result.verdict)
    return VerifyRunEnvelope(
        data=VerifyRunDetail(
            run_id=run_id,
            status=durable_run.status,
            pipeline_version=durable_run.pipeline_version,
            mode=durable_run.mode,
            started_at=durable_run.started_at,
            finished_at=durable_run.finished_at,
            total=durable_run.total,
            completed=durable_run.completed,
            counts=durable_run.counts,
            verdicts=verdicts,
        )
    )

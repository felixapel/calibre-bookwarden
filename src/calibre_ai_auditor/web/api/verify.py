"""Verify API — programmatic v1.0 ContentVerificationEngine runs over a Calibre library.

Endpoints:
  POST /api/verify           — start a verify run (returns run_id)
  GET  /api/verify/{run_id}  — get progress + verdicts for a run
  GET  /api/verify/runs      — list recent runs with summary stats

The verify path differs from /api/audit (legacy v0.9) by using the
ContentVerificationEngine + per-field FieldVerdicts + auto-apply gate
instead of a single aggregate MetadataResolution.
"""

import asyncio
import logging
from datetime import UTC, datetime
from pathlib import Path
from typing import Any

from fastapi import APIRouter, Body, HTTPException
from pydantic import BaseModel

from calibre_ai_auditor.calibre.cli import CalibreCLI
from calibre_ai_auditor.config.settings import load_settings
from calibre_ai_auditor.extractors.heuristics import extract_heuristics
from calibre_ai_auditor.extractors.text import extract_snippets
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


class VerifyStartResponse(BaseModel):
    run_id: str
    started_at: datetime
    total: int
    status: str = "running"


# In-memory run state. In production this would go to Postgres / Valkey.
_runs: dict[str, dict[str, Any]] = {}


@router.post("", response_model=VerifyStartResponse)
async def start_verify(
    req: VerifyRequest = Body(default_factory=VerifyRequest),
) -> VerifyStartResponse:
    settings = load_settings()
    lib_path = Path(req.library) if req.library else settings.library.path
    if not lib_path:
        raise HTTPException(status_code=400, detail="No library path configured")

    if not lib_path.exists():
        raise HTTPException(status_code=404, detail=f"Library path not found: {lib_path}")

    cli = CalibreCLI(lib_path)
    books = cli.list_books()
    if req.limit:
        books = books[: req.limit]

    run_id = f"verify_{datetime.now().strftime('%Y%m%d_%H%M%S')}"
    started_at = datetime.now(UTC)
    metrics = get_metrics()
    metrics.set_run_progress(run_id, total=len(books), completed=0)

    # Run synchronously in the background (FastAPI threadpool) so the
    # caller can poll /verify/{run_id} for progress.
    _runs[run_id] = {
        "status": "running",
        "started_at": started_at,
        "total": len(books),
        "completed": 0,
        "counts": {"no_change": 0, "suggest_fix": 0, "needs_review": 0, "defer": 0},
        "verdicts": [],
    }

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

                declared = DeclaredMetadata(
                    title=book.get("title"),
                    authors=authors_list,
                    publisher=book.get("publisher"),
                    published_date=book.get("pubdate"),
                    language=book.get("languages"),
                    series=book.get("series"),
                    isbn=(book.get("identifiers") or {}).get("isbn") if book.get("identifiers") else None,
                )
                observed = ObservationSet(
                    title_page_text=snippet_text[:5000] if snippet_text else None,
                    body_sample=snippet_text[:10000] if snippet_text else None,
                    title_extracted=heuristics.get("title"),
                    authors_extracted=heuristics.get("authors") or [],
                    isbn_extracted=(heuristics.get("identifiers") or {}).get("isbn"),
                    publisher_extracted=heuristics.get("publisher"),
                    date_extracted=heuristics.get("published_date"),
                    language_detected=heuristics.get("language"),
                    evidence_quality="high" if len(snippet_text) > 500 else "low",
                )

                verdict = engine.verify(
                    book_key=book_key,
                    run_id=run_id,
                    declared=declared,
                    observed=observed,
                )

                _runs[run_id]["verdicts"].append(verdict.model_dump())
                _runs[run_id]["counts"][verdict.action.value] = _runs[run_id]["counts"].get(verdict.action.value, 0) + 1
                metrics.record_book_action(verdict.action.value, run_id)
                await store.mark_completed(book_key)
                _runs[run_id]["completed"] = i
                metrics.set_run_progress(run_id, total=len(books), completed=i)
            except Exception as e:  # pragma: no cover — defensive
                logger.warning("Verify failed for book %s: %s", book.get("id"), e)
                await store.mark_failed(f"calibre:{book['id']}")

        _runs[run_id]["status"] = "completed"
        _runs[run_id]["finished_at"] = datetime.now(UTC)
        logger.info("Verify run %s completed: %s", run_id, _runs[run_id]["counts"])

    # Fire-and-forget; the run id is returned to the caller
    asyncio.create_task(_run())

    return VerifyStartResponse(
        run_id=run_id,
        started_at=started_at,
        total=len(books),
        status="running",
    )


@router.get("/runs")
async def list_verify_runs() -> dict[str, Any]:
    """List all verify runs with summary stats."""
    runs_summary = []
    for rid, r in _runs.items():
        finished_at = r.get("finished_at")
        runs_summary.append(
            {
                "run_id": rid,
                "status": r["status"],
                "started_at": r["started_at"].isoformat(),
                "finished_at": finished_at.isoformat() if finished_at else None,
                "total": r["total"],
                "completed": r["completed"],
                "counts": r["counts"],
            }
        )
    return {"runs": runs_summary}


@router.get("/{run_id}")
async def get_verify_run(run_id: str) -> dict[str, Any]:
    """Get progress + per-book verdicts for a verify run."""
    if run_id not in _runs:
        raise HTTPException(status_code=404, detail=f"Verify run not found: {run_id}")
    r = _runs[run_id]
    finished_at = r.get("finished_at")
    return {
        "run_id": run_id,
        "status": r["status"],
        "started_at": r["started_at"].isoformat(),
        "finished_at": finished_at.isoformat() if finished_at else None,
        "total": r["total"],
        "completed": r["completed"],
        "counts": r["counts"],
        "verdicts": r["verdicts"],
    }

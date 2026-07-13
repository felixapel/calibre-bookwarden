"""Legacy v0.9 audit engine — shim that runs v1.0 verification per book.

The original implementation called `build_evidence_package` (v0.9) +
`MetadataJudge.judge` (v0.9 LLM judge) + `apply_confidence_thresholds` (v0.9).
All three are superseded by the v1.0 ContentVerificationEngine + LLMWitness.

This module now re-exports a `run_audit` function that:
  1. Iterates over BookRecords with the given run_id
  2. Builds ObservationSet + DeclaredMetadata per book from DB state
  3. Runs ContentVerificationEngine.verify on each
  4. Optionally calls LLMWitness for ambiguous fields
  5. Persists BookVerdict back into EvidencePackage.decision

Kept for backward compatibility with:
  - cli/main.py:audit (legacy CLI command)
  - web/api/audit.py (legacy /audit endpoint)
  - web/api/bridges.py (paperless webhook)
  - web/jobs.py (legacy job dispatch)
  - ingest/single_file.py (file watcher)
  - tests/test_judge.py (legacy judge test)

New code should use `verification.engine.ContentVerificationEngine` directly
or call `bookaudit verify` / the `/api/verify` endpoint.
"""

import logging
from typing import Any

from sqlmodel import Session, select

from calibre_ai_auditor.comics.pipeline import enrich_comic_observations
from calibre_ai_auditor.config.settings import Settings
from calibre_ai_auditor.extractors.heuristics import extract_heuristics
from calibre_ai_auditor.extractors.text import extract_snippets
from calibre_ai_auditor.storage.db import get_engine
from calibre_ai_auditor.storage.models import BookRecord, EvidencePackage
from calibre_ai_auditor.verification.engine import (
    ContentVerificationEngine,
    DeclaredMetadata,
    ObservationSet,
)
from calibre_ai_auditor.verification.metrics import get_metrics

logger = logging.getLogger(__name__)


async def _build_observation_set(book: BookRecord, settings: Settings) -> ObservationSet:
    """Extract content from the book's first available file and build an ObservationSet.
    Uses central enrich_comic_observations pipeline for ComicInfo + vision (manga) + Komf.
    """
    snippet_text = ""
    file_path = None
    for fmt in (book.files or [])[:1]:
        file_path_data = fmt.get("path") if isinstance(fmt, dict) else None
        if not file_path_data:
            continue
        from pathlib import Path

        file_path = Path(file_path_data)
        if file_path.exists():
            snippets = extract_snippets(file_path)
            snippet_text = "\n".join(s.text for s in snippets)
            break

    heuristics = extract_heuristics([{"text": snippet_text, "source": "first_pages"}] if snippet_text else [])

    # Use pipeline (replaces all scattered comic merge / extract_cbz logic)
    raw_declared = {
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
    raw_observed = dict(raw_declared)

    is_cbz = bool(file_path and file_path.suffix.lower() in (".cbz", ".cbr"))
    enriched_decl, enriched_obs = await enrich_comic_observations(
        settings, file_path if is_cbz else None, None, raw_declared, raw_observed
    )

    # Update heuristics from enriched for ObservationSet
    for k in (
        "title",
        "authors",
        "publisher",
        "published_date",
        "language",
        "series",
        "series_index",
        "isbn",
        "volume",
        "chapter",
        "series_position",
    ):
        if k in enriched_obs and enriched_obs[k] is not None:
            heuristics[k] = enriched_obs[k]

    cover_v = enriched_obs.get("cover_vision")

    obs = ObservationSet(
        title_page_text=snippet_text[:5000] if snippet_text else None,
        body_sample=snippet_text[:10000] if snippet_text else None,
        title_extracted=heuristics.get("title"),
        authors_extracted=heuristics.get("authors") or [],
        isbn_extracted=(heuristics.get("identifiers") or {}).get("isbn"),
        publisher_extracted=heuristics.get("publisher"),
        date_extracted=heuristics.get("published_date"),
        language_detected=heuristics.get("language"),
        series_extracted=heuristics.get("series"),
        series_index_extracted=heuristics.get("series_index"),
        volume_extracted=heuristics.get("volume"),
        chapter_extracted=heuristics.get("chapter"),
        series_position_extracted=heuristics.get("series_position"),
        cover_vision=cover_v,
        evidence_quality="high" if len(snippet_text) > 500 else "low",
    )
    return obs


async def _build_declared(book: BookRecord, settings: Settings) -> DeclaredMetadata:
    """Build declared metadata, including enrichment from the comic pipeline."""
    current_meta = book.current_metadata or {}
    raw_decl = {
        "title": current_meta.get("title"),
        "authors": current_meta.get("authors") or [],
        "publisher": current_meta.get("publisher"),
        "published_date": current_meta.get("pubdate"),
        "language": current_meta.get("languages"),
        "series": current_meta.get("series"),
        "series_index": current_meta.get("series_index"),
        "isbn": (current_meta.get("identifiers") or {}).get("isbn") if current_meta.get("identifiers") else None,
        "volume": current_meta.get("volume"),
        "chapter": current_meta.get("chapter"),
        "series_position": current_meta.get("series_position"),
    }
    # determine file for pipeline (comic extract + vision/komf)
    file_path = None
    for fmt in (book.files or [])[:1]:
        fp = fmt.get("path") if isinstance(fmt, dict) else None
        if fp:
            from pathlib import Path

            p = Path(fp)
            if p.exists():
                file_path = p
                break
    is_cbz = bool(file_path and file_path.suffix.lower() in (".cbz", ".cbr"))
    enriched_decl, _ = await enrich_comic_observations(settings, file_path if is_cbz else None, None, raw_decl, {})
    return DeclaredMetadata(
        title=enriched_decl.get("title"),
        authors=enriched_decl.get("authors") or [],
        publisher=enriched_decl.get("publisher"),
        published_date=enriched_decl.get("published_date"),
        language=enriched_decl.get("language"),
        series=enriched_decl.get("series"),
        series_index=enriched_decl.get("series_index"),
        volume=enriched_decl.get("volume"),
        chapter=enriched_decl.get("chapter"),
        series_position=enriched_decl.get("series_position"),
        isbn=enriched_decl.get("isbn"),
    )


async def run_audit(
    settings: Settings,
    run_id: str,
    judge: bool = True,
    save_evidence: bool = True,
    progress_callback: Any | None = None,
) -> None:
    """Legacy v0.9 audit entry point, now backed by v1.0 ContentVerificationEngine.

    Iterates over BookRecords with `run_id`, builds per-book observations,
    runs the v1.0 engine, optionally calls the LLM witness for ambiguous fields,
    and persists the BookVerdict back into EvidencePackage.decision (legacy shape).
    """
    sql_engine = get_engine(settings)
    verify_engine = ContentVerificationEngine()
    metrics = get_metrics()

    with Session(sql_engine) as session:
        book_stmt = select(BookRecord).where(BookRecord.run_id == run_id)
        books = session.exec(book_stmt).all()

        total = len(books)
        for i, book in enumerate(books):
            logger.info(f"Auditing book {i + 1}/{total}: {(book.current_metadata or {}).get('title')}")
            if progress_callback:
                progress_callback(i + 1, total)

            try:
                declared = await _build_declared(book, settings)
                observed = await _build_observation_set(book, settings)
                verdict = verify_engine.verify(
                    book_key=book.book_key,
                    run_id=run_id,
                    declared=declared,
                    observed=observed,
                )

                # Optional LLM witness for ambiguous fields (if judge=True and providers exist)
                if judge:
                    try:
                        from calibre_ai_auditor.llm.router import LLMRouter
                        from calibre_ai_auditor.verification.engine_llm import (
                            LLMWitness,
                            WitnessConfig,
                        )

                        router = LLMRouter(settings)
                        witness = LLMWitness(router, WitnessConfig(cache_responses=True))
                        for fname, fv in list(verdict.field_verdicts.items()):
                            if fv.verdict.value == "ambiguous":
                                res = await witness.witness_field(
                                    field_name=fname,
                                    current_fv=fv,
                                )
                                if res.success and res.refined_verdict:
                                    verdict.field_verdicts[fname] = res.refined_verdict
                                if res.judge_call:
                                    metrics.record_llm_call(
                                        provider=res.judge_call.provider,
                                        model=res.judge_call.model,
                                        duration_ms=res.judge_call.duration_ms,
                                        tokens_in=res.judge_call.tokens_in,
                                        tokens_out=res.judge_call.tokens_out,
                                    )
                    except Exception as witness_err:
                        # LLM witness is optional; deterministic verdict is enough
                        logger.debug("LLM witness skipped: %s", witness_err)

                metrics.record_book_action(verdict.action.value, run_id)
                book.status = verdict.action.value
                if save_evidence:
                    # Build a legacy-shape EvidencePackage for back-compat
                    old_ev = session.exec(
                        select(EvidencePackage)
                        .where(EvidencePackage.book_key == book.book_key)
                        .where(EvidencePackage.run_id == run_id)
                    ).first()
                    if old_ev:
                        session.delete(old_ev)
                    session.add(
                        EvidencePackage(
                            evidence_id=f"ev_{run_id}_{book.book_key.replace(':', '_')}",
                            book_key=book.book_key,
                            run_id=run_id,
                            current=book.current_metadata or {},
                            extracted=verdict.model_dump(),
                            decision=verdict.model_dump(),
                        )
                    )
                session.add(book)
                session.commit()
            except Exception as e:
                logger.error(f"Failed to audit book {book.book_key}: {e}")
                continue

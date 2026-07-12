"""Ingest and audit a single filesystem path without scanning the whole library."""

import logging
from datetime import datetime
from pathlib import Path
from typing import Any

from sqlmodel import Session, select

from calibre_ai_auditor.audit.engine import run_audit
from calibre_ai_auditor.comics.pipeline import enrich_comic_observations
from calibre_ai_auditor.config.settings import Settings
from calibre_ai_auditor.extractors.heuristics import extract_heuristics
from calibre_ai_auditor.extractors.text import extract_snippets
from calibre_ai_auditor.storage.db import get_engine
from calibre_ai_auditor.storage.models import BookRecord, Run

logger = logging.getLogger(__name__)

SUPPORTED_EXTENSIONS = {".epub", ".pdf", ".mobi", ".azw3", ".cbz", ".cbr"}


async def audit_ingested_file(settings: Settings, file_path: Path) -> dict[str, Any]:
    """
    Register one file under a dedicated ingest run and audit only that book record.
    """
    resolved = file_path.resolve()
    if not resolved.exists() or not resolved.is_file():
        raise ValueError(f"File not found: {file_path}")
    if resolved.suffix.lower() not in SUPPORTED_EXTENSIONS:
        raise ValueError(f"Unsupported file type: {resolved.suffix}")

    run_id = f"run_ingest_{datetime.now().strftime('%Y%m%d_%H%M%S')}"
    book_key = f"path:{resolved}"

    snippets = extract_snippets(resolved)
    extracted = extract_heuristics([s.model_dump() for s in snippets])
    # Use central pipeline (replaces scattered C3 logic; calls vision + Komf if enabled)
    raw_decl = {
        "title": extracted.get("title"),
        "authors": extracted.get("authors") or [],
        "publisher": extracted.get("publisher"),
        "published_date": extracted.get("published_date"),
        "language": extracted.get("language"),
        "series": extracted.get("series"),
        "series_index": extracted.get("series_index"),
        "isbn": extracted.get("isbn"),
        "volume": extracted.get("volume"),
        "chapter": extracted.get("chapter"),
        "series_position": extracted.get("series_position"),
    }
    raw_obs = dict(raw_decl)
    enriched_decl, enriched_obs = await enrich_comic_observations(
        settings, resolved if resolved.suffix.lower() in (".cbz", ".cbr") else None, None, raw_decl, raw_obs
    )
    for k in list(enriched_obs.keys()):
        if enriched_obs[k] is not None:
            extracted[k] = enriched_obs[k]
    current_metadata: dict[str, Any] = {
        "title": extracted.get("title") or resolved.stem,
        "authors": extracted.get("authors", []),
        "identifiers": extracted.get("identifiers", {}),
        # Comic fields (v1.1) — populated here or via ComicInfo in C3 + cover vision; pull for declared
        "volume": extracted.get("volume"),
        "chapter": extracted.get("chapter"),
        "series_position": extracted.get("series_position"),
        "series": extracted.get("series"),
        "series_index": extracted.get("series_index"),
    }

    engine = get_engine(settings)
    with Session(engine) as session:
        new_run = Run(run_id=run_id, status="started")
        session.add(new_run)

        existing = session.exec(select(BookRecord).where(BookRecord.book_key == book_key)).first()
        file_info = {
            "path": str(resolved),
            "format": resolved.suffix.lstrip(".").lower(),
            "size_bytes": resolved.stat().st_size,
        }
        if existing:
            existing.run_id = run_id
            existing.current_metadata = current_metadata
            existing.files = [file_info]
            existing.status = "scanned"
            existing.source = "direct_path"
            session.add(existing)
        else:
            session.add(
                BookRecord(
                    book_key=book_key,
                    run_id=run_id,
                    source="direct_path",
                    current_metadata=current_metadata,
                    files=[file_info],
                    status="scanned",
                )
            )
        session.commit()

    logger.info("Ingest audit started for %s (run %s)", resolved, run_id)
    await run_audit(settings, run_id, judge=True, save_evidence=True)

    with Session(engine) as session:
        run: Run | None = session.exec(select(Run).where(Run.run_id == run_id)).first()
        if run is not None:
            run.status = "completed"
            session.add(run)
            session.commit()
        book = session.exec(select(BookRecord).where(BookRecord.book_key == book_key)).first()

    return {
        "run_id": run_id,
        "book_key": book_key,
        "status": book.status if book else "unknown",
    }

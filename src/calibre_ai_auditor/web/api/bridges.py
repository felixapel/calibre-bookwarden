import logging
from datetime import datetime
from typing import Any

from fastapi import APIRouter, Depends, HTTPException, Query, Request
from sqlmodel import Session, select

from calibre_ai_auditor.config.settings import Settings, load_settings
from calibre_ai_auditor.storage.db import get_engine
from calibre_ai_auditor.storage.models import BookRecord, Run
from calibre_ai_auditor.web.api.books import get_session
from calibre_ai_auditor.web.auth import verify_paperless_webhook
from calibre_ai_auditor.web.jobs import start_job
from calibre_ai_auditor.web.schemas import APIResponse

logger = logging.getLogger(__name__)

router = APIRouter(tags=["Bridges"])


def get_settings() -> Settings:
    return load_settings()


async def do_paperless_webhook_audit(settings: Settings, document_id: int, run_id: str) -> None:
    from pathlib import Path

    from calibre_ai_auditor.audit.engine import run_audit
    from calibre_ai_auditor.integrations.paperless import PaperlessBridge

    logger.info(f"Starting background Paperless webhook audit for doc {document_id} in run {run_id}")

    bridge = PaperlessBridge(settings)
    if not settings.paperless.enabled:
        raise ValueError("Paperless integration is disabled in settings")

    # Ingest document
    doc = await bridge.get_document(document_id)
    if not doc:
        raise ValueError(f"Document {document_id} not found or accessible in Paperless-ngx")

    title = doc.get("title", f"Paperless Document {document_id}")
    import_dir = Path(settings.storage.artifacts_dir) / "paperless_imports"

    file_path = await bridge.download_document_file(document_id, import_dir)
    if not file_path:
        raise ValueError(f"Failed to download file for document {document_id}")

    book_key = f"paperless:{document_id}"

    engine = get_engine(settings)
    with Session(engine) as session:
        # Check if Run exists and update it to completed
        run_stmt = select(Run).where(Run.run_id == run_id)
        run = session.exec(run_stmt).first()
        if run:
            run.status = "completed"
            session.add(run)

        # Check for existing BookRecord
        existing = session.exec(select(BookRecord).where(BookRecord.book_key == book_key)).first()

        file_info = {
            "path": str(file_path),
            "format": file_path.suffix.lstrip(".").lower(),
            "size_bytes": file_path.stat().st_size if file_path.exists() else None,
        }

        current_meta = {
            "title": title,
            "authors": [],
            "publisher": None,
            "published_date": doc.get("created"),
            "tags": doc.get("tags", []),
        }

        if existing:
            existing.run_id = run_id
            existing.current_metadata = current_meta
            existing.files = [file_info]
            existing.status = "scanned"
            existing.paperless_document_id = document_id
            session.add(existing)
        else:
            record = BookRecord(
                book_key=book_key,
                run_id=run_id,
                paperless_document_id=document_id,
                source="paperless",
                current_metadata=current_meta,
                files=[file_info],
            )
            session.add(record)

        session.commit()

    # Trigger the audit engine on this run_id
    await run_audit(settings, run_id, judge=True, save_evidence=True)
    logger.info(f"Completed background Paperless webhook audit for doc {document_id} in run {run_id}")


@router.post("/bridges/paperless/webhook", response_model=APIResponse)
async def paperless_webhook(
    request: Request,
    document_id_query: int | None = Query(None, alias="document_id"),
    id_query: int | None = Query(None, alias="id"),
    document_query: int | None = Query(None, alias="document"),
    session: Session = Depends(get_session),
    settings: Settings = Depends(get_settings),
) -> Any:
    verify_paperless_webhook(request, settings)

    # doc_id can be int or str (parsed from JSON body, then int()'d later)
    doc_id: int | str | None = None

    # Check queries first
    if document_id_query is not None:
        doc_id = document_id_query
    elif id_query is not None:
        doc_id = id_query
    elif document_query is not None:
        doc_id = document_query

    # Try parsing JSON body if queries not set
    if doc_id is None:
        try:
            body_bytes = await request.body()
            if body_bytes:
                import json

                payload = json.loads(body_bytes)
                if isinstance(payload, dict):
                    if "document_id" in payload:
                        doc_id = payload["document_id"]
                    elif "id" in payload:
                        doc_id = payload["id"]
                    elif "document" in payload:
                        val = payload["document"]
                        if isinstance(val, dict) and "id" in val:
                            doc_id = val["id"]
                        elif isinstance(val, (int, str)):
                            doc_id = val
        except Exception:
            pass

    if doc_id is None:
        raise HTTPException(status_code=400, detail="Could not extract document ID from payload or query parameters")

    try:
        doc_id = int(doc_id)
    except (ValueError, TypeError):
        raise HTTPException(status_code=400, detail="Invalid document ID type; must be an integer") from None

    # Generate a run ID
    run_id = f"run_paperless_webhook_{doc_id}_{datetime.now().strftime('%Y%m%d_%H%M%S')}"

    # Record the start of the Run
    run = Run(run_id=run_id, status="started")
    session.add(run)
    session.commit()

    # Trigger job
    job_id = await start_job(f"paperless_webhook:{doc_id}", do_paperless_webhook_audit, settings, doc_id, run_id)

    return {
        "status": "success",
        "data": {"job_id": job_id, "run_id": run_id, "message": "Paperless webhook task scheduled"},
    }

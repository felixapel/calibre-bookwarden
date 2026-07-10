from typing import Any
from urllib.parse import unquote

from fastapi import APIRouter, Depends, HTTPException
from fastapi.responses import HTMLResponse, Response
from sqlmodel import Session, desc, select

from calibre_ai_auditor.config.settings import Settings, load_settings
from calibre_ai_auditor.preview.context import build_review_context
from calibre_ai_auditor.preview.renderer import PreviewRenderer
from calibre_ai_auditor.storage.models import BookRecord, EvidencePackage
from calibre_ai_auditor.web.api.books import get_session

router = APIRouter(prefix="/preview", tags=["Preview and Reports"])


def get_settings() -> Settings:
    return load_settings()


def _decode_book_key(book_key: str) -> str:
    return unquote(book_key)


@router.get("/{book_key}", response_class=HTMLResponse)
async def preview_html(
    book_key: str,
    session: Session = Depends(get_session),
    settings: Settings = Depends(get_settings),
) -> Any:
    key = _decode_book_key(book_key)
    book = session.exec(select(BookRecord).where(BookRecord.book_key == key)).first()
    if not book:
        raise HTTPException(status_code=404, detail="Book not found")

    package = session.exec(
        select(EvidencePackage).where(EvidencePackage.book_key == key).order_by(desc(EvidencePackage.created_at))
    ).first()

    renderer = PreviewRenderer()
    context = build_review_context(book, package)
    html_content = await renderer.render_html("review_packet.html", context)
    return HTMLResponse(content=html_content)


@router.get("/{book_key}.pdf", response_class=Response)
async def preview_pdf(
    book_key: str,
    session: Session = Depends(get_session),
    settings: Settings = Depends(get_settings),
) -> Any:
    if not settings.preview.gotenberg_enabled:
        raise HTTPException(status_code=400, detail="Gotenberg is disabled")

    key = _decode_book_key(book_key)
    book = session.exec(select(BookRecord).where(BookRecord.book_key == key)).first()
    if not book:
        raise HTTPException(status_code=404, detail="Book not found")

    package = session.exec(select(EvidencePackage).where(EvidencePackage.book_key == key)).first()

    renderer = PreviewRenderer(settings.preview.gotenberg_enabled, settings.preview.gotenberg_url)
    context = build_review_context(book, package)
    html_content = await renderer.render_html("review_packet.html", context)
    pdf_bytes = await renderer.render_pdf(html_content)

    if not pdf_bytes:
        raise HTTPException(status_code=500, detail="Failed to generate PDF")

    safe_name = key.replace(":", "_")
    return Response(
        content=pdf_bytes,
        media_type="application/pdf",
        headers={"Content-Disposition": f"attachment; filename={safe_name}_review.pdf"},
    )

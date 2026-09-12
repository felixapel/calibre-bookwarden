import logging
import tempfile
from pathlib import Path
from typing import Any

import jinja2
from fastapi import APIRouter, Depends, File, HTTPException, Query, UploadFile
from fastapi.responses import FileResponse, HTMLResponse
from pydantic import BaseModel

from calibre_ai_auditor.calibre.direct_engine import DirectCalibreEngine, _safe_path
from calibre_ai_auditor.config.settings import Settings, load_settings
from calibre_ai_auditor.covers.extractor import UnifiedCoverExtractor
from calibre_ai_auditor.covers.scorer import CoverQualityScorer
from calibre_ai_auditor.covers.spurious_detector import SpuriousCoverDetector
from calibre_ai_auditor.web.schemas import APIResponse

logger = logging.getLogger(__name__)

router = APIRouter(prefix="/covers", tags=["Covers and CQS"])

TEMPLATES_DIR = Path(__file__).resolve().parent.parent / "templates"
jinja_env = jinja2.Environment(loader=jinja2.FileSystemLoader(str(TEMPLATES_DIR)), autoescape=True)


def get_settings() -> Settings:
    return load_settings()


class CoverScoreRequest(BaseModel):
    cover_path: str


class ExtractNativeRequest(BaseModel):
    book_file_path: str
    target_cover_path: str


@router.post("/score", response_model=APIResponse)
async def score_cover(
    payload: CoverScoreRequest,
    settings: Settings = Depends(get_settings),
) -> Any:
    """Computes Cover Quality Score (CQS 0-100) and spurious cover detection for an on-disk image."""
    p = Path(payload.cover_path).resolve()
    if not p.is_file():
        raise HTTPException(status_code=404, detail=f"Cover file not found: {p}")
    if p.suffix.lower() not in (".jpg", ".jpeg", ".png", ".webp"):
        raise HTTPException(
            status_code=400,
            detail="Target file must be a supported image format (.jpg, .jpeg, .png, .webp)",
        )

    lib_path = settings.library.path.resolve() if settings.library.path else None
    if lib_path and not p.is_relative_to(lib_path):
        raise HTTPException(
            status_code=403,
            detail="Access denied: cover path must reside within the configured library path",
        )

    scorer = CoverQualityScorer()
    detector = SpuriousCoverDetector()

    score_res = scorer.score_image(p)
    spurious_res = detector.inspect(p)

    return {
        "status": "success",
        "data": {
            "cqs": {
                "score": score_res.cqs,
                "tier": score_res.tier,
                "width": score_res.width,
                "height": score_res.height,
                "aspect_ratio": score_res.aspect_ratio,
                "dimension_score": score_res.dimension_score,
                "sharpness_score": score_res.sharpness_score,
                "contrast_score": score_res.contrast_score,
                "cleanliness_score": score_res.cleanliness_score,
                "penalties": score_res.penalties,
                "fatal_defects": score_res.fatal_defects,
                "is_actionable": score_res.is_actionable,
            },
            "spurious": {
                "is_spurious": spurious_res.is_spurious,
                "defect_type": spurious_res.defect_type,
                "confidence": spurious_res.confidence,
                "details": spurious_res.details,
            },
        },
    }


@router.post("/score-upload", response_model=APIResponse)
async def score_uploaded_cover(file: UploadFile = File(...)) -> Any:
    """Scores an uploaded cover file in-memory or temporary storage."""
    # DoS guard: stream with hard cap instead of unbounded read().
    max_bytes = 25 * 1024 * 1024
    # Must close handle before reading with PIL on Windows to prevent WinError 32
    tmp = tempfile.NamedTemporaryFile(suffix=".jpg", delete=False)  # noqa: SIM115
    tmp_path = Path(tmp.name)
    try:
        total = 0
        while True:
            chunk = await file.read(1024 * 1024)
            if not chunk:
                break
            total += len(chunk)
            if total > max_bytes:
                raise HTTPException(status_code=413, detail="Upload too large (max 25MB)")
            tmp.write(chunk)
        tmp.flush()
        tmp.close()
        if total == 0:
            raise HTTPException(status_code=400, detail="Empty upload")

        scorer = CoverQualityScorer()
        detector = SpuriousCoverDetector()

        score_res = scorer.score_image(tmp_path)
        spurious_res = detector.inspect(tmp_path)

        return {
            "status": "success",
            "data": {
                "filename": file.filename,
                "cqs": {
                    "score": score_res.cqs,
                    "tier": score_res.tier,
                    "width": score_res.width,
                    "height": score_res.height,
                    "aspect_ratio": score_res.aspect_ratio,
                    "penalties": score_res.penalties,
                    "fatal_defects": score_res.fatal_defects,
                    "is_actionable": score_res.is_actionable,
                },
                "spurious": {
                    "is_spurious": spurious_res.is_spurious,
                    "defect_type": spurious_res.defect_type,
                    "confidence": spurious_res.confidence,
                },
            },
        }
    finally:
        if tmp_path.exists():
            tmp_path.unlink(missing_ok=True)


@router.get("/deck", response_model=APIResponse)
async def get_cover_review_deck(
    limit: int = Query(default=30, ge=1, le=100),
    settings: Settings = Depends(get_settings),
) -> Any:
    """Returns a curated queue of books needing cover attention ('Cover Tinder' deck)."""
    lib_path = settings.library.path
    if not lib_path or not (lib_path / "metadata.db").exists():
        return {
            "status": "success",
            "data": {"deck": [], "message": "No Calibre library configured"},
        }

    engine = DirectCalibreEngine(lib_path)
    deck: list[dict[str, Any]] = []

    # Stream books using low-memory keyset pagination
    for book in engine.stream_books(batch_size=100):
        if len(deck) >= limit:
            break

        path_rel = book.get("path")
        if not path_rel:
            continue

        book_dir = lib_path / path_rel
        cover_path = book_dir / "cover.jpg"

        if not cover_path.exists():
            deck.append(
                {
                    "book_id": book["id"],
                    "title": book["title"],
                    "authors": book.get("authors"),
                    "issue": "missing_cover",
                    "cqs_tier": "Tier D",
                    "cqs_score": 0,
                    "cover_path": None,
                }
            )
            continue

        # Score cover
        score_res = CoverQualityScorer().score_image(cover_path)
        if score_res.is_actionable or score_res.cqs < 60:
            spurious_res = SpuriousCoverDetector().inspect(cover_path)
            deck.append(
                {
                    "book_id": book["id"],
                    "title": book["title"],
                    "authors": book.get("authors"),
                    "issue": spurious_res.defect_type or "low_quality",
                    "cqs_tier": score_res.tier,
                    "cqs_score": score_res.cqs,
                    "dimensions": f"{score_res.width}x{score_res.height}",
                    "cover_path": str(cover_path),
                    "penalties": score_res.penalties,
                }
            )

    return {
        "status": "success",
        "data": {
            "deck": deck,
            "count": len(deck),
        },
    }


@router.post("/extract-native", response_model=APIResponse)
async def extract_native_cover(
    payload: ExtractNativeRequest,
    settings: Settings = Depends(get_settings),
) -> Any:
    """Extracts native cover from EPUB, PDF, or Comic archive into the target path."""
    book_file = Path(payload.book_file_path).resolve()
    if not book_file.is_file():
        raise HTTPException(status_code=404, detail=f"Book file not found: {book_file}")
    if book_file.suffix.lower() not in (".epub", ".pdf", ".cbz", ".cbr", ".mobi", ".azw3"):
        raise HTTPException(
            status_code=400,
            detail="Unsupported book file format for cover extraction",
        )

    target = Path(payload.target_cover_path).resolve()
    if target.suffix.lower() not in (".jpg", ".jpeg", ".png", ".webp"):
        raise HTTPException(
            status_code=400,
            detail="Target cover path must have an image extension (.jpg, .jpeg, .png, .webp)",
        )

    lib_path = settings.library.path.resolve() if settings.library.path else None
    if lib_path:
        if not book_file.is_relative_to(lib_path):
            raise HTTPException(
                status_code=403,
                detail="Access denied: book file must reside within the configured library path",
            )
        if not target.is_relative_to(lib_path):
            raise HTTPException(
                status_code=403,
                detail="Access denied: target cover path must reside within the configured library path",
            )

    success = UnifiedCoverExtractor.extract(book_file, target)

    if not success:
        raise HTTPException(
            status_code=422,
            detail=f"Could not extract native cover from {book_file.name}",
        )

    score_res = CoverQualityScorer().score_image(target)
    return {
        "status": "success",
        "data": {
            "extracted": True,
            "target_path": str(target),
            "cqs": {
                "score": score_res.cqs,
                "tier": score_res.tier,
                "width": score_res.width,
                "height": score_res.height,
            },
        },
    }


@router.get("/book/{book_id}/image")
async def get_book_cover_image(
    book_id: int,
    settings: Settings = Depends(get_settings),
) -> FileResponse:
    """Directly serves the on-disk cover.jpg for a given book ID."""
    lib_path = settings.library.path
    if not lib_path or not (lib_path / "metadata.db").exists():
        raise HTTPException(status_code=404, detail="Library metadata.db not found")

    engine = DirectCalibreEngine(lib_path)
    conn = engine.get_connection(read_only=True)
    try:
        c = conn.cursor()
        c.execute("SELECT path FROM books WHERE id = ?", (book_id,))
        row = c.fetchone()
        if not row or not row["path"]:
            raise HTTPException(status_code=404, detail=f"Book {book_id} not found")
        cov_file = _safe_path(lib_path / row["path"] / "cover.jpg")
        if not cov_file.exists():
            raise HTTPException(status_code=404, detail="Cover image not found on disk")
        return FileResponse(str(cov_file), media_type="image/jpeg")
    finally:
        conn.close()


@router.get("/ui/deck", response_class=HTMLResponse)
async def get_cover_deck_ui(
    settings: Settings = Depends(get_settings),
) -> HTMLResponse:
    """Renders the interactive Cover Deck HTMX interface for rapid cover review."""
    deck_resp = await get_cover_review_deck(limit=30, settings=settings)
    deck_items = deck_resp.get("data", {}).get("deck", [])

    item = deck_items[0] if deck_items else None
    if item and item.get("cover_path"):
        item["has_current_image"] = True
    elif item:
        item["has_current_image"] = False

    template = jinja_env.get_template("cover_deck.html")
    html_content = template.render(
        item=item,
        index=0,
        total=len(deck_items),
    )
    return HTMLResponse(content=html_content)


@router.post("/ui/deck/action", response_class=HTMLResponse)
async def handle_cover_deck_action(
    action: str = Query(..., pattern="^(skip|apply)$"),
    book_id: int = Query(...),
    candidate: int = Query(1),
    settings: Settings = Depends(get_settings),
) -> HTMLResponse:
    """Handles apply or skip action via HTMX and returns the next card in the deck."""
    deck_resp = await get_cover_review_deck(limit=30, settings=settings)
    deck_items = deck_resp.get("data", {}).get("deck", [])

    remaining = [b for b in deck_items if b["book_id"] != book_id]
    next_item = remaining[0] if remaining else None
    if next_item and next_item.get("cover_path"):
        next_item["has_current_image"] = True
    elif next_item:
        next_item["has_current_image"] = False

    template = jinja_env.get_template("cover_deck.html")
    html_content = template.render(
        item=next_item,
        index=0,
        total=len(remaining),
    )
    return HTMLResponse(content=html_content)

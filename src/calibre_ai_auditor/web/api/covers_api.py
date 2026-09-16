import logging
import os
import tempfile
from pathlib import Path
from typing import Any

import jinja2
from fastapi import APIRouter, Depends, File, HTTPException, Query, UploadFile
from fastapi.responses import HTMLResponse, RedirectResponse, Response
from pydantic import BaseModel

from calibre_ai_auditor.calibre.direct_engine import DirectCalibreEngine
from calibre_ai_auditor.config.settings import Settings, load_settings
from calibre_ai_auditor.covers.scorer import CoverQualityScorer
from calibre_ai_auditor.covers.spurious_detector import SpuriousCoverDetector
from calibre_ai_auditor.security.files import SecurePathError, read_file_beneath
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


def _reject_legacy_direct_write(settings: Settings) -> None:
    if settings.library.read_only:
        raise HTTPException(status_code=403, detail="Operation rejected: library is in read-only mode.")
    raise HTTPException(
        status_code=409,
        detail="Direct library writes are retired; submit the change through the supervised writer workflow.",
    )


def _read_book_cover_bytes(library_root: Path, stored_book_path: object) -> bytes | None:
    """Read one stored cover through a rooted descriptor without reopening its path.

    The existing Windows fallback cannot anchor all parent directories against
    junction swaps, so this UI fails closed there until a handle-anchored reader
    is available.
    """
    if not isinstance(stored_book_path, str) or not stored_book_path:
        return None
    if os.name == "nt":
        return None
    try:
        return read_file_beneath(
            library_root,
            library_root / Path(stored_book_path) / "cover.jpg",
            max_bytes=25 * 1024 * 1024,
        )
    except SecurePathError:
        return None


def _inspect_cover_bytes(image_bytes: bytes) -> tuple[Any, Any]:
    """Run the established inspectors only on a private copy of rooted bytes."""
    temporary = tempfile.NamedTemporaryFile(suffix=".jpg", delete=False)  # noqa: SIM115
    temporary_path = Path(temporary.name)
    try:
        temporary.write(image_bytes)
        temporary.flush()
        temporary.close()
        score = CoverQualityScorer().score_image(temporary_path)
        if "corrupt_image" in score.fatal_defects or any(
            penalty.startswith("read_error:") for penalty in score.penalties
        ):
            score.penalties = ["cover_decode_failed"]
        return score, SpuriousCoverDetector().inspect(temporary_path)
    finally:
        if not temporary.closed:
            temporary.close()
        temporary_path.unlink(missing_ok=True)


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
                tmp.close()  # release handle before raise (Windows unlink in finally)
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
    after_book_id: int = Query(default=0, ge=0, le=2_147_483_647),
    settings: Settings = Depends(get_settings),
) -> Any:
    """Return the next read-only page of flagged covers after a monotonic cursor."""
    lib_path = settings.library.path.resolve() if settings.library.path else None
    if not lib_path or not (lib_path / "metadata.db").exists():
        return {
            "status": "success",
            "data": {"deck": [], "message": "No Calibre library configured", "after_book_id": after_book_id},
        }

    engine = DirectCalibreEngine(lib_path)
    deck: list[dict[str, Any]] = []

    # Stream books using low-memory keyset pagination
    for book in engine.stream_books(batch_size=100):
        if len(deck) >= limit:
            break
        book_id = int(book["id"])
        if book_id <= after_book_id:
            continue

        image_bytes = _read_book_cover_bytes(lib_path, book.get("path"))
        if image_bytes is None:
            deck.append(
                {
                    "book_id": book_id,
                    "title": book["title"] or "Untitled",
                    "authors": book.get("authors"),
                    "issue": "missing_cover",
                    "cqs_tier": "Tier D",
                    "cqs_score": 0,
                    "dimensions": None,
                    "penalties": ["cover_unavailable"],
                    "has_current_image": False,
                }
            )
            continue

        # Score cover
        score_res, spurious_res = _inspect_cover_bytes(image_bytes)
        if score_res.is_actionable or score_res.cqs < 60:
            deck.append(
                {
                    "book_id": book_id,
                    "title": book["title"] or "Untitled",
                    "authors": book.get("authors"),
                    "issue": spurious_res.defect_type or "low_quality",
                    "cqs_tier": score_res.tier,
                    "cqs_score": score_res.cqs,
                    "dimensions": f"{score_res.width}x{score_res.height}",
                    "penalties": score_res.penalties,
                    "has_current_image": True,
                }
            )

    return {
        "status": "success",
        "data": {
            "deck": deck,
            "count": len(deck),
            "after_book_id": after_book_id,
            "message": (
                "No flagged cover was found after this cursor; this is not a claim that every library cover is clean."
            )
            if not deck
            else None,
        },
    }


@router.post("/extract-native", response_model=APIResponse)
async def extract_native_cover(
    payload: ExtractNativeRequest,
    settings: Settings = Depends(get_settings),
) -> Any:
    """Reject the retired in-place cover extraction endpoint."""
    _reject_legacy_direct_write(settings)


@router.get("/book/{book_id}/image")
async def get_book_cover_image(
    book_id: int,
    settings: Settings = Depends(get_settings),
) -> Response:
    """Directly serves the on-disk cover.jpg for a given book ID."""
    lib_path = settings.library.path.resolve() if settings.library.path else None
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
        image_bytes = _read_book_cover_bytes(lib_path, row["path"])
        if image_bytes is None:
            raise HTTPException(status_code=404, detail="Cover image not found on disk")
        return Response(content=image_bytes, media_type="image/jpeg")
    finally:
        conn.close()


@router.get("/ui/deck", response_class=HTMLResponse)
async def get_cover_deck_ui(
    after_book_id: int = Query(default=0, ge=0, le=2_147_483_647),
    settings: Settings = Depends(get_settings),
) -> HTMLResponse:
    """Render one read-only flagged cover at a stable cursor."""
    deck_resp = await get_cover_review_deck(limit=1, after_book_id=after_book_id, settings=settings)
    deck_items = deck_resp.get("data", {}).get("deck", [])

    item = deck_items[0] if deck_items else None

    template = jinja_env.get_template("cover_deck.html")
    html_content = template.render(
        item=item,
        after_book_id=after_book_id,
        message=deck_resp.get("data", {}).get("message"),
    )
    return HTMLResponse(content=html_content)


@router.post("/ui/deck/action", response_class=HTMLResponse)
async def handle_cover_deck_action(
    action: str = Query(..., pattern="^(skip|apply)$"),
    book_id: int = Query(..., ge=1, le=2_147_483_647),
    candidate: int = Query(1),
    after_book_id: int = Query(default=0, ge=0, le=2_147_483_647),
    settings: Settings = Depends(get_settings),
) -> RedirectResponse:
    """Advance a review cursor; applying any candidate remains unavailable."""
    if action == "apply":
        _reject_legacy_direct_write(settings)
    if book_id <= after_book_id:
        raise HTTPException(status_code=400, detail="Skip cursor must advance monotonically.")
    return RedirectResponse(url=f"/api/covers/ui/deck?after_book_id={book_id}", status_code=303)

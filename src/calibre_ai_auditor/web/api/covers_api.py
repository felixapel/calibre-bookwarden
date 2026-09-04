import logging
import tempfile
from pathlib import Path
from typing import Any

from fastapi import APIRouter, Depends, File, HTTPException, Query, UploadFile
from pydantic import BaseModel

from calibre_ai_auditor.calibre.direct_engine import DirectCalibreEngine
from calibre_ai_auditor.config.settings import Settings, load_settings
from calibre_ai_auditor.covers.extractor import UnifiedCoverExtractor
from calibre_ai_auditor.covers.scorer import CoverQualityScorer
from calibre_ai_auditor.covers.spurious_detector import SpuriousCoverDetector
from calibre_ai_auditor.web.schemas import APIResponse

logger = logging.getLogger(__name__)

router = APIRouter(prefix="/covers", tags=["Covers and CQS"])


def get_settings() -> Settings:
    return load_settings()


class CoverScoreRequest(BaseModel):
    cover_path: str


class ExtractNativeRequest(BaseModel):
    book_file_path: str
    target_cover_path: str


@router.post("/score", response_model=APIResponse)
async def score_cover(payload: CoverScoreRequest) -> Any:
    """Computes Cover Quality Score (CQS 0-100) and spurious cover detection for an on-disk image."""
    p = Path(payload.cover_path)
    if not p.is_file():
        raise HTTPException(status_code=404, detail=f"Cover file not found: {p}")

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
    with tempfile.NamedTemporaryFile(suffix=".jpg", delete=False) as tmp:
        tmp_path = Path(tmp.name)
        try:
            content = await file.read()
            tmp_path.write_bytes(content)

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
async def extract_native_cover(payload: ExtractNativeRequest) -> Any:
    """Extracts native cover from EPUB, PDF, or Comic archive into the target path."""
    book_file = Path(payload.book_file_path)
    if not book_file.is_file():
        raise HTTPException(status_code=404, detail=f"Book file not found: {book_file}")

    target = Path(payload.target_cover_path)
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

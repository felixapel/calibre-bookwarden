import base64
import hashlib
import logging
from pathlib import Path
from typing import Any, cast

from sqlmodel import Session, select

from calibre_ai_auditor.config.settings import Settings
from calibre_ai_auditor.covers.scorer import CoverQualityScorer
from calibre_ai_auditor.covers.spurious_detector import SpuriousCoverDetector
from calibre_ai_auditor.extractors.cover_hashes import calculate_phash, compare_phashes
from calibre_ai_auditor.llm.router import LLMRouter
from calibre_ai_auditor.llm.schemas import LLMRequest
from calibre_ai_auditor.storage.db import get_engine
from calibre_ai_auditor.storage.models import CoverVisionCache

logger = logging.getLogger(__name__)


def calculate_sha256(file_path: Path) -> str:
    sha256_hash = hashlib.sha256()
    with open(file_path, "rb") as f:
        for byte_block in iter(lambda: f.read(4096), b""):
            sha256_hash.update(byte_block)
    return sha256_hash.hexdigest()


class VisionVerifier:
    def __init__(self, settings: Settings):
        self.settings = settings
        self.router = LLMRouter(settings)

    async def verify_cover(self, cover_path: Path) -> dict[str, Any] | None:
        """
        Sends the cover image to a vision-capable LLM to extract title/author/ISBN details.
        Uses a CoverVisionCache database table to cache responses by SHA-256 and/or phash.
        """
        if not cover_path.exists():
            logger.warning(f"Cover image path does not exist: {cover_path}")
            return None

        # Calculate hashes for caching
        try:
            sha256 = calculate_sha256(cover_path)
        except Exception as e:
            logger.error(f"Failed to calculate SHA-256 for cover image: {e}")
            sha256 = None

        phash = calculate_phash(cover_path)

        if sha256 or phash:
            try:
                engine = get_engine(self.settings)
                cached_response = None
                with Session(engine) as session:
                    # 1. Try exact SHA-256 match
                    if sha256:
                        record = session.exec(select(CoverVisionCache).where(CoverVisionCache.sha256 == sha256)).first()
                        if record:
                            logger.info(f"Cover vision cache hit via SHA-256 for: {cover_path.name}")
                            cached_response = record.response

                    # 2. Try exact phash match
                    if not cached_response and phash:
                        record = session.exec(select(CoverVisionCache).where(CoverVisionCache.phash == phash)).first()
                        if record:
                            logger.info(f"Cover vision cache hit via exact phash for: {cover_path.name}")
                            cached_response = record.response

                    # 3. Try similar phash match (Hamming distance <= 4, bounded scan)
                    if not cached_response and phash:
                        records = session.exec(
                            select(CoverVisionCache)
                            .where(CoverVisionCache.phash.is_not(None))
                            .order_by(CoverVisionCache.id.desc())
                            .limit(150)
                        ).all()
                        for rec in records:
                            if rec.phash and compare_phashes(phash, rec.phash) <= 4:
                                logger.info(
                                    f"Cover vision cache hit via similar phash (distance: "
                                    f"{compare_phashes(phash, rec.phash)}) for: {cover_path.name}"
                                )
                                cached_response = rec.response
                                break

                if cached_response is not None:
                    return cached_response
            except Exception as e:
                logger.error(f"Failed to query CoverVisionCache from database: {e}")

        # Check if the provider supports vision
        try:
            provider = self.router.get_provider_for_task("vision")
            if not provider.supports_vision:
                logger.warning(f"Selected vision provider '{provider.name}' does not support vision.")
                return None
        except Exception as e:
            logger.warning(f"Failed to find vision provider: {e}")
            return None

        # Read and encode image to base64
        try:
            with open(cover_path, "rb") as f:
                img_data = base64.b64encode(f.read()).decode("utf-8")
        except Exception as e:
            logger.error(f"Failed to encode cover image for vision: {e}")
            return None

        # Define schema for structured output (extended for comics/manga vision)
        schema = {
            "type": "object",
            "properties": {
                "title": {"type": ["string", "null"]},
                "authors": {"type": ["array", "null"], "items": {"type": "string"}},
                "publisher": {"type": ["string", "null"]},
                "isbn": {"type": ["string", "null"]},
                "volume": {"type": ["integer", "null"]},
                "chapter": {"type": ["number", "null"]},
                "series_position": {"type": ["number", "null"]},
                "confidence": {"type": "number"},
                "explanation": {"type": "string"},
            },
            "required": [
                "title",
                "authors",
                "publisher",
                "isbn",
                "volume",
                "chapter",
                "series_position",
                "confidence",
                "explanation",
            ],
            "additionalProperties": False,
        }

        # Build prompt (comic-aware if manga_mode)
        is_comic = getattr(self.settings, "manga_mode", None) and self.settings.manga_mode.enabled
        if is_comic:
            system_prompt = (
                "You are a comic/manga cover metadata auditor. Analyze the provided book cover image. "
                "Extract the exact title, author(s), publisher, ISBN if visible, volume number, "
                "chapter number (use decimal), and series position. For manga covers, prioritize "
                "series title, volume/edition from art, and any chapter hints. "
                "Assess your confidence in the extraction (0.0 to 1.0) and explain your reasoning."
            )
        else:
            system_prompt = (
                "You are a cover metadata auditor. Analyze the provided book cover image. "
                "Extract the exact title, author(s), publisher, and ISBN if visible on the cover. "
                "Assess your confidence in the extraction (0.0 to 1.0) and explain your reasoning."
            )

        user_content = [
            {
                "type": "text",
                "text": ("Extract metadata from this book cover. Output strictly as JSON matching the schema."),
            },
            {
                "type": "image_url",
                "image_url": {"url": f"data:image/jpeg;base64,{img_data}"},
            },
        ]

        messages = [
            {"role": "system", "content": system_prompt},
            {"role": "user", "content": cast(Any, user_content)},
        ]

        req = LLMRequest(
            model=self.settings.vision_model,
            messages=messages,
            temperature=0.0,
            json_schema=True,
        )

        try:
            logger.info(f"Running vision cover verification on model '{self.settings.vision_model}'...")
            response = await self.router.execute_structured("vision", req, schema)

            result = None
            if isinstance(response.content, dict):
                result = response.content
            else:
                import json

                result = cast(dict[str, Any], json.loads(response.content))

            if result and (sha256 or phash):
                try:
                    engine = get_engine(self.settings)
                    with Session(engine) as session:
                        cache_record = CoverVisionCache(sha256=sha256, phash=phash, response=result)
                        session.add(cache_record)
                        session.commit()
                        logger.info(f"Cached cover vision response in database for: {cover_path.name}")
                except Exception as cache_err:
                    logger.error(f"Failed to cache cover vision response: {cache_err}")

            if isinstance(result, dict):
                try:
                    score_res = CoverQualityScorer().score_image(cover_path)
                    spurious_res = SpuriousCoverDetector().inspect(cover_path)
                    result["cqs"] = {
                        "score": score_res.cqs,
                        "tier": score_res.tier,
                        "aspect_ratio": score_res.aspect_ratio,
                        "is_actionable": score_res.is_actionable,
                        "penalties": score_res.penalties,
                        "fatal_defects": score_res.fatal_defects,
                    }
                    result["spurious"] = {
                        "is_spurious": spurious_res.is_spurious,
                        "defect_type": spurious_res.defect_type,
                        "confidence": spurious_res.confidence,
                    }
                except Exception as qc_err:
                    logger.debug(f"Cover QC scoring skipped: {qc_err}")

            return result
        except Exception as e:
            logger.error(f"Vision cover verification failed: {e}")
            return None

    async def cross_check_cover_alignment(
        self,
        cover_path: Path,
        expected_title: str,
        expected_authors: list[str] | None = None,
    ) -> dict[str, Any] | None:
        """
        Multimodal cross-check with Gemini 3.8 Flash:
        Verifies if cover art and text legitimately match the declared book title and authors.
        Detects mismatched covers (e.g. Jesus on Prometheus Bound, unrelated textbooks, or broken art).
        """
        if not cover_path.exists():
            return None

        try:
            with open(cover_path, "rb") as f:
                img_data = base64.b64encode(f.read()).decode("utf-8")
        except Exception as e:
            logger.error(f"Failed to encode cover: {e}")
            return None

        authors_str = ", ".join(expected_authors) if expected_authors else "Unknown"
        system_prompt = (
            "You are an expert bibliophile and book cover forensic auditor. "
            "Examine this book cover image and determine if it legitimately belongs to the declared book:\n"
            f"Expected Title: {expected_title}\n"
            f"Expected Author(s): {authors_str}\n\n"
            "Evaluate:\n"
            "1. matches_book (boolean): True if this is an authentic, legitimate cover for this book. "
            "False if it depicts an entirely different book, author, religious mismatch "
            "(e.g. Jesus on Prometheus Bound), or unrelated subject.\n"
            "2. visual_quality (string: 'high', 'medium', 'low', 'unusable'): High if crisp official publisher "
            "cover, low if ugly flat plain text or heavily pixelated thumbnail.\n"
            "3. detected_title (string): Exact title visible on cover.\n"
            "4. detected_author (string): Exact author visible on cover.\n"
            "5. confidence (number: 0.0 to 1.0).\n"
            "6. reason (string): Concise explanation of your judgment."
        )

        schema = {
            "type": "object",
            "properties": {
                "matches_book": {"type": "boolean"},
                "visual_quality": {"type": "string", "enum": ["high", "medium", "low", "unusable"]},
                "detected_title": {"type": "string"},
                "detected_author": {"type": "string"},
                "confidence": {"type": "number"},
                "reason": {"type": "string"},
            },
            "required": [
                "matches_book",
                "visual_quality",
                "detected_title",
                "detected_author",
                "confidence",
                "reason",
            ],
            "additionalProperties": False,
        }

        user_content = [
            {"type": "text", "text": "Analyze whether this cover matches the declared book title and authors."},
            {"type": "image_url", "image_url": {"url": f"data:image/jpeg;base64,{img_data}"}},
        ]

        req = LLMRequest(
            model=self.settings.vision_model,
            messages=[
                {"role": "system", "content": system_prompt},
                {"role": "user", "content": cast(Any, user_content)},
            ],
            temperature=0.0,
            json_schema=True,
        )

        try:
            resp = await self.router.execute_structured("vision", req, schema)
            if isinstance(resp.content, dict):
                resp_data = resp.content
            else:
                import json

                resp_data = cast(dict[str, Any], json.loads(resp.content))

            if isinstance(resp_data, dict):
                try:
                    score_res = CoverQualityScorer().score_image(cover_path)
                    spurious_res = SpuriousCoverDetector().inspect(cover_path)
                    resp_data["cqs"] = {
                        "score": score_res.cqs,
                        "tier": score_res.tier,
                        "is_actionable": score_res.is_actionable,
                    }
                    resp_data["spurious"] = {
                        "is_spurious": spurious_res.is_spurious,
                        "defect_type": spurious_res.defect_type,
                    }
                except Exception as qc_err:
                    logger.debug(f"Cover QC scoring skipped: {qc_err}")

            return resp_data
        except Exception as exc:
            logger.error(f"Cross check cover alignment failed: {exc}")
            return None


async def verify_comic_cover(vision_verifier: VisionVerifier, cover_path: Path) -> dict[str, Any] | None:
    """
    Comic/Manga specific cover verification using vision LLM.
    Extracts series, volume, chapter (decimal), title from cover art.
    Intended for use when ComicInfo.xml is missing or to cross-verify.
    """
    # Reuse the main verifier but the prompt inside is now comic-aware if manga_mode enabled
    result = await vision_verifier.verify_cover(cover_path)
    if not result:
        return None

    # Normalize for comic fields (decimal chapter per Weebarr)
    comic_result = {
        "title": result.get("title"),
        "authors": result.get("authors", []),
        "series": result.get("title"),  # often the series is the main title on cover
        "volume": result.get("volume"),
        "chapter": result.get("chapter"),
        "series_position": result.get("series_position") or result.get("chapter"),
        "confidence": result.get("confidence", 0.5),
        "explanation": result.get("explanation", ""),
    }
    return comic_result

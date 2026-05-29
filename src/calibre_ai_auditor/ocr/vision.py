import base64
import hashlib
import logging
from pathlib import Path
from typing import Any, cast

from sqlmodel import Session, select

from calibre_ai_auditor.config.settings import Settings
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
                # Ensure the cache table exists (e.g. in test environments)
                from sqlmodel import SQLModel
                SQLModel.metadata.create_all(engine)

                cached_response = None
                with Session(engine) as session:
                    # 1. Try exact SHA-256 match
                    if sha256:
                        record = session.exec(
                            select(CoverVisionCache).where(CoverVisionCache.sha256 == sha256)
                        ).first()
                        if record:
                            logger.info(f"Cover vision cache hit via SHA-256 for: {cover_path.name}")
                            cached_response = record.response

                    # 2. Try exact phash match
                    if not cached_response and phash:
                        record = session.exec(
                            select(CoverVisionCache).where(CoverVisionCache.phash == phash)
                        ).first()
                        if record:
                            logger.info(f"Cover vision cache hit via exact phash for: {cover_path.name}")
                            cached_response = record.response

                    # 3. Try similar phash match (Hamming distance <= 4)
                    if not cached_response and phash:
                        records = session.exec(
                            select(CoverVisionCache).where(CoverVisionCache.phash != None)
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
                logger.warning(
                    f"Selected vision provider '{provider.name}' does not support vision."
                )
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

        # Define schema for structured output
        schema = {
            "type": "object",
            "properties": {
                "title": {"type": ["string", "null"]},
                "authors": {"type": ["array", "null"], "items": {"type": "string"}},
                "publisher": {"type": ["string", "null"]},
                "isbn": {"type": ["string", "null"]},
                "confidence": {"type": "number"},
                "explanation": {"type": "string"},
            },
            "required": ["title", "authors", "publisher", "isbn", "confidence", "explanation"],
            "additionalProperties": False,
        }

        # Build prompt
        system_prompt = (
            "You are a cover metadata auditor. Analyze the provided book cover image. "
            "Extract the exact title, author(s), publisher, and ISBN if visible on the cover. "
            "Assess your confidence in the extraction (0.0 to 1.0) and explain your reasoning."
        )

        user_content = [
            {
                "type": "text",
                "text": (
                    "Extract metadata from this book cover. "
                    "Output strictly as JSON matching the schema."
                ),
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
            logger.info(
                f"Running vision cover verification on model '{self.settings.vision_model}'..."
            )
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
                        cache_record = CoverVisionCache(
                            sha256=sha256,
                            phash=phash,
                            response=result
                        )
                        session.add(cache_record)
                        session.commit()
                        logger.info(f"Cached cover vision response in database for: {cover_path.name}")
                except Exception as cache_err:
                    logger.error(f"Failed to cache cover vision response: {cache_err}")

            return result
        except Exception as e:
            logger.error(f"Vision cover verification failed: {e}")
            return None


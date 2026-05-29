import base64
import json
import logging
from pathlib import Path
from typing import Any, cast

from calibre_ai_auditor.config.settings import Settings
from calibre_ai_auditor.llm.router import LLMRouter
from calibre_ai_auditor.llm.schemas import LLMRequest
from calibre_ai_auditor.storage.models import EvidencePackage

logger = logging.getLogger(__name__)


class MetadataJudge:
    def __init__(self, settings: Settings):
        self.settings = settings
        self.router = LLMRouter(settings)

    async def judge(self, package: EvidencePackage) -> dict[str, Any]:
        """
        Calls an LLM to judge the candidates against current metadata and extracted evidence.
        """
        system_prompt = """
        You are a library metadata auditor. Your task is to compare a book's current metadata 
        and extracted text snippets (and optional cover image) against a list of candidates 
        from public providers.

        You must output a valid JSON object following this schema.

        Rules:
        1. Only suggest a fix if confidence is high (>85).
        2. Flag 'author_swap' if the candidate has a different author than current.
        3. Flag 'edition_ambiguous' if snippets don't clearly match candidate publisher/year.
        4. If a cover image is provided, use it to verify the edition/title.
        """

        schema = {
            "type": "object",
            "properties": {
                "recommended_action": {
                    "type": "string",
                    "enum": ["no_change", "suggest_fix", "needs_review", "defer"],
                },
                "confidence": {"type": "integer"},
                "same_work_confidence": {"type": "integer"},
                "same_edition_confidence": {"type": "integer"},
                "selected_candidate_ids": {"type": "array", "items": {"type": "string"}},
                "proposed_patch": {
                    "type": "object",
                    "properties": {
                        "title": {"type": ["string", "null"]},
                        "authors": {"type": ["array", "null"], "items": {"type": "string"}},
                        "publisher": {"type": ["string", "null"]},
                        "published_date": {"type": ["string", "null"]},
                        "identifiers": {
                            "type": ["object", "null"],
                            "additionalProperties": {"type": "string"},
                        },
                    },
                    "additionalProperties": False,
                },
                "reasons": {"type": "array", "items": {"type": "string"}},
                "risk_flags": {
                    "type": "array",
                    "items": {
                        "type": "string",
                        "enum": [
                            "author_swap",
                            "isbn_conflict",
                            "edition_ambiguous",
                            "cover_mismatch",
                        ],
                    },
                },
            },
            "required": [
                "recommended_action",
                "confidence",
                "same_work_confidence",
                "same_edition_confidence",
                "selected_candidate_ids",
                "proposed_patch",
                "reasons",
                "risk_flags",
            ],
            "additionalProperties": False,
        }

        messages = [
            {"role": "system", "content": system_prompt},
        ]

        user_content: list[dict[str, Any]] = [
            {"type": "text", "text": self._build_user_prompt(package)}
        ]

        # If cover exists and vision is allowed/supported
        if package.cover and package.cover.get("embedded_cover_path"):
            cover_path = Path(cast(str, package.cover["embedded_cover_path"]))
            if cover_path.exists():
                try:
                    with open(cover_path, "rb") as image_file:
                        base64_image = base64.b64encode(image_file.read()).decode("utf-8")
                    user_content.append(
                        {
                            "type": "image_url",
                            "image_url": {"url": f"data:image/jpeg;base64,{base64_image}"},
                        }
                    )
                except Exception as e:
                    logger.warning(f"Failed to encode cover image for vision: {e}")

        messages.append({"role": "user", "content": cast(Any, user_content)})

        req = LLMRequest(
            model=self.settings.judge_model, messages=messages, temperature=0.0, json_schema=True
        )

        from tenacity import AsyncRetrying, stop_after_attempt, wait_exponential

        try:
            async for attempt in AsyncRetrying(
                stop=stop_after_attempt(3),
                wait=wait_exponential(multiplier=1, min=2, max=10),
                reraise=True,
            ):
                with attempt:
                    response = await self.router.execute_structured("deep_reasoning", req, schema)
            if isinstance(response.content, dict):
                return response.content
            return cast(dict[str, Any], json.loads(response.content))
        except Exception as e:
            logger.error(f"LLM Judge failed after retries: {e}")
            return {
                "recommended_action": "error",
                "reasons": [str(e)],
                "confidence": 0,
                "risk_flags": [],
                "proposed_patch": {},
                "same_work_confidence": 0,
                "same_edition_confidence": 0,
                "selected_candidate_ids": [],
            }

    def _build_user_prompt(self, package: EvidencePackage) -> str:
        prompt_data = {
            "current_metadata": package.current,
            "extracted_evidence": package.extracted,
            "snippets": [s["text"] for s in package.snippets],
            "candidates": package.candidates,
        }
        return json.dumps(prompt_data, indent=2)

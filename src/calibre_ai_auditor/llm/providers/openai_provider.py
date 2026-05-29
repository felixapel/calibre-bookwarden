import json
import logging
from typing import Any

from openai import AsyncOpenAI

from calibre_ai_auditor.llm.base import LLMProvider
from calibre_ai_auditor.llm.schemas import LLMRequest, LLMResponse

logger = logging.getLogger(__name__)


class OpenAIProvider(LLMProvider):
    def __init__(self, api_key: str | None, base_url: str | None = None):
        self.client = AsyncOpenAI(api_key=api_key, base_url=base_url)

    @property
    def name(self) -> str:
        return "openai"

    @property
    def is_local(self) -> bool:
        return False

    @property
    def supports_vision(self) -> bool:
        return True

    @property
    def supports_json_schema(self) -> bool:
        return True

    async def chat(self, request: LLMRequest) -> LLMResponse:
        kwargs: dict[str, Any] = {
            "model": request.model,
            "messages": request.messages,
            "temperature": request.temperature,
        }
        if request.max_tokens:
            kwargs["max_tokens"] = request.max_tokens

        try:
            response = await self.client.chat.completions.create(**kwargs)
            content = response.choices[0].message.content or ""
            return LLMResponse(content=content, raw_response=response)
        except Exception as e:
            logger.error(f"OpenAI chat failed: {e}")
            raise

    async def structured(self, request: LLMRequest, schema: dict[str, Any]) -> LLMResponse:
        kwargs: dict[str, Any] = {
            "model": request.model,
            "messages": request.messages,
            "temperature": request.temperature,
            "response_format": {
                "type": "json_schema",
                "json_schema": {"name": "response", "schema": schema, "strict": True},
            },
        }
        if request.max_tokens:
            kwargs["max_tokens"] = request.max_tokens

        try:
            response = await self.client.chat.completions.create(**kwargs)
            content = response.choices[0].message.content or "{}"
            parsed = json.loads(content)
            return LLMResponse(content=parsed, raw_response=response)
        except Exception as e:
            logger.error(f"OpenAI structured output failed: {e}")
            raise

    async def test_connection(self) -> bool:
        try:
            # A simple lightweight call to verify auth
            await self.client.models.list()
            return True
        except Exception as e:
            logger.error(f"OpenAI test connection failed: {e}")
            return False

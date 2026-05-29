import json
import logging
from typing import Any

import httpx
from openai import AsyncOpenAI

from calibre_ai_auditor.llm.base import LLMProvider
from calibre_ai_auditor.llm.schemas import LLMRequest, LLMResponse

logger = logging.getLogger(__name__)


class OllamaProvider(LLMProvider):
    def __init__(self, base_url: str):
        # Ensure base_url points to the OpenAI compatible endpoint, e.g. http://localhost:11434/v1
        self.base_url = base_url
        # We can use the OpenAI client since Ollama provides an OpenAI compatible endpoint
        self.client = AsyncOpenAI(api_key="ollama", base_url=base_url)

    @property
    def name(self) -> str:
        return "ollama"

    @property
    def is_local(self) -> bool:
        return True

    @property
    def supports_vision(self) -> bool:
        # Depends on the model, but Ollama supports it (e.g. llava)
        return True

    @property
    def supports_json_schema(self) -> bool:
        # Ollama supports json mode, but strict JSON schema might be tricky via
        # the OpenAI compat layer.
        # However, it does support format="json". Let's assume true and
        # fallback to basic json format if needed.
        return True

    async def chat(self, request: LLMRequest) -> LLMResponse:
        kwargs: dict[str, Any] = {
            "model": request.model,
            "messages": request.messages,
            "temperature": request.temperature,
        }
        if request.max_tokens:
            kwargs["max_tokens"] = request.max_tokens

        if request.json_schema:
            kwargs["response_format"] = {"type": "json_object"}

        try:
            response = await self.client.chat.completions.create(**kwargs)
            content = response.choices[0].message.content or ""
            return LLMResponse(content=content, raw_response=response)
        except Exception as e:
            logger.error(f"Ollama chat failed: {e}")
            raise

    async def structured(self, request: LLMRequest, schema: dict[str, Any]) -> LLMResponse:
        # Ollama's OpenAI compat layer doesn't fully support structured strict schemas
        # yet in the same way OpenAI does.
        # But it supports response_format={"type": "json_object"}.
        # We will append the schema to the prompt.
        messages = list(request.messages)
        schema_str = json.dumps(schema)
        messages.append(
            {
                "role": "user",
                "content": (
                    f"Please ensure your output strictly follows this JSON schema:\n{schema_str}"
                ),
            }
        )

        kwargs: dict[str, Any] = {
            "model": request.model,
            "messages": messages,
            "temperature": request.temperature,
            "response_format": {"type": "json_object"},
        }
        if request.max_tokens:
            kwargs["max_tokens"] = request.max_tokens

        try:
            response = await self.client.chat.completions.create(**kwargs)
            content = response.choices[0].message.content or "{}"
            parsed = json.loads(content)
            return LLMResponse(content=parsed, raw_response=response)
        except Exception as e:
            logger.error(f"Ollama structured output failed: {e}")
            raise

    async def test_connection(self) -> bool:
        try:
            # We can use httpx to directly ping the Ollama tags endpoint
            # Since the base_url might be /v1, we need to extract the root
            root_url = self.base_url.replace("/v1", "").rstrip("/")
            async with httpx.AsyncClient(timeout=5.0) as client:
                res = await client.get(f"{root_url}/api/tags")
                res.raise_for_status()
            return True
        except Exception as e:
            logger.error(f"Ollama test connection failed: {e}")
            return False

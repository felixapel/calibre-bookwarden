import json
import logging
from typing import Any

from openai import AsyncOpenAI

from calibre_ai_auditor.llm.base import LLMProvider
from calibre_ai_auditor.llm.schemas import LLMRequest, LLMResponse

logger = logging.getLogger(__name__)


class LMStudioProvider(LLMProvider):
    def __init__(self, base_url: str = "http://localhost:1234/v1"):
        # LM Studio uses an OpenAI-compatible endpoint
        self.base_url = base_url
        self.client = AsyncOpenAI(api_key="lm-studio", base_url=base_url)

    @property
    def name(self) -> str:
        return "lmstudio"

    @property
    def is_local(self) -> bool:
        return True

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
            logger.error(f"LM Studio chat failed: {e}")
            raise

    async def structured(self, request: LLMRequest, schema: dict[str, Any]) -> LLMResponse:
        # LM Studio compatible structured format (json_object or strict json_schema depending on model)
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
            logger.error(f"LM Studio structured output failed: {e}")
            # Try falling back to JSON object mode if strict schema is not supported by the local model
            try:
                kwargs["response_format"] = {"type": "json_object"}
                # Add schema context to messages
                messages = list(request.messages)
                messages.append({
                    "role": "user",
                    "content": f"Please ensure your output strictly follows this JSON schema:\n{json.dumps(schema)}"
                })
                kwargs["messages"] = messages
                response = await self.client.chat.completions.create(**kwargs)
                content = response.choices[0].message.content or "{}"
                parsed = json.loads(content)
                return LLMResponse(content=parsed, raw_response=response)
            except Exception as e2:
                logger.error(f"LM Studio fallback structured output failed: {e2}")
                raise

    async def test_connection(self) -> bool:
        try:
            await self.client.models.list()
            return True
        except Exception as e:
            logger.error(f"LM Studio test connection failed: {e}")
            return False

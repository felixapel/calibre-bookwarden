import json
import logging
from typing import Any

try:
    from google import genai
    from google.genai import types
except ImportError:
    genai = None  # type: ignore
    types = None  # type: ignore

from calibre_ai_auditor.llm.base import LLMProvider
from calibre_ai_auditor.llm.schemas import LLMRequest, LLMResponse

logger = logging.getLogger(__name__)


class GoogleProvider(LLMProvider):
    def __init__(self, api_key: str | None = None):
        self.api_key = api_key
        # If api_key is None, genai.Client will look at GEMINI_API_KEY env var automatically.
        self.client = genai.Client(api_key=api_key)

    @property
    def name(self) -> str:
        return "google"

    @property
    def is_local(self) -> bool:
        return False

    @property
    def supports_vision(self) -> bool:
        return True

    @property
    def supports_json_schema(self) -> bool:
        return True

    def _map_messages(self, messages: list[dict[str, Any]]) -> tuple[list[types.Content], str | None]:
        contents: list[types.Content] = []
        system_instruction: str | None = None

        for msg in messages:
            role = msg.get("role")
            content = msg.get("content")

            if role == "system":
                # System prompt is set in instructions
                if isinstance(content, str):
                    system_instruction = content
                elif isinstance(content, list):
                    system_instruction = "\n".join(
                        part.get("text", "") if isinstance(part, dict) else str(part) for part in content
                    )
            elif role in ("user", "assistant"):
                parts = []
                if isinstance(content, str):
                    parts.append(types.Part.from_text(text=content))
                elif isinstance(content, list):
                    for part in content:
                        if isinstance(part, str):
                            parts.append(types.Part.from_text(text=part))
                        elif isinstance(part, dict):
                            part_type = part.get("type")
                            if part_type == "text":
                                parts.append(types.Part.from_text(text=part.get("text", "")))
                            elif part_type == "image_url":
                                image_url = part.get("image_url", {}).get("url", "")
                                if image_url.startswith("data:image"):
                                    import base64

                                    try:
                                        header, base64_str = image_url.split(",", 1)
                                        mime_type = header.split(";")[0].split(":")[1]
                                        img_bytes = base64.b64decode(base64_str)
                                        parts.append(types.Part.from_bytes(data=img_bytes, mime_type=mime_type))
                                    except Exception as e:
                                        logger.warning(f"Failed to parse base64 image: {e}")

                gemini_role = "user" if role == "user" else "model"
                contents.append(types.Content(role=gemini_role, parts=parts))

        return contents, system_instruction

    async def chat(self, request: LLMRequest) -> LLMResponse:
        contents, system_instruction = self._map_messages(request.messages)
        config = types.GenerateContentConfig(  # type: ignore[call-overload]
            system_instruction=system_instruction,
            temperature=request.temperature,
        )
        if request.max_tokens:
            config.max_output_tokens = request.max_tokens

        model = request.model or "gemini-2.5-flash"
        try:
            response = await self.client.aio.models.generate_content(  # type: ignore[attr-defined]
                model=model,
                contents=contents,
                config=config,
            )
            content_text = response.text or ""
            return LLMResponse(content=content_text, raw_response=response)
        except Exception as e:
            logger.error(f"Gemini chat failed: {e}")
            raise

    async def structured(self, request: LLMRequest, schema: dict[str, Any]) -> LLMResponse:
        contents, system_instruction = self._map_messages(request.messages)
        config = types.GenerateContentConfig(  # type: ignore[call-overload]
            system_instruction=system_instruction,
            temperature=request.temperature,
            response_mime_type="application/json",
            response_schema=schema,
        )
        if request.max_tokens:
            config.max_output_tokens = request.max_tokens

        model = request.model or "gemini-2.5-flash"
        try:
            response = await self.client.aio.models.generate_content(
                model=model,
                contents=contents,
                config=config,
            )
            content_text = response.text or "{}"
            parsed = json.loads(content_text)
            return LLMResponse(content=parsed, raw_response=response)
        except Exception as e:
            logger.error(f"Gemini structured output failed: {e}")
            raise

    async def test_connection(self) -> bool:
        try:
            # We list models to check credentials/api connection
            pager = await self.client.aio.models.list(config=types.ListModelsConfig(page_size=1))  # type: ignore[attr-defined]
            async for _ in pager:  # type: ignore[attr-defined]
                break
            return True
        except Exception as e:
            logger.error(f"Gemini test connection failed: {e}")
            return False

from typing import Any

from pydantic import BaseModel


class LLMRequest(BaseModel):
    messages: list[dict[str, Any]]
    model: str
    temperature: float = 0.0
    json_schema: bool = False
    max_tokens: int | None = None


class LLMResponse(BaseModel):
    content: str | dict[str, Any]
    raw_response: Any | None = None

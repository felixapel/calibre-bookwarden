from abc import ABC, abstractmethod
from typing import Any

from calibre_ai_auditor.llm.schemas import LLMRequest, LLMResponse


class LLMProvider(ABC):
    @property
    @abstractmethod
    def name(self) -> str:
        pass

    @property
    def is_local(self) -> bool:
        """Indicates if the provider runs locally (e.g., Ollama) or remotely (e.g., OpenAI)."""
        return False

    @property
    def supports_vision(self) -> bool:
        return False

    @property
    def supports_json_schema(self) -> bool:
        return False

    @abstractmethod
    async def chat(self, request: LLMRequest) -> LLMResponse:
        """Standard chat completion"""
        pass

    async def structured(self, request: LLMRequest, schema: dict[str, Any]) -> LLMResponse:
        """Structured output completion"""
        raise NotImplementedError(f"{self.name} does not support structured output")

    async def test_connection(self) -> bool:
        """Test if the provider is reachable and authenticated"""
        return False

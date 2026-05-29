from collections import OrderedDict
import logging
from typing import cast

import httpx
from openai import AsyncOpenAI

from calibre_ai_auditor.config.settings import Settings

logger = logging.getLogger(__name__)


class EmbeddingClient:
    _global_cache: OrderedDict[str, list[float]] = OrderedDict()
    _max_cache_size: int = 1000


    def __init__(
        self,
        base_url: str,
        model: str = "nomic-embed-text",
        provider: str = "ollama",
        api_key: str | None = None,
    ):
        self.base_url = base_url.rstrip("/") if base_url else ""
        self.model = model
        self.provider = provider.lower()
        self.api_key = api_key

    async def get_embeddings(self, text: str) -> list[float]:
        """Fetches embeddings from the configured provider (ollama, openai, or google)."""
        if not text or not text.strip():
            return []

        import hashlib

        # Create a unique key based on provider, model, and the text itself
        text_hash = hashlib.sha256(f"{self.provider}:{self.model}:{text}".encode()).hexdigest()
        if text_hash in self._global_cache:
            logger.debug(f"Retrieved cached embedding for hash: {text_hash}")
            self._global_cache.move_to_end(text_hash)
            return self._global_cache[text_hash]

        result: list[float] = []

        if self.provider == "openai" or (
            self.provider == "ollama" and "/v1" in self.base_url and self.api_key != "ollama"
        ):
            # OpenAI compatible embeddings endpoint
            try:
                client = AsyncOpenAI(api_key=self.api_key or "noop", base_url=self.base_url or None)
                response = await client.embeddings.create(input=[text], model=self.model)
                result = response.data[0].embedding
            except Exception as e:
                logger.error(f"Failed to fetch OpenAI embeddings: {e}")

        elif self.provider == "google":
            # Google GenAI embeddings endpoint
            url = f"https://generativelanguage.googleapis.com/v1beta/models/{self.model}:embedContent"
            headers = {"Content-Type": "application/json"}
            params = {"key": self.api_key or ""}
            payload = {"content": {"parts": [{"text": text}]}}
            try:
                async with httpx.AsyncClient(timeout=10.0) as client:
                    response = await client.post(url, json=payload, headers=headers, params=params)
                    response.raise_for_status()
                    data = response.json()
                    result = cast(list[float], data.get("embedding", {}).get("values", []))
            except Exception as e:
                logger.error(f"Failed to fetch Google embeddings: {e}")

        else:
            # Default to Ollama native /api/embeddings endpoint
            url = self.base_url
            if not url or not url.startswith("http"):
                # Graceful fallback: treat url as provider name or invalid and use a default
                url = "http://localhost:11434"

            # Remove '/v1' if present for the native Ollama endpoint
            ollama_url = f"{url.replace('/v1', '')}/api/embeddings"
            payload = {"model": self.model, "prompt": text}

            try:
                async with httpx.AsyncClient(timeout=10.0) as client:
                    response = await client.post(ollama_url, json=payload)
                    response.raise_for_status()
                    data = response.json()
                    result = cast(list[float], data.get("embedding", []))
            except Exception as e:
                logger.error(f"Failed to fetch embeddings from {ollama_url}: {e}")

        if result:
            self._global_cache[text_hash] = result
            if len(self._global_cache) > self._max_cache_size:
                self._global_cache.popitem(last=False)
        return result


def get_embedding_client(settings: Settings) -> EmbeddingClient:
    provider = settings.vectors.embedding_provider.lower()
    base_url = settings.ollama_base_url
    api_key = None

    if provider == "openai":
        base_url = settings.open_ai_base_url
        api_key = settings.open_ai_api_key
    elif provider == "google":
        base_url = "https://generativelanguage.googleapis.com"
        api_key = settings.gemini_api_key

    return EmbeddingClient(
        base_url=base_url,
        model=settings.vectors.embedding_model,
        provider=provider,
        api_key=api_key,
    )



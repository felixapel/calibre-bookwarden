import logging
from typing import Any

from calibre_ai_auditor.vectors.client import VectorClient
from calibre_ai_auditor.vectors.embeddings import EmbeddingClient

logger = logging.getLogger(__name__)


class VectorSearcher:
    def __init__(self, vector_client: VectorClient, embedding_client: EmbeddingClient):
        self.vector_client = vector_client
        self.embedding_client = embedding_client

    async def find_similar(self, text: str, limit: int = 5) -> list[dict[str, Any]]:
        if not self.vector_client.enabled:
            return []

        vector = await self.embedding_client.get_embeddings(text)
        if not vector:
            return []

        return await self.vector_client.search(vector=vector, limit=limit)

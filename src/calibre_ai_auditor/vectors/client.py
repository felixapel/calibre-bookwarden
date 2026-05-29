import logging
from typing import Any

logger = logging.getLogger(__name__)


class VectorClient:
    """Client for Qdrant vector database."""

    def __init__(self, url: str, collection: str, enabled: bool = False):
        self.url = url
        self.collection = collection
        self.enabled = enabled
        self._client = None
        self._collection_ensured = False

        if self.enabled:
            try:
                from qdrant_client import AsyncQdrantClient

                self._client = AsyncQdrantClient(url=self.url)
            except Exception as e:
                logger.error(f"Failed to initialize QdrantClient: {e}")
                self.enabled = False

    async def ensure_collection(self, vector_size: int = 1536) -> None:
        if not self.enabled or not self._client or self._collection_ensured:
            return
        try:
            collections = await self._client.get_collections()
            collection_names = [c.name for c in collections.collections]
            if self.collection not in collection_names:
                from qdrant_client.models import Distance, VectorParams
                await self._client.create_collection(
                    collection_name=self.collection,
                    vectors_config=VectorParams(size=vector_size, distance=Distance.COSINE),
                )
                logger.info(f"Created Qdrant collection: {self.collection}")
            self._collection_ensured = True
        except Exception as e:
            logger.error(f"Failed to ensure Qdrant collection exists: {e}")

    async def upsert(self, id: str, vector: list[float], payload: dict[str, Any]) -> None:
        if not self.enabled or not self._client:
            return

        from qdrant_client.models import PointStruct

        try:
            await self.ensure_collection(len(vector))
            await self._client.upsert(
                collection_name=self.collection,
                points=[PointStruct(id=id, vector=vector, payload=payload)],
            )
        except Exception as e:
            logger.error(f"Failed to upsert to Qdrant: {e}")

    async def search(self, vector: list[float], limit: int = 5) -> list[dict[str, Any]]:
        if not self.enabled or not self._client:
            return []

        try:
            await self.ensure_collection(len(vector))
            results = await self._client.search(  # type: ignore
                collection_name=self.collection, query_vector=vector, limit=limit
            )
            return [hit.payload for hit in results] if results else []
        except Exception as e:
            logger.error(f"Failed to search Qdrant: {e}")
            return []

    async def test_connection(self) -> bool:
        if not self.enabled or not self._client:
            return False
        try:
            # Just ping the server
            await self._client.get_collections()
            return True
        except Exception:
            return False


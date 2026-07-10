import hashlib
import logging

from calibre_ai_auditor.storage.models import BookRecord, EvidencePackage
from calibre_ai_auditor.vectors.client import VectorClient
from calibre_ai_auditor.vectors.embeddings import EmbeddingClient

logger = logging.getLogger(__name__)


class VectorIndexer:
    def __init__(self, vector_client: VectorClient, embedding_client: EmbeddingClient):
        self.vector_client = vector_client
        self.embedding_client = embedding_client

    async def index_book(self, book: BookRecord, package: EvidencePackage | None = None) -> None:
        if not self.vector_client.enabled:
            return

        text_to_embed = book.current_metadata.get("title", "")
        authors = book.current_metadata.get("authors", [])
        if authors:
            text_to_embed += f" by {', '.join(authors)}"

        if package and package.snippets:
            # Add a bit of the first snippet
            snippet_text = package.snippets[0].get("text", "")[:500]
            text_to_embed += f"\n\nSnippet: {snippet_text}"

        if not text_to_embed.strip():
            return

        # Create a stable UUID based on the book key
        book_id = str(hashlib.md5(book.book_key.encode()).hexdigest())
        # Qdrant requires UUID string, so we format the md5 hash as UUID
        point_id = f"{book_id[:8]}-{book_id[8:12]}-4{book_id[13:16]}-a{book_id[17:20]}-{book_id[20:32]}"

        vector = await self.embedding_client.get_embeddings(text_to_embed)
        if vector:
            payload = {
                "book_key": book.book_key,
                "title": book.current_metadata.get("title"),
                "authors": authors,
            }
            await self.vector_client.upsert(id=point_id, vector=vector, payload=payload)

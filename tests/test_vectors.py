import pytest

from calibre_ai_auditor.storage.models import BookRecord
from calibre_ai_auditor.vectors.client import VectorClient
from calibre_ai_auditor.vectors.embeddings import EmbeddingClient
from calibre_ai_auditor.vectors.indexer import VectorIndexer
from calibre_ai_auditor.vectors.search import VectorSearcher


@pytest.mark.asyncio
async def test_vector_indexer_disabled() -> None:
    vclient = VectorClient(url="http://test", collection="test", enabled=False)
    eclient = EmbeddingClient(base_url="http://test")
    indexer = VectorIndexer(vclient, eclient)

    book = BookRecord(book_key="calibre:1", run_id="run1", current_metadata={"title": "Test Title"})

    await indexer.index_book(book)  # Should do nothing


@pytest.mark.asyncio
async def test_vector_searcher_disabled() -> None:
    vclient = VectorClient(url="http://test", collection="test", enabled=False)
    eclient = EmbeddingClient(base_url="http://test")
    searcher = VectorSearcher(vclient, eclient)

    res = await searcher.find_similar("Test Title")
    assert res == []


@pytest.mark.asyncio
async def test_embedding_client_caching(monkeypatch) -> None:
    eclient = EmbeddingClient(base_url="http://test", provider="openai")

    call_count = 0

    async def mock_embeddings_create(*args, **kwargs):
        nonlocal call_count
        call_count += 1

        class MockData:
            embedding = [0.1, 0.2, 0.3]

        class MockResponse:
            data = [MockData()]

        return MockResponse()

    from openai.resources.embeddings import AsyncEmbeddings

    monkeypatch.setattr(AsyncEmbeddings, "create", mock_embeddings_create)

    vec1 = await eclient.get_embeddings("unique text")
    assert vec1 == [0.1, 0.2, 0.3]
    assert call_count == 1

    vec2 = await eclient.get_embeddings("unique text")
    assert vec2 == [0.1, 0.2, 0.3]
    assert call_count == 1

from unittest.mock import AsyncMock

import pytest

from calibre_ai_auditor.queue.valkey import ValkeyQueue


@pytest.mark.asyncio
async def test_valkey_queue_fallback_memory() -> None:
    queue = ValkeyQueue(url="redis://localhost:6379", backend="memory")
    assert queue._client is None

    job_id = await queue.enqueue("test_task", {"foo": "bar"})
    assert job_id is not None

    status = await queue.get_status(job_id)
    assert status is not None
    assert status["status"] == "pending"
    assert status["payload"] == {"foo": "bar"}

    job = await queue.dequeue()
    assert job is not None
    assert job["job_id"] == job_id

    await queue.update_job(job_id, {"status": "completed", "result": 42})
    status = await queue.get_status(job_id)
    assert status["status"] == "completed"
    assert status["result"] == 42


@pytest.mark.asyncio
async def test_valkey_queue_with_redis() -> None:
    queue = ValkeyQueue(url="redis://localhost:6379", backend="valkey")
    # Mock the client
    mock_client = AsyncMock()
    queue._client = mock_client

    mock_client.get.return_value = '{"job_id": "123", "status": "pending"}'
    status = await queue.get_status("123")
    assert status == {"job_id": "123", "status": "pending"}
    mock_client.get.assert_called_with("job:123")


@pytest.mark.asyncio
async def test_valkey_backend_does_not_fall_back_on_connection_failure() -> None:
    queue = ValkeyQueue(url="redis://localhost:6379", backend="valkey")
    mock_client = AsyncMock()
    mock_client.set.side_effect = ConnectionError("valkey unavailable")
    queue._client = mock_client

    with pytest.raises(RuntimeError, match="Valkey enqueue failed"):
        await queue.enqueue("test_task", {"foo": "bar"})

    assert queue._fallback_db == {}

"""Writer liveness heartbeat shared through the production Valkey."""

import json
from datetime import UTC, datetime
from typing import Any

WRITER_HEARTBEAT_KEY = "writer:heartbeat"


def heartbeat_is_fresh(payload: object, *, now: datetime | None = None, max_age_seconds: int = 300) -> bool:
    if not isinstance(payload, dict) or not payload.get("owner"):
        return False
    try:
        timestamp = datetime.fromisoformat(str(payload["timestamp"]))
        if timestamp.tzinfo is None:
            timestamp = timestamp.replace(tzinfo=UTC)
        reference = now or datetime.now(UTC)
        return 0 <= (reference - timestamp.astimezone(UTC)).total_seconds() <= max_age_seconds
    except (KeyError, TypeError, ValueError):
        return False


def publish_writer_heartbeat(url: str, owner: str, *, ttl_seconds: int = 600, timeout: float = 1.0) -> None:
    import redis

    client: Any = redis.from_url(url, decode_responses=True, socket_connect_timeout=timeout, socket_timeout=timeout)
    try:
        payload = json.dumps({"owner": owner, "timestamp": datetime.now(UTC).isoformat()})
        client.set(WRITER_HEARTBEAT_KEY, payload, ex=ttl_seconds)
    finally:
        client.close()


def read_writer_heartbeat(url: str, *, timeout: float = 1.0) -> object | None:
    import redis

    client: Any = redis.from_url(url, decode_responses=True, socket_connect_timeout=timeout, socket_timeout=timeout)
    try:
        raw = client.get(WRITER_HEARTBEAT_KEY)
        return json.loads(raw) if raw else None
    finally:
        client.close()

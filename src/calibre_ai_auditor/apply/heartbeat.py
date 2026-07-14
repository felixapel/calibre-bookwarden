"""Writer liveness heartbeat shared through the production Valkey."""

import hashlib
import json
import os
from datetime import UTC, datetime
from pathlib import Path
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


def library_root_sha256(root: str | Path) -> str:
    canonical = str(Path(os.path.normpath(os.path.abspath(root))))
    return hashlib.sha256(canonical.encode("utf-8")).hexdigest()


def heartbeat_matches_pilot(
    payload: object,
    *,
    release_digest: str,
    alembic_revision: str,
    library_root_sha256: str,
    pilot_id: str,
    max_operations: int,
) -> bool:
    return bool(
        isinstance(payload, dict)
        and payload.get("release_digest") == release_digest
        and payload.get("alembic_revision") == alembic_revision
        and payload.get("library_root_sha256") == library_root_sha256
        and payload.get("pilot_id") == pilot_id
        and payload.get("max_operations") == max_operations
    )


def publish_writer_heartbeat(
    url: str,
    owner: str,
    *,
    ttl_seconds: int = 600,
    timeout: float = 1.0,
    release_digest: str | None = None,
    alembic_revision: str | None = None,
    library_root_sha256: str | None = None,
    pilot_id: str | None = None,
    max_operations: int | None = None,
) -> None:
    import redis

    client: Any = redis.from_url(url, decode_responses=True, socket_connect_timeout=timeout, socket_timeout=timeout)
    try:
        heartbeat: dict[str, object] = {"owner": owner, "timestamp": datetime.now(UTC).isoformat()}
        if release_digest and alembic_revision and library_root_sha256 and pilot_id and max_operations is not None:
            heartbeat.update(
                {
                    "release_digest": release_digest,
                    "alembic_revision": alembic_revision,
                    "library_root_sha256": library_root_sha256,
                    "pilot_id": pilot_id,
                    "max_operations": max_operations,
                }
            )
        payload = json.dumps(heartbeat)
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

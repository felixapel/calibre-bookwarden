"""Certificate A verifier liveness bound to one immutable runtime."""

from __future__ import annotations

import json
from datetime import UTC, datetime
from typing import Any

from calibre_ai_auditor.apply.heartbeat import heartbeat_is_fresh

VERIFIER_HEARTBEAT_KEY = "certificate-a:verifier:heartbeat"


def verifier_heartbeat_is_fresh(
    payload: object,
    *,
    release_digest: str,
    alembic_revision: str,
    library_root_sha256: str,
    now: datetime | None = None,
    max_age_seconds: int = 90,
) -> bool:
    """Require freshness plus the exact release, schema, and library binding."""
    return bool(
        heartbeat_is_fresh(payload, now=now, max_age_seconds=max_age_seconds)
        and isinstance(payload, dict)
        and payload.get("release_digest") == release_digest
        and payload.get("alembic_revision") == alembic_revision
        and payload.get("library_root_sha256") == library_root_sha256
    )


def publish_verifier_heartbeat(
    url: str,
    owner: str,
    *,
    release_digest: str,
    alembic_revision: str,
    library_root_sha256: str,
    ttl_seconds: int = 180,
    timeout: float = 1.0,
) -> None:
    """Publish a short-lived verifier identity without source or credential data."""
    import redis

    client: Any = redis.from_url(
        url,
        decode_responses=True,
        socket_connect_timeout=timeout,
        socket_timeout=timeout,
    )
    try:
        payload = json.dumps(
            {
                "owner": owner,
                "timestamp": datetime.now(UTC).isoformat(),
                "release_digest": release_digest,
                "alembic_revision": alembic_revision,
                "library_root_sha256": library_root_sha256,
            },
            separators=(",", ":"),
            sort_keys=True,
        )
        client.set(VERIFIER_HEARTBEAT_KEY, payload, ex=ttl_seconds)
    finally:
        client.close()


def read_verifier_heartbeat(url: str, *, timeout: float = 1.0) -> object | None:
    """Read the current verifier heartbeat from Valkey."""
    import redis

    client: Any = redis.from_url(
        url,
        decode_responses=True,
        socket_connect_timeout=timeout,
        socket_timeout=timeout,
    )
    try:
        raw = client.get(VERIFIER_HEARTBEAT_KEY)
        return json.loads(raw) if raw else None
    finally:
        client.close()

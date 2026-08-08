from datetime import UTC, datetime, timedelta

from calibre_ai_auditor.verification.heartbeat import verifier_heartbeat_is_fresh


def _payload(now: datetime) -> dict[str, object]:
    return {
        "owner": "verifier-1",
        "timestamp": now.isoformat(),
        "release_digest": f"sha256:{'a' * 64}",
        "alembic_revision": "revision-1",
        "library_root_sha256": "b" * 64,
    }


def test_verifier_heartbeat_requires_fresh_exact_runtime_binding() -> None:
    now = datetime.now(UTC)
    payload = _payload(now)

    assert verifier_heartbeat_is_fresh(
        payload,
        now=now + timedelta(seconds=10),
        max_age_seconds=30,
        release_digest=f"sha256:{'a' * 64}",
        alembic_revision="revision-1",
        library_root_sha256="b" * 64,
    )
    assert not verifier_heartbeat_is_fresh(
        payload,
        now=now + timedelta(seconds=31),
        max_age_seconds=30,
        release_digest=f"sha256:{'a' * 64}",
        alembic_revision="revision-1",
        library_root_sha256="b" * 64,
    )
    assert not verifier_heartbeat_is_fresh(
        payload,
        now=now + timedelta(seconds=10),
        max_age_seconds=30,
        release_digest=f"sha256:{'c' * 64}",
        alembic_revision="revision-1",
        library_root_sha256="b" * 64,
    )


def test_verifier_heartbeat_rejects_malformed_or_future_payloads() -> None:
    now = datetime.now(UTC)
    expected = {
        "now": now,
        "max_age_seconds": 30,
        "release_digest": f"sha256:{'a' * 64}",
        "alembic_revision": "revision-1",
        "library_root_sha256": "b" * 64,
    }

    assert not verifier_heartbeat_is_fresh(None, **expected)
    assert not verifier_heartbeat_is_fresh({"owner": "x", "timestamp": "invalid"}, **expected)
    assert not verifier_heartbeat_is_fresh(_payload(now + timedelta(seconds=1)), **expected)

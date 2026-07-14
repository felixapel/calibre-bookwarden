from datetime import UTC, datetime, timedelta

from calibre_ai_auditor.apply.heartbeat import heartbeat_is_fresh, heartbeat_matches_pilot


def test_writer_heartbeat_rejects_stale_or_malformed_payloads() -> None:
    now = datetime(2026, 7, 12, 18, 0, tzinfo=UTC)

    assert heartbeat_is_fresh({"timestamp": now.isoformat(), "owner": "writer-1"}, now=now, max_age_seconds=30)
    assert not heartbeat_is_fresh(
        {"timestamp": (now - timedelta(seconds=31)).isoformat(), "owner": "writer-1"},
        now=now,
        max_age_seconds=30,
    )
    assert not heartbeat_is_fresh({"timestamp": "not-a-date"}, now=now, max_age_seconds=30)


def test_writer_heartbeat_must_match_the_exact_pilot_runtime() -> None:
    payload = {
        "owner": "writer-1",
        "timestamp": datetime.now(UTC).isoformat(),
        "release_digest": f"sha256:{'a' * 64}",
        "alembic_revision": "revision-1",
        "library_root_sha256": "b" * 64,
        "pilot_id": "pilot-1",
        "max_operations": 5,
    }

    assert heartbeat_matches_pilot(
        payload,
        release_digest=f"sha256:{'a' * 64}",
        alembic_revision="revision-1",
        library_root_sha256="b" * 64,
        pilot_id="pilot-1",
        max_operations=5,
    )
    assert not heartbeat_matches_pilot(
        payload,
        release_digest=f"sha256:{'c' * 64}",
        alembic_revision="revision-1",
        library_root_sha256="b" * 64,
        pilot_id="pilot-1",
        max_operations=5,
    )
    assert not heartbeat_matches_pilot(
        payload,
        release_digest=f"sha256:{'a' * 64}",
        alembic_revision="revision-1",
        library_root_sha256="b" * 64,
        pilot_id="pilot-2",
        max_operations=5,
    )
    assert not heartbeat_matches_pilot(
        payload,
        release_digest=f"sha256:{'a' * 64}",
        alembic_revision="revision-1",
        library_root_sha256="b" * 64,
        pilot_id="pilot-1",
        max_operations=4,
    )
    assert not heartbeat_matches_pilot(
        {"owner": "writer-1", "timestamp": datetime.now(UTC).isoformat()},
        release_digest=f"sha256:{'a' * 64}",
        alembic_revision="revision-1",
        library_root_sha256="b" * 64,
        pilot_id="pilot-1",
        max_operations=5,
    )

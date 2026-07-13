from datetime import UTC, datetime, timedelta

from calibre_ai_auditor.apply.heartbeat import heartbeat_is_fresh


def test_writer_heartbeat_rejects_stale_or_malformed_payloads() -> None:
    now = datetime(2026, 7, 12, 18, 0, tzinfo=UTC)

    assert heartbeat_is_fresh({"timestamp": now.isoformat(), "owner": "writer-1"}, now=now, max_age_seconds=30)
    assert not heartbeat_is_fresh(
        {"timestamp": (now - timedelta(seconds=31)).isoformat(), "owner": "writer-1"},
        now=now,
        max_age_seconds=30,
    )
    assert not heartbeat_is_fresh({"timestamp": "not-a-date"}, now=now, max_age_seconds=30)

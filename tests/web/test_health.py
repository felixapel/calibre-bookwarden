from datetime import UTC, datetime

from calibre_ai_auditor.apply.heartbeat import library_root_sha256
from calibre_ai_auditor.config.settings import Settings
from calibre_ai_auditor.storage.db import expected_schema_revision
from calibre_ai_auditor.web.api.health import _writer_readiness_check


def _settings() -> Settings:
    return Settings(
        library={"path": "/library"},
        manifestation_v2={
            "supervised_pilot": {
                "enabled": True,
                "pilot_id": "readiness-pilot",
                "release_digest": f"sha256:{'a' * 64}",
                "max_operations": 5,
            }
        },
    )


def _heartbeat(*, pilot_id: str = "readiness-pilot") -> dict[str, object]:
    return {
        "owner": "writer",
        "timestamp": datetime.now(UTC).isoformat(),
        "release_digest": f"sha256:{'a' * 64}",
        "alembic_revision": expected_schema_revision(),
        "library_root_sha256": library_root_sha256("/library"),
        "pilot_id": pilot_id,
        "max_operations": 5,
    }


def test_writer_readiness_requires_the_exact_fresh_pilot_binding() -> None:
    assert _writer_readiness_check(_settings(), _heartbeat()) == {
        "ok": True,
        "fresh": True,
        "pilot_binding": True,
    }

    assert _writer_readiness_check(_settings(), _heartbeat(pilot_id="another-pilot")) == {
        "ok": False,
        "fresh": True,
        "pilot_binding": False,
    }

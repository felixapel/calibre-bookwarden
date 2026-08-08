"""Certificate A verification request contract tests."""

from __future__ import annotations

from collections.abc import Generator
from datetime import UTC, datetime
from pathlib import Path

import pytest
from fastapi.testclient import TestClient
from sqlalchemy.engine import Engine
from sqlmodel import Session, SQLModel, create_engine, select

from calibre_ai_auditor.config.settings import Settings
from calibre_ai_auditor.storage.models import VerificationResult, VerificationRun

API_KEY = "certificate-a-verify-key-with-32-characters-9Z"
RELEASE_DIGEST = f"sha256:{'a' * 64}"
AUTH = {"X-API-Key": API_KEY}


def _settings(library: Path) -> Settings:
    return Settings(
        profile="production",
        api_key=API_KEY,
        trusted_hosts="testserver",
        release_digest=RELEASE_DIGEST,
        library={"path": library, "read_only": True},
        database={
            "backend": "postgres",
            "postgres_dsn": "postgresql+psycopg://bookaudit_shadow:test@postgres/bookaudit",
        },
        queue={"backend": "valkey", "valkey_url": "redis://valkey:6379/0"},
        rate_limits={
            "enabled": True,
            "backend": "valkey",
            "valkey_url": "redis://valkey:6379/1",
        },
        providers={"calibre_fetch": False, "openlibrary": True, "google_books": True},
        recognition_v2={
            "ocr": {"enabled": True, "backends": ["tesseract"], "max_pages": 6, "language": "eng"},
            "vision": {"enabled": False},
        },
        ollama_enabled=False,
        lmstudio_enabled=False,
    )


@pytest.fixture
def api(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
) -> Generator[tuple[TestClient, Engine], None, None]:
    from calibre_ai_auditor.web.production import create_production_app

    monkeypatch.setenv("BOOKAUDIT_DATABASE__BACKEND", "postgres")
    monkeypatch.setenv(
        "BOOKAUDIT_DATABASE__POSTGRES_DSN",
        "postgresql+psycopg://bookaudit_shadow:test@postgres/bookaudit",
    )
    library = tmp_path / "offline-library"
    library.mkdir()
    engine = create_engine(
        f"sqlite:///{tmp_path / 'certificate-a.db'}",
        connect_args={"check_same_thread": False},
    )
    SQLModel.metadata.create_all(engine)
    app = create_production_app(
        settings_provider=lambda: _settings(library),
        engine_provider=lambda _settings: engine,
        static_dir=tmp_path / "missing-static",
    )

    async def allow_rate_limit(
        _url: str,
        _identity: str,
        _limit: int,
        _window: int,
        _timeout: float,
    ) -> int:
        return 0

    app.state.rate_limit_consumer = allow_rate_limit
    with TestClient(app) as client:
        yield client, engine


def _start(
    client: TestClient,
    *,
    key: str = "request-key-00000001",
    body: dict[str, object] | None = None,
):  # type: ignore[no-untyped-def]
    headers = {**AUTH, "Idempotency-Key": key}
    return client.post(
        "/api/verify",
        headers=headers,
        json=body or {"confirm_calibre_stopped": True},
    )


def test_start_requires_confirmation_and_idempotency_key(api: tuple[TestClient, Engine]) -> None:
    client, _engine = api

    missing_confirmation = client.post(
        "/api/verify",
        headers={**AUTH, "Idempotency-Key": "request-key-00000001"},
        json={},
    )
    missing_key = client.post(
        "/api/verify",
        headers=AUTH,
        json={"confirm_calibre_stopped": True},
    )
    denied_confirmation = _start(client, body={"confirm_calibre_stopped": False})

    assert missing_confirmation.status_code == 422
    assert missing_key.status_code == 422
    assert denied_confirmation.status_code == 422


@pytest.mark.parametrize(
    "legacy_field",
    [
        "library",
        "pipeline",
        "run_id",
        "use_llm",
        "use_vision",
        "allow_remote_text",
        "allow_remote_images",
    ],
)
def test_start_rejects_every_removed_or_remote_field(
    api: tuple[TestClient, Engine],
    legacy_field: str,
) -> None:
    client, _engine = api
    body: dict[str, object] = {"confirm_calibre_stopped": True, legacy_field: "attacker-controlled"}

    response = _start(client, body=body)

    assert response.status_code == 422
    assert response.json()["detail"][0]["type"] == "extra_forbidden"


@pytest.mark.parametrize("limit", [0, -1, 10_001])
def test_start_rejects_unbounded_limits(api: tuple[TestClient, Engine], limit: int) -> None:
    client, _engine = api

    response = _start(client, body={"confirm_calibre_stopped": True, "limit": limit})

    assert response.status_code == 422


def test_start_only_persists_a_pending_request_without_inventorying_the_library(
    api: tuple[TestClient, Engine],
) -> None:
    client, engine = api

    response = _start(
        client,
        body={"confirm_calibre_stopped": True, "limit": 5, "use_ocr": False},
    )

    assert response.status_code == 202, response.text
    payload = response.json()
    assert payload["run_id"].startswith("verify_")
    assert payload["status"] == "pending"
    assert payload["total"] is None
    assert payload["completed"] == 0
    assert payload["counts"] == {}

    with Session(engine) as session:
        run = session.exec(select(VerificationRun)).one()
        assert run.run_id == payload["run_id"]
        assert run.status == "pending"
        assert run.pipeline_version == "manifestation-v2"
        assert run.mode == "shadow"
        assert run.total == 0
        assert run.completed == 0
        assert run.idempotency_key == "request-key-00000001"
        assert len(run.request_sha256 or "") == 64
        assert run.contract_version == "certificate-a-v1"
        assert run.source_root == str(Path(run.source_root or "").resolve())
        assert len(run.source_root_sha256 or "") == 64
        assert run.fence_token == 0
        assert run.cancel_requested_at is None
        assert run.effective_config["release_digest"] == RELEASE_DIGEST
        assert run.effective_config["providers"] == ["google_books_isbn", "openlibrary_isbn"]
        assert run.effective_config["recognition"] == {
            "ocr_enabled": False,
            "ocr_backend": "tesseract",
            "ocr_language": "eng",
            "ocr_max_pages": 6,
            "vision_enabled": False,
            "llm_enabled": False,
        }
        assert run.effective_config["privacy"] == {
            "remote_text": False,
            "remote_images": False,
        }
        assert session.exec(select(VerificationResult)).all() == []


def test_same_idempotency_key_and_payload_returns_the_original_run(
    api: tuple[TestClient, Engine],
) -> None:
    client, engine = api
    body = {"confirm_calibre_stopped": True, "limit": 2, "use_ocr": True}

    first = _start(client, body=body)
    second = _start(client, body=body)

    assert first.status_code == 202
    assert second.status_code == 202
    assert second.json() == first.json()
    with Session(engine) as session:
        assert len(session.exec(select(VerificationRun)).all()) == 1


def test_changed_payload_under_the_same_idempotency_key_returns_conflict(
    api: tuple[TestClient, Engine],
) -> None:
    client, _engine = api
    assert _start(client, body={"confirm_calibre_stopped": True, "limit": 1}).status_code == 202

    conflict = _start(client, body={"confirm_calibre_stopped": True, "limit": 2})

    assert conflict.status_code == 409
    assert conflict.json()["detail"]["code"] == "idempotency_key_reused"


def test_only_one_pending_or_active_run_is_allowed_for_the_library(
    api: tuple[TestClient, Engine],
) -> None:
    client, _engine = api
    first = _start(client, key="request-key-00000001")

    conflict = _start(client, key="request-key-00000002")

    assert conflict.status_code == 409
    assert conflict.json()["detail"] == {
        "code": "active_run_exists",
        "active_run_id": first.json()["run_id"],
    }


def test_list_and_detail_return_only_certificate_a_runs(api: tuple[TestClient, Engine]) -> None:
    client, engine = api
    started = _start(client)
    with Session(engine) as session:
        session.add(
            VerificationRun(
                run_id="legacy-run",
                status="completed",
                started_at=datetime.now(UTC),
                finished_at=datetime.now(UTC),
                pipeline_version="v1",
                mode="legacy",
            )
        )
        session.commit()

    listed = client.get("/api/verify/runs", headers=AUTH)
    detailed = client.get(f"/api/verify/{started.json()['run_id']}", headers=AUTH)

    assert listed.status_code == 200
    assert [run["run_id"] for run in listed.json()["runs"]] == [started.json()["run_id"]]
    assert detailed.status_code == 200
    assert detailed.json()["run_id"] == started.json()["run_id"]
    assert detailed.json()["results"] == []
    assert client.get("/api/verify/legacy-run", headers=AUTH).status_code == 404


def test_total_remains_unknown_until_inventory_completes(api: tuple[TestClient, Engine]) -> None:
    client, engine = api
    started = _start(client)
    run_id = started.json()["run_id"]
    with Session(engine) as session:
        run = session.exec(select(VerificationRun).where(VerificationRun.run_id == run_id)).one()
        run.status = "running"
        run.total = 4
        run.inventory_finished_at = datetime.now(UTC)
        session.add(run)
        session.commit()

    response = client.get(f"/api/verify/{run_id}", headers=AUTH)

    assert response.status_code == 200
    assert response.json()["total"] == 4


def test_cancel_is_durable_and_idempotent_while_nonterminal(api: tuple[TestClient, Engine]) -> None:
    client, engine = api
    started = _start(client)
    run_id = started.json()["run_id"]

    first = client.post(f"/api/verify/{run_id}/cancel", headers=AUTH)
    second = client.post(f"/api/verify/{run_id}/cancel", headers=AUTH)

    assert first.status_code == 202
    assert first.json()["status"] == "cancelling"
    assert second.status_code == 202
    assert second.json()["status"] == "cancelling"
    with Session(engine) as session:
        run = session.exec(select(VerificationRun).where(VerificationRun.run_id == run_id)).one()
        assert run.status == "cancelling"
        assert run.cancel_requested_at is not None


def test_cancel_rejects_terminal_or_unknown_runs(api: tuple[TestClient, Engine]) -> None:
    client, engine = api
    started = _start(client)
    run_id = started.json()["run_id"]
    with Session(engine) as session:
        run = session.exec(select(VerificationRun).where(VerificationRun.run_id == run_id)).one()
        run.status = "completed"
        run.finished_at = datetime.now(UTC)
        session.add(run)
        session.commit()

    terminal = client.post(f"/api/verify/{run_id}/cancel", headers=AUTH)
    missing = client.post("/api/verify/missing/cancel", headers=AUTH)

    assert terminal.status_code == 409
    assert terminal.json()["detail"]["code"] == "run_already_terminal"
    assert missing.status_code == 404

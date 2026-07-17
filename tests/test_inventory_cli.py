import json
import stat
from pathlib import Path
from types import SimpleNamespace

from typer.testing import CliRunner

import calibre_ai_auditor.cli.main as cli_module
from calibre_ai_auditor.cli.main import app

runner = CliRunner()


class FakeContentServerSource:
    def __init__(
        self,
        base_url: str,
        *,
        library_id: str,
        username: str,
        source_identity: str,
        password: str,
    ) -> None:
        assert base_url == "http://127.0.0.1:18086"
        assert library_id == "library"
        assert username == "auditor"
        assert source_identity == "SHA256:test-host"
        assert password == "private-password"
        self.fingerprint = "b" * 64

    def list_books(self) -> list[dict[str, object]]:
        return [
            {
                "id": 1,
                "title": "Never print me",
                "authors": ["Private author"],
                "publisher": "Publisher",
                "pubdate": "2020-01-01T00:00:00+00:00",
                "last_modified": "2026-07-01T00:00:00+00:00",
                "languages": ["en"],
                "identifiers": {"isbn": "9780306406157"},
                "formats": ["/private/book.epub"],
            }
        ]


def test_inventory_cli_emits_only_aggregate_json_without_initializing_state(tmp_path: Path, monkeypatch) -> None:
    monkeypatch.chdir(tmp_path)
    monkeypatch.setattr(cli_module, "ContentServerSource", FakeContentServerSource)

    def reject_db(*_args, **_kwargs) -> None:
        raise AssertionError("DB used")

    monkeypatch.setattr(cli_module, "init_db", reject_db)
    monkeypatch.setattr(cli_module, "get_engine", reject_db)

    result = runner.invoke(
        app,
        [
            "inventory",
            "--content-server",
            "http://127.0.0.1:18086",
            "--library-id",
            "library",
            "--username",
            "auditor",
            "--source-identity",
            "SHA256:test-host",
            "--password-stdin",
        ],
        input="private-password\n",
    )

    assert result.exit_code == 0, result.output
    payload = json.loads(result.output)
    assert payload["counts"]["books"] == 1
    assert "Never print me" not in result.output
    assert "Private author" not in result.output
    assert "private-password" not in result.output
    assert list(tmp_path.iterdir()) == []


def test_inventory_cli_writes_private_output_only_beneath_working_directory(tmp_path: Path, monkeypatch) -> None:
    monkeypatch.chdir(tmp_path)
    monkeypatch.setattr(cli_module, "ContentServerSource", FakeContentServerSource)
    output = tmp_path / "reports" / "inventory.json"

    result = runner.invoke(
        app,
        [
            "inventory",
            "--content-server",
            "http://127.0.0.1:18086",
            "--library-id",
            "library",
            "--username",
            "auditor",
            "--source-identity",
            "SHA256:test-host",
            "--password-stdin",
            "--output",
            str(output),
        ],
        input="private-password\n",
    )

    assert result.exit_code == 0, result.output
    assert output.exists()
    assert stat.S_IMODE(output.stat().st_mode) == 0o600
    assert stat.S_IMODE(output.parent.stat().st_mode) == 0o700
    assert "Never print me" not in output.read_text()

    outside = tmp_path.parent / "outside-inventory.json"
    rejected = runner.invoke(
        app,
        [
            "inventory",
            "--content-server",
            "http://127.0.0.1:18086",
            "--library-id",
            "library",
            "--username",
            "auditor",
            "--source-identity",
            "SHA256:test-host",
            "--password-stdin",
            "--output",
            str(outside),
        ],
        input="private-password\n",
    )
    assert rejected.exit_code != 0
    assert not outside.exists()


def test_verify_content_server_runs_only_shadow_pipeline_and_emits_no_metadata(tmp_path: Path, monkeypatch) -> None:
    import calibre_ai_auditor.verification.service_v2 as service_module

    monkeypatch.setattr(cli_module, "ContentServerSource", FakeContentServerSource)

    def reject_migration(_settings) -> None:
        raise AssertionError("runtime migration used")

    monkeypatch.setattr(cli_module, "init_db", reject_migration)
    monkeypatch.setattr(cli_module, "get_engine", lambda _settings: object())
    monkeypatch.setattr(cli_module, "_require_current_schema", lambda _engine: None)
    captured: dict[str, object] = {}

    def fake_build(*_args, **kwargs):
        captured["build_kwargs"] = kwargs
        return object()

    monkeypatch.setattr(service_module, "build_v2_enricher", fake_build)

    async def fake_run(**kwargs):
        captured.update(kwargs)
        return SimpleNamespace(
            status=SimpleNamespace(value="completed"),
            packages=[
                SimpleNamespace(
                    book_key=f"calibre-server:{'b' * 64}:1",
                    state=SimpleNamespace(value="review"),
                    identity=SimpleNamespace(value="unused", tier=SimpleNamespace(value="B"), risk_flags=[]),
                )
            ],
        )

    monkeypatch.setattr(service_module, "run_persisted_library_audit", fake_run)
    scratch = tmp_path / "scratch"
    result = runner.invoke(
        app,
        [
            "verify-content-server",
            "--content-server",
            "http://127.0.0.1:18086",
            "--library-id",
            "library",
            "--username",
            "auditor",
            "--source-identity",
            "SHA256:test-host",
            "--password-stdin",
            "--scratch-root",
            str(scratch),
            "--limit",
            "1",
            "--format",
            "json",
        ],
        input="private-password\n",
    )

    assert result.exit_code == 0, result.output
    assert captured["limit"] == 1
    assert captured["mode"].value == "shadow"
    assert captured["scratch_root"] == scratch
    assert captured["build_kwargs"]["use_public_providers"] is False
    assert "Never print me" not in result.output
    assert "private-password" not in result.output

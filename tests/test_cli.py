import json
from datetime import UTC, datetime, timedelta
from pathlib import Path
from unittest.mock import patch

from typer.testing import CliRunner

from calibre_ai_auditor.cli.main import app

runner = CliRunner()


def test_doctor() -> None:
    result = runner.invoke(app, ["doctor"])
    assert result.exit_code == 0
    assert "Doctor check:" in result.stdout


def test_scan_no_library() -> None:
    result = runner.invoke(app, ["scan"])
    assert result.exit_code == 1
    assert "Error: Library path not set" in result.stdout


def test_config() -> None:
    result = runner.invoke(app, ["config"])
    assert result.exit_code == 0
    assert "profile" in result.stdout
    assert "open_ai_api_key" not in result.stdout  # Should be excluded


def test_migrate_runs_explicit_schema_upgrade() -> None:
    with patch("calibre_ai_auditor.cli.main.init_db") as upgrade:
        result = runner.invoke(app, ["migrate"])

    assert result.exit_code == 0
    upgrade.assert_called_once()
    assert "Database schema upgraded" in result.stdout


def test_retention_is_dry_run_and_requires_backup_and_stopped_writer(tmp_path: Path) -> None:
    artifacts = tmp_path / "artifacts"
    restore_point = artifacts / "restore" / "run-old" / "calibre-1"
    restore_point.mkdir(parents=True)
    (restore_point / "restore.json").write_text(
        json.dumps(
            {
                "run_id": "run-old",
                "book_key": "calibre:1",
                "calibre_book_id": 1,
                "applied_at": (datetime.now(UTC) - timedelta(days=31)).isoformat(),
                "fields_changed": ["title"],
                "ttl_seconds": 30 * 24 * 3600,
            }
        )
    )
    config = tmp_path / "config.yml"
    config.write_text(f"storage:\n  artifacts_dir: {artifacts}\n")

    preview = runner.invoke(app, ["-c", str(config), "retention"])
    assert preview.exit_code == 0
    assert "Expired restore points: 1" in preview.stdout
    assert "Dry run only" in preview.stdout
    assert restore_point.is_dir()

    unsafe = runner.invoke(app, ["-c", str(config), "retention", "--execute"])
    assert unsafe.exit_code == 1
    assert "--backup-reference is required" in unsafe.stdout
    assert restore_point.is_dir()

    executed = runner.invoke(
        app,
        [
            "-c",
            str(config),
            "retention",
            "--execute",
            "--backup-reference",
            "backup-20260712",
            "--confirm-writer-stopped",
        ],
    )
    assert executed.exit_code == 0
    assert "Deleted 1 expired restore points" in executed.stdout
    assert not restore_point.exists()


def test_ingest_paperless_disabled() -> None:
    # By default, Paperless integration is disabled
    result = runner.invoke(app, ["ingest-paperless"])
    assert result.exit_code == 1
    assert "Error: Paperless integration is disabled in settings." in result.stdout


def test_ingest_paperless_success() -> None:
    import os
    import tempfile
    from unittest.mock import AsyncMock, MagicMock, patch

    with tempfile.TemporaryDirectory() as tmp_dir:
        config_content = f"""
storage:
  sqlite_path: {tmp_dir}/test_db.db
  artifacts_dir: {tmp_dir}/artifacts
paperless:
  enabled: true
  base_url: http://fake-paperless
  import_document_types: [book_scan]
"""
        config_path = Path(tmp_dir) / "config.yml"
        config_path.write_text(config_content)

        # Mock PaperlessBridge
        mock_bridge = MagicMock()
        mock_bridge.test_connection = AsyncMock(return_value=True)
        mock_bridge.fetch_candidate_documents = AsyncMock(return_value=[{"id": 42, "title": "Scanned Novel"}])
        mock_bridge.download_document_file = AsyncMock(return_value=Path(tmp_dir) / "scanned_novel.pdf")

        # Create dummy file to simulate downloaded book
        (Path(tmp_dir) / "scanned_novel.pdf").write_bytes(b"dummy pdf bytes")

        with (
            patch(
                "calibre_ai_auditor.integrations.paperless.PaperlessBridge",
                return_value=mock_bridge,
            ),
            patch.dict(os.environ, {"PAPERLESS_TOKEN": "secret"}),
        ):
            result = runner.invoke(app, ["-c", str(config_path), "ingest-paperless"])
            assert result.exit_code == 0
            assert "Ingestion complete." in result.stdout
            assert "Ingesting document 42: Scanned Novel..." in result.stdout

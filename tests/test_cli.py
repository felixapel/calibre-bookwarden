from pathlib import Path

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

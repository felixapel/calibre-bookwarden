import hashlib
import json
import shutil
from datetime import UTC, datetime, timedelta
from pathlib import Path
from unittest.mock import MagicMock, patch

import pytest
from click.exceptions import Exit
from typer.testing import CliRunner

from calibre_ai_auditor.cli.main import _verify_backup_manifest, app
from calibre_ai_auditor.verification.restore import RestorePointStore

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


def test_legacy_direct_apply_is_disabled_in_every_profile() -> None:
    result = runner.invoke(app, ["apply", "--yes"])

    assert result.exit_code == 1
    assert "Legacy direct apply is disabled" in result.stdout


def test_calibrate_v2_writes_a_sealed_report_from_reviewed_labels(tmp_path: Path) -> None:
    from calibre_ai_auditor.verification.calibration_v2 import CalibrationReportV2

    corpus = tmp_path / "labels.json"
    corpus.write_text(
        json.dumps(
            {
                "schema_version": 1,
                "policy_version": "manifestation-v2",
                "observations": [
                    {
                        "evidence_id": "evidence-1",
                        "tier": "A",
                        "would_auto_apply": True,
                        "identity_correct": True,
                        "patch_correct": True,
                    }
                ],
            }
        )
    )
    output = tmp_path / "calibration.json"

    result = runner.invoke(
        app,
        ["calibrate-v2", "--corpus", str(corpus), "--output", str(output)],
    )

    assert result.exit_code == 0
    report = CalibrationReportV2.model_validate_json(output.read_text())
    assert report.verify_seal()
    assert "false_auto_apply=0" in result.stdout


def test_migrate_runs_explicit_schema_upgrade() -> None:
    with patch("calibre_ai_auditor.cli.main.init_db") as upgrade:
        result = runner.invoke(app, ["migrate"])

    assert result.exit_code == 0
    upgrade.assert_called_once()
    assert "Database schema upgraded" in result.stdout


def test_retention_is_dry_run_and_requires_verified_backup(tmp_path: Path) -> None:
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
    database_dump = tmp_path / "database.dump"
    artifacts_archive = tmp_path / "artifacts.tar"
    database_dump.write_bytes(b"database backup")
    artifacts_archive.write_bytes(b"artifact backup")
    backup_manifest = tmp_path / "backup.json"
    backup_manifest.write_text(
        json.dumps(
            {
                "created_at": datetime.now(UTC).isoformat(),
                "database_dump": database_dump.name,
                "database_sha256": hashlib.sha256(database_dump.read_bytes()).hexdigest(),
                "artifacts_archive": artifacts_archive.name,
                "artifacts_sha256": hashlib.sha256(artifacts_archive.read_bytes()).hexdigest(),
            }
        )
    )

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
            str(backup_manifest),
        ],
    )
    assert executed.exit_code == 0
    assert "Deleted 1 expired restore points" in executed.stdout
    assert not restore_point.exists()


def test_production_retention_refuses_when_writer_owns_guard(tmp_path: Path) -> None:
    artifacts = tmp_path / "artifacts"
    restore_point = artifacts / "restore" / "run-old" / "calibre-1"
    restore_point.mkdir(parents=True)
    (restore_point / "restore.json").write_text(
        json.dumps(
            {
                "run_id": "run-old",
                "book_key": "calibre:1",
                "applied_at": (datetime.now(UTC) - timedelta(days=31)).isoformat(),
                "ttl_seconds": 30 * 24 * 3600,
            }
        )
    )
    database_dump = tmp_path / "database.dump"
    artifacts_archive = tmp_path / "artifacts.tar"
    database_dump.write_bytes(b"database backup")
    artifacts_archive.write_bytes(b"artifact backup")
    backup_manifest = tmp_path / "backup.json"
    backup_manifest.write_text(
        json.dumps(
            {
                "created_at": datetime.now(UTC).isoformat(),
                "database_dump": database_dump.name,
                "database_sha256": hashlib.sha256(database_dump.read_bytes()).hexdigest(),
                "artifacts_archive": artifacts_archive.name,
                "artifacts_sha256": hashlib.sha256(artifacts_archive.read_bytes()).hexdigest(),
            }
        )
    )
    config = tmp_path / "config.yml"
    config.write_text(f"profile: production\nstorage:\n  artifacts_dir: {artifacts}\ndatabase:\n  backend: postgres\n")
    engine = MagicMock()
    engine.dialect.name = "postgresql"

    with (
        patch("calibre_ai_auditor.cli.main.get_engine", return_value=engine),
        patch("calibre_ai_auditor.apply.guard.acquire_writer_guard", return_value=False),
    ):
        result = runner.invoke(
            app,
            [
                "-c",
                str(config),
                "retention",
                "--execute",
                "--backup-reference",
                str(backup_manifest),
            ],
        )

    assert result.exit_code == 1
    assert "Metadata writer is active" in result.stdout
    assert restore_point.is_dir()
    engine.connect.return_value.close.assert_called_once()


def test_backup_manifest_rejects_symlinked_input(tmp_path: Path) -> None:
    real_dump = tmp_path / "real.dump"
    real_dump.write_bytes(b"database backup")
    symlinked_dump = tmp_path / "database.dump"
    symlinked_dump.symlink_to(real_dump)
    archive = tmp_path / "artifacts.tar"
    archive.write_bytes(b"artifact backup")
    manifest = tmp_path / "backup.json"
    manifest.write_text(
        json.dumps(
            {
                "created_at": datetime.now(UTC).isoformat(),
                "database_dump": symlinked_dump.name,
                "database_sha256": hashlib.sha256(real_dump.read_bytes()).hexdigest(),
                "artifacts_archive": archive.name,
                "artifacts_sha256": hashlib.sha256(archive.read_bytes()).hexdigest(),
            }
        )
    )

    with pytest.raises(Exit):
        _verify_backup_manifest(manifest)


def test_backup_manifest_rejects_symlinked_parent(tmp_path: Path) -> None:
    backup_root = tmp_path / "real-backups"
    backup_root.mkdir()
    database_dump = backup_root / "database.dump"
    database_dump.write_bytes(b"database backup")
    archive = backup_root / "artifacts.tar"
    archive.write_bytes(b"artifact backup")
    manifest = backup_root / "backup.json"
    manifest.write_text(
        json.dumps(
            {
                "created_at": datetime.now(UTC).isoformat(),
                "database_dump": database_dump.name,
                "database_sha256": hashlib.sha256(database_dump.read_bytes()).hexdigest(),
                "artifacts_archive": archive.name,
                "artifacts_sha256": hashlib.sha256(archive.read_bytes()).hexdigest(),
            }
        )
    )
    linked_root = tmp_path / "linked-backups"
    linked_root.symlink_to(backup_root, target_is_directory=True)

    with pytest.raises(Exit):
        _verify_backup_manifest(linked_root / manifest.name)


def test_production_retention_closes_connection_when_lock_acquisition_errors(tmp_path: Path) -> None:
    artifacts = tmp_path / "artifacts"
    artifacts.mkdir()
    database_dump = tmp_path / "database.dump"
    database_dump.write_bytes(b"database backup")
    archive = tmp_path / "artifacts.tar"
    archive.write_bytes(b"artifact backup")
    manifest = tmp_path / "backup.json"
    manifest.write_text(
        json.dumps(
            {
                "created_at": datetime.now(UTC).isoformat(),
                "database_dump": database_dump.name,
                "database_sha256": hashlib.sha256(database_dump.read_bytes()).hexdigest(),
                "artifacts_archive": archive.name,
                "artifacts_sha256": hashlib.sha256(archive.read_bytes()).hexdigest(),
            }
        )
    )
    config = tmp_path / "config.yml"
    config.write_text(f"profile: production\nstorage:\n  artifacts_dir: {artifacts}\ndatabase:\n  backend: postgres\n")
    engine = MagicMock()
    engine.dialect.name = "postgresql"

    with (
        patch("calibre_ai_auditor.cli.main.get_engine", return_value=engine),
        patch(
            "calibre_ai_auditor.apply.guard.acquire_writer_guard",
            side_effect=RuntimeError("database failure"),
        ),
    ):
        result = runner.invoke(
            app,
            ["-c", str(config), "retention", "--execute", "--backup-reference", str(manifest)],
        )

    assert result.exit_code == 1
    engine.connect.return_value.close.assert_called_once()


def test_retention_recovery_is_explicit_and_requires_verified_backup(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch
) -> None:
    artifacts = tmp_path / "artifacts"
    restore_point = artifacts / "restore" / "run-old" / "calibre-1"
    restore_point.mkdir(parents=True)
    (restore_point / "restore.json").write_text(
        json.dumps(
            {
                "run_id": "run-old",
                "book_key": "calibre:1",
                "applied_at": (datetime.now(UTC) - timedelta(days=31)).isoformat(),
                "ttl_seconds": 30 * 24 * 3600,
            }
        )
    )
    database_dump = tmp_path / "database.dump"
    database_dump.write_bytes(b"database backup")
    archive = tmp_path / "artifacts.tar"
    archive.write_bytes(b"artifact backup")
    backup_manifest = tmp_path / "backup.json"
    backup_manifest.write_text(
        json.dumps(
            {
                "created_at": datetime.now(UTC).isoformat(),
                "database_dump": database_dump.name,
                "database_sha256": hashlib.sha256(database_dump.read_bytes()).hexdigest(),
                "artifacts_archive": archive.name,
                "artifacts_sha256": hashlib.sha256(archive.read_bytes()).hexdigest(),
            }
        )
    )
    backup_digest = hashlib.sha256(backup_manifest.read_bytes()).hexdigest()
    store = RestorePointStore(artifacts)
    manifest = store.build_deletion_manifest()
    real_rmtree = shutil.rmtree
    monkeypatch.setattr(shutil, "rmtree", MagicMock(side_effect=OSError("interrupted")))
    with pytest.raises(OSError, match="interrupted"):
        store.quarantine_and_delete(manifest, backup_manifest_sha256=backup_digest)
    monkeypatch.setattr(shutil, "rmtree", real_rmtree)
    transaction_id = store.list_pending_quarantines()[0].name
    config = tmp_path / "config.yml"
    config.write_text(f"storage:\n  artifacts_dir: {artifacts}\n")

    normal = runner.invoke(app, ["-c", str(config), "retention"])
    assert normal.exit_code == 1
    assert "Pending retention quarantine" in normal.stdout

    preview_recovery = runner.invoke(
        app,
        ["-c", str(config), "retention", "--recover-quarantine", transaction_id],
    )
    assert preview_recovery.exit_code == 1
    assert "--recover-quarantine requires --execute" in preview_recovery.stdout

    missing_backup = runner.invoke(
        app,
        ["-c", str(config), "retention", "--recover-quarantine", transaction_id, "--execute"],
    )
    assert missing_backup.exit_code == 1
    assert "--backup-reference is required" in missing_backup.stdout

    recovered = runner.invoke(
        app,
        [
            "-c",
            str(config),
            "retention",
            "--recover-quarantine",
            transaction_id,
            "--execute",
            "--backup-reference",
            str(backup_manifest),
        ],
    )
    assert recovered.exit_code == 0
    assert "Recovered deletion of 1 quarantined restore points" in recovered.stdout
    assert store.list_pending_quarantines() == []


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

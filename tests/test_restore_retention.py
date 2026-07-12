import json
from datetime import UTC, datetime, timedelta
from pathlib import Path

import pytest

from calibre_ai_auditor.verification.restore import RestorePointStore


def _expired_restore_point(root: Path) -> Path:
    restore_point = root / "restore" / "run-old" / "calibre-1"
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
    return restore_point


def test_retention_manifest_deletes_only_prevalidated_restore_point(tmp_path: Path) -> None:
    restore_point = _expired_restore_point(tmp_path)
    store = RestorePointStore(tmp_path)

    manifest = store.build_deletion_manifest()

    assert store.quarantine_and_delete(manifest) == 1
    assert not restore_point.exists()


def test_retention_aborts_before_mutation_when_manifest_changes(tmp_path: Path) -> None:
    restore_point = _expired_restore_point(tmp_path)
    store = RestorePointStore(tmp_path)
    manifest = store.build_deletion_manifest()
    (restore_point / "restore.json").write_text("{}")

    with pytest.raises(RuntimeError, match="changed after preview"):
        store.quarantine_and_delete(manifest)

    assert restore_point.is_dir()


def test_retention_fails_closed_on_unmanaged_or_malformed_entry(tmp_path: Path) -> None:
    restore_point = tmp_path / "restore" / "run-bad" / "calibre-2"
    restore_point.mkdir(parents=True)
    (restore_point / "restore.json").write_text("not-json")

    with pytest.raises(RuntimeError, match="Invalid restore manifest"):
        RestorePointStore(tmp_path).build_deletion_manifest()

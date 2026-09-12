import json
import os
import shutil
from datetime import UTC, datetime, timedelta
from pathlib import Path

import pytest

from calibre_ai_auditor.verification import restore as restore_module
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


def _second_expired_restore_point(root: Path) -> Path:
    restore_point = root / "restore" / "run-old" / "calibre-2"
    restore_point.mkdir(parents=True)
    (restore_point / "restore.json").write_text(
        json.dumps(
            {
                "run_id": "run-old",
                "book_key": "calibre:2",
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

    assert store.quarantine_and_delete(manifest, backup_manifest_sha256="a" * 64) == 1
    assert not restore_point.exists()


def test_retention_aborts_before_mutation_when_manifest_changes(tmp_path: Path) -> None:
    restore_point = _expired_restore_point(tmp_path)
    store = RestorePointStore(tmp_path)
    manifest = store.build_deletion_manifest()
    (restore_point / "restore.json").write_text("{}")

    with pytest.raises(RuntimeError, match="changed after preview"):
        store.quarantine_and_delete(manifest, backup_manifest_sha256="a" * 64)

    assert restore_point.is_dir()


def test_retention_fails_closed_on_unmanaged_or_malformed_entry(tmp_path: Path) -> None:
    restore_point = tmp_path / "restore" / "run-bad" / "calibre-2"
    restore_point.mkdir(parents=True)
    (restore_point / "restore.json").write_text("not-json")

    with pytest.raises(RuntimeError, match="Invalid restore manifest"):
        RestorePointStore(tmp_path).build_deletion_manifest()


def test_retention_recovers_transaction_interrupted_after_first_rename(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch
) -> None:
    first = _expired_restore_point(tmp_path)
    second = _second_expired_restore_point(tmp_path)
    store = RestorePointStore(tmp_path)
    manifest = store.build_deletion_manifest()
    real_rename = os.rename
    moved_candidates = 0

    def kill_after_first_candidate(source: Path, target: Path) -> None:
        nonlocal moved_candidates
        real_rename(source, target)
        if Path(source) in {first, second}:
            moved_candidates += 1
            if moved_candidates == 1:
                raise SystemExit("simulated SIGKILL boundary")

    monkeypatch.setattr(os, "rename", kill_after_first_candidate)
    with pytest.raises(SystemExit, match="SIGKILL"):
        store.quarantine_and_delete(manifest, backup_manifest_sha256="a" * 64)
    monkeypatch.setattr(os, "rename", real_rename)

    with pytest.raises(RuntimeError, match="Pending retention quarantine"):
        store.build_deletion_manifest()
    pending = store.list_pending_quarantines()
    assert len(pending) == 1
    assert store.recover_quarantine(pending[0].name, backup_manifest_sha256="a" * 64) == 2
    assert not first.exists()
    assert not second.exists()
    assert store.list_pending_quarantines() == []


def test_retention_recovers_transaction_interrupted_during_payload_delete(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch
) -> None:
    restore_point = _expired_restore_point(tmp_path)
    store = RestorePointStore(tmp_path)
    manifest = store.build_deletion_manifest()
    real_rmtree = shutil.rmtree

    def interrupt_payload_delete(path: Path) -> None:
        payload = Path(path)
        first_file = next(candidate for candidate in payload.rglob("*") if candidate.is_file())
        first_file.unlink()
        raise OSError("simulated delete interruption")

    monkeypatch.setattr(shutil, "rmtree", interrupt_payload_delete)
    with pytest.raises(OSError, match="delete interruption"):
        store.quarantine_and_delete(manifest, backup_manifest_sha256="a" * 64)
    monkeypatch.setattr(shutil, "rmtree", real_rmtree)

    pending = store.list_pending_quarantines()
    assert len(pending) == 1
    assert store.recover_quarantine(pending[0].name, backup_manifest_sha256="a" * 64) == 1
    assert not restore_point.exists()
    assert store.list_pending_quarantines() == []
    journal = json.loads((pending[0] / "transaction.json").read_text())
    assert journal["state"] == "deleted"


def test_retention_rejects_unknown_hidden_restore_entry(tmp_path: Path) -> None:
    hidden = tmp_path / "restore" / ".operator-files"
    hidden.mkdir(parents=True)

    with pytest.raises(RuntimeError, match="Unmanaged restore entry"):
        RestorePointStore(tmp_path).build_deletion_manifest()


@pytest.mark.parametrize(
    ("field", "value"),
    [
        ("moved", ["run-old/calibre-1", "run-old/calibre-1"]),
        ("moved", ["run-old/calibre-99"]),
        (
            "candidates",
            [
                {
                    "relative_path": ".retention-quarantine/escape",
                    "manifest_sha256": "a" * 64,
                    "device": 1,
                    "inode": 1,
                }
            ],
        ),
    ],
)
def test_retention_rejects_corrupt_journal_inventory(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch, field: str, value: object
) -> None:
    original = _expired_restore_point(tmp_path)
    store = RestorePointStore(tmp_path)
    real_rename = os.rename

    def interrupt(source: Path, target: Path) -> None:
        real_rename(source, target)
        if Path(source) == original:
            raise SystemExit("interrupt")

    monkeypatch.setattr(os, "rename", interrupt)
    with pytest.raises(SystemExit):
        store.quarantine_and_delete(store.build_deletion_manifest(), backup_manifest_sha256="a" * 64)
    transaction = next((tmp_path / "restore" / ".retention-quarantine").iterdir())
    journal_path = transaction / "transaction.json"
    journal = json.loads(journal_path.read_text())
    journal[field] = value
    journal_path.write_text(json.dumps(journal))

    with pytest.raises(RuntimeError, match="Invalid retention quarantine journal"):
        store.list_pending_quarantines()


def test_retention_recovery_rejects_symlinked_payload_before_mutation(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch
) -> None:
    original = _expired_restore_point(tmp_path)
    store = RestorePointStore(tmp_path)
    real_rename = os.rename

    def interrupt(source: Path, target: Path) -> None:
        real_rename(source, target)
        if Path(source) == original:
            raise SystemExit("interrupt")

    monkeypatch.setattr(os, "rename", interrupt)
    with pytest.raises(SystemExit):
        store.quarantine_and_delete(store.build_deletion_manifest(), backup_manifest_sha256="a" * 64)
    monkeypatch.setattr(os, "rename", real_rename)
    transaction = store.list_pending_quarantines()[0]
    payload = transaction / "payload"
    moved_payload = transaction / "real-payload"
    payload.rename(moved_payload)
    payload.symlink_to(moved_payload, target_is_directory=True)

    with pytest.raises(RuntimeError, match="Unsafe retention quarantine payload"):
        store.recover_quarantine(transaction.name, backup_manifest_sha256="a" * 64)
    assert not original.exists()
    assert moved_payload.exists()


def test_retention_recovery_rejects_symlinked_quarantine_root(tmp_path: Path, monkeypatch: pytest.MonkeyPatch) -> None:
    original = _expired_restore_point(tmp_path)
    store = RestorePointStore(tmp_path)
    real_rename = os.rename

    def interrupt(source: Path, target: Path) -> None:
        real_rename(source, target)
        if Path(source) == original:
            raise SystemExit("interrupt")

    monkeypatch.setattr(os, "rename", interrupt)
    with pytest.raises(SystemExit):
        store.quarantine_and_delete(store.build_deletion_manifest(), backup_manifest_sha256="a" * 64)
    monkeypatch.setattr(os, "rename", real_rename)
    quarantine_root = store.restore_root / ".retention-quarantine"
    transaction_id = next(quarantine_root.iterdir()).name
    moved_root = store.restore_root / "quarantine-copy"
    quarantine_root.rename(moved_root)
    quarantine_root.symlink_to(moved_root, target_is_directory=True)

    with pytest.raises(RuntimeError, match="Unsafe retention quarantine"):
        store.recover_quarantine(transaction_id, backup_manifest_sha256="a" * 64)


def test_retention_recovery_requires_the_bound_backup(tmp_path: Path, monkeypatch: pytest.MonkeyPatch) -> None:
    original = _expired_restore_point(tmp_path)
    store = RestorePointStore(tmp_path)
    real_rename = os.rename

    def interrupt(source: Path, target: Path) -> None:
        real_rename(source, target)
        if Path(source) == original:
            raise SystemExit("interrupt")

    monkeypatch.setattr(os, "rename", interrupt)
    with pytest.raises(SystemExit):
        store.quarantine_and_delete(store.build_deletion_manifest(), backup_manifest_sha256="a" * 64)
    monkeypatch.setattr(os, "rename", real_rename)
    transaction = store.list_pending_quarantines()[0]

    with pytest.raises(RuntimeError, match="does not match the transaction"):
        store.recover_quarantine(transaction.name, backup_manifest_sha256="b" * 64)


def test_retention_persists_namespace_changes_before_advancing_journal(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch
) -> None:
    restore_point = _expired_restore_point(tmp_path)
    store = RestorePointStore(tmp_path)
    events: list[tuple[str, str, str]] = []
    real_fsync = restore_module._fsync_directory
    real_write = restore_module._write_json_atomically
    real_rename = os.rename

    def record_fsync(path: Path) -> None:
        events.append(("fsync", str(path), ""))
        real_fsync(path)

    def record_write(path: Path, payload: dict[str, object]) -> None:
        events.append(("journal", str(path), str(payload["state"])))
        real_write(path, payload)

    def record_rename(source: Path, target: Path) -> None:
        events.append(("rename", str(source), str(target)))
        real_rename(source, target)

    monkeypatch.setattr(restore_module, "_fsync_directory", record_fsync)
    monkeypatch.setattr(restore_module, "_write_json_atomically", record_write)
    monkeypatch.setattr(os, "rename", record_rename)

    assert (
        store.quarantine_and_delete(
            store.build_deletion_manifest(),
            backup_manifest_sha256="a" * 64,
        )
        == 1
    )

    transaction = next((store.restore_root / ".retention-quarantine").iterdir())
    payload_root = transaction / "payload"
    target_parent = payload_root / "run-old"
    prepared = next(index for index, event in enumerate(events) if event[0] == "journal" and event[2] == "prepared")
    staging = Path(events[prepared][1]).parent
    publication = events.index(("rename", str(staging), str(transaction)))
    rename = events.index(("rename", str(restore_point), str(target_parent / "calibre-1")))
    moved_journal = next(
        index
        for index, event in enumerate(events)
        if index > rename and event == ("journal", str(transaction / "transaction.json"), "quarantining")
    )
    deleted = events.index(("journal", str(transaction / "transaction.json"), "deleted"))

    assert events.index(("fsync", str(store.restore_root), "")) < prepared
    assert events.index(("fsync", str(transaction.parent), "")) < prepared
    assert events.index(("fsync", str(staging), "")) < prepared
    assert prepared < publication < events.index(("fsync", str(transaction.parent), ""), publication)
    assert events.index(("fsync", str(payload_root), "")) < rename
    assert rename < events.index(("fsync", str(restore_point.parent), "")) < moved_journal
    assert rename < events.index(("fsync", str(target_parent), "")) < moved_journal
    assert events.index(("fsync", str(transaction), ""), rename) < deleted


def test_retention_discards_staging_abandoned_before_first_journal(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch
) -> None:
    restore_point = _expired_restore_point(tmp_path)
    store = RestorePointStore(tmp_path)

    def interrupt_first_journal(path: Path, payload: dict[str, object]) -> None:
        raise OSError("journal interruption")

    monkeypatch.setattr(restore_module, "_write_json_atomically", interrupt_first_journal)
    with pytest.raises(OSError, match="journal interruption"):
        store.quarantine_and_delete(
            store.build_deletion_manifest(),
            backup_manifest_sha256="a" * 64,
        )

    monkeypatch.undo()
    assert store.list_pending_quarantines() == []
    assert restore_point.exists()
    assert list((store.restore_root / ".retention-quarantine").iterdir()) == []


def test_retention_discards_staging_abandoned_after_first_journal(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch
) -> None:
    restore_point = _expired_restore_point(tmp_path)
    store = RestorePointStore(tmp_path)
    real_rename = os.rename

    def interrupt_publication(source: Path, target: Path) -> None:
        if Path(source).name.startswith(".staging-"):
            raise OSError("publication interruption")
        real_rename(source, target)

    monkeypatch.setattr(os, "rename", interrupt_publication)
    with pytest.raises(OSError, match="publication interruption"):
        store.quarantine_and_delete(
            store.build_deletion_manifest(),
            backup_manifest_sha256="a" * 64,
        )

    monkeypatch.setattr(os, "rename", real_rename)
    assert store.list_pending_quarantines() == []
    assert restore_point.exists()
    assert list((store.restore_root / ".retention-quarantine").iterdir()) == []


def test_cleanup_expired_skips_symlinked_run_dir(tmp_path: Path) -> None:
    outside = tmp_path / "outside"
    outside.mkdir()
    secret = outside / "secret.txt"
    secret.write_text("do not touch")
    link = tmp_path / "restore" / "run-evil"
    link.parent.mkdir(parents=True)
    link.symlink_to(outside, target_is_directory=True)

    store = RestorePointStore(tmp_path)
    assert store.cleanup_expired() == 0
    assert secret.read_text() == "do not touch"
    assert store.list_all() == []

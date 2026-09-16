"""Regression tests for disabled experimental mutation paths."""

from __future__ import annotations

import sqlite3
from pathlib import Path

import pytest

from calibre_ai_auditor.covers.optimizer import CoverOptimizer
from calibre_ai_auditor.integrations.homelab_bridge import CooperativeAdvisoryLock, HomelabBridge
from calibre_ai_auditor.security.cryptographic_ledger import CryptographicLedger


def test_ledger_constructor_and_entrypoints_do_not_create_schema_or_files(tmp_path: Path) -> None:
    ledger_path = tmp_path / "ledger.sqlite"
    restore_dir = tmp_path / "restore_points"
    ledger = CryptographicLedger(ledger_path)

    assert not ledger_path.exists()
    assert not restore_dir.exists()
    with pytest.raises(PermissionError, match="supervised Manifestation V2 apply writer"), ledger.connect():
        pytest.fail("retired ledger opened a database")
    assert not ledger_path.exists()
    with pytest.raises(PermissionError, match="supervised Manifestation V2 apply writer"):
        ledger.create_restore_point(1, "rename", "tester", tmp_path, {}, {})
    with pytest.raises(PermissionError, match="supervised Manifestation V2 apply writer"):
        ledger.quarantine_book_files(1, tmp_path, "test")
    with (
        sqlite3.connect(":memory:") as calibre_conn,
        pytest.raises(PermissionError, match="supervised Manifestation V2 apply writer"),
    ):
        ledger.rollback("legacy", calibre_conn, tmp_path)
    assert not ledger_path.exists()
    assert not restore_dir.exists()


def test_retired_lock_and_bridge_do_not_touch_lock_files_thumbnails_or_network(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch
) -> None:
    def deny_network(*args: object, **kwargs: object) -> None:
        pytest.fail("retired bridge attempted network access")

    monkeypatch.setattr("urllib.request.urlopen", deny_network)
    lock = CooperativeAdvisoryLock(tmp_path)
    with pytest.raises(RuntimeError, match="does not protect"):
        lock.acquire()
    assert not lock.lock_file.exists()

    thumbnails = tmp_path / "thumbnails"
    thumbnails.mkdir()
    thumbnail = thumbnails / "12.jpg"
    thumbnail.write_bytes(b"unchanged")
    bridge = HomelabBridge("http://invalid.example", thumbnails)
    with pytest.raises(PermissionError, match="retired"):
        bridge.invalidate_book_thumbnails(12)
    with pytest.raises(PermissionError, match="retired"):
        bridge.trigger_calibre_web_reconnect()
    assert thumbnail.read_bytes() == b"unchanged"


def test_retired_optimizer_rejects_before_changing_the_cover_or_library(tmp_path: Path) -> None:
    cover = tmp_path / "cover.jpg"
    cover.write_bytes(b"fixture-cover")
    optimizer = CoverOptimizer(tmp_path)

    with pytest.raises(PermissionError, match="retired"):
        optimizer.optimize_cover(cover)
    with pytest.raises(PermissionError, match="retired"):
        optimizer.scan_and_optimize_all()

    assert cover.read_bytes() == b"fixture-cover"
    assert not (tmp_path / "cover.orig_bak").exists()

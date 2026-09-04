from pathlib import Path
from unittest.mock import MagicMock, patch

from calibre_ai_auditor.integrations.calibre_web import CalibreWebSyncManager


def test_calibre_web_sync_local_invalidation(tmp_path: Path):
    thumb_dir = tmp_path / "thumbnails"
    thumb_dir.mkdir()
    (thumb_dir / "101.jpg").write_bytes(b"dummy1")
    (thumb_dir / "102.jpg").write_bytes(b"dummy2")
    (thumb_dir / "103.jpg").write_bytes(b"dummy3")

    mgr = CalibreWebSyncManager(thumbnails_dir=thumb_dir)

    # Invalidate only book 101 and 103
    count = mgr.invalidate_book_thumbnails([101, 103])
    assert count == 2
    assert not (thumb_dir / "101.jpg").exists()
    assert (thumb_dir / "102.jpg").exists()
    assert not (thumb_dir / "103.jpg").exists()


@patch("urllib.request.urlopen")
def test_calibre_web_reconnect_http(mock_urlopen):
    mock_resp = MagicMock()
    mock_resp.status = 200
    mock_urlopen.return_value.__enter__.return_value = mock_resp

    mgr = CalibreWebSyncManager(calibre_web_url="http://mock-calibre-web:8083")
    assert mgr.trigger_reconnect() is True


@patch("subprocess.run")
def test_calibre_web_sighup_reload(mock_run):
    mock_proc = MagicMock()
    mock_proc.returncode = 0
    mock_run.return_value = mock_proc

    mgr = CalibreWebSyncManager(container_name="test-cps")
    assert mgr.reload_workers_sighup() is True
    mock_run.assert_called_once()
    assert "pkill -HUP" in mock_run.call_args[0][0][-1]


def test_calibre_web_sync_rejects_malicious_book_ids():
    mgr = CalibreWebSyncManager()
    import pytest

    # Injection string attempt
    with pytest.raises(ValueError, match="Invalid book ID"):
        mgr.invalidate_book_thumbnails(["1; rm -rf /; echo "])  # type: ignore

    # Negative book ID
    with pytest.raises(ValueError, match="Invalid book ID"):
        mgr.invalidate_book_thumbnails([-5])

from pathlib import Path
from typing import Any

import pytest

from calibre_ai_auditor.config.settings import load_settings


def test_load_settings_default(tmp_path: Any, monkeypatch: pytest.MonkeyPatch) -> None:
    monkeypatch.chdir(tmp_path)
    settings = load_settings()
    assert settings.profile == "default"
    assert settings.storage.sqlite_path == Path(".state/bookaudit.db")


def test_read_only_env_alias(monkeypatch: pytest.MonkeyPatch, tmp_path: Any) -> None:
    monkeypatch.chdir(tmp_path)
    monkeypatch.setenv("BOOKAUDIT_READ_ONLY", "false")
    settings = load_settings()
    assert settings.library.read_only is False


def test_load_settings_yaml(tmp_path: Any) -> None:
    config_path = tmp_path / "config.yml"
    config_path.write_text("profile: custom\nlibrary:\n  path: /tmp/lib")
    settings = load_settings(config_path)
    assert settings.profile == "custom"
    assert str(settings.library.path) == "/tmp/lib"

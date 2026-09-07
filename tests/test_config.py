from pathlib import Path
from typing import Any

import pytest

from calibre_ai_auditor.config.settings import load_settings
from calibre_ai_auditor.storage.db import get_engine


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
    assert Path(settings.library.path) == Path("/tmp/lib")


def test_manifestation_v2_recognition_defaults_are_bounded_and_local_first(
    tmp_path: Any, monkeypatch: pytest.MonkeyPatch
) -> None:
    monkeypatch.chdir(tmp_path)

    settings = load_settings()

    assert settings.recognition_v2.ocr.backends == ["tesseract"]
    assert settings.recognition_v2.ocr.max_pages == 6
    assert settings.recognition_v2.vision.enabled is False
    assert settings.privacy.allow_remote_images is False
    assert settings.manifestation_v2.auto_apply.enabled is False
    assert settings.manifestation_v2.auto_apply.calibration_report is None
    assert settings.manifestation_v2.supervised_pilot.enabled is False
    assert settings.manifestation_v2.supervised_pilot.max_operations == 5


def test_nested_environment_overrides_yaml(tmp_path: Any, monkeypatch: pytest.MonkeyPatch) -> None:
    config_path = tmp_path / "config.yml"
    config_path.write_text("database:\n  backend: sqlite\n  postgres_dsn: postgresql+psycopg://yaml.invalid/db\n")
    monkeypatch.setenv("BOOKAUDIT_DATABASE__BACKEND", "postgres")
    monkeypatch.setenv("BOOKAUDIT_DATABASE__POSTGRES_DSN", "postgresql+psycopg://env.invalid/db")

    settings = load_settings(config_path)

    assert settings.database.backend == "postgres"
    assert settings.database.postgres_dsn == "postgresql+psycopg://env.invalid/db"


def test_postgres_backend_requires_dsn(monkeypatch: pytest.MonkeyPatch, tmp_path: Any) -> None:
    monkeypatch.chdir(tmp_path)
    monkeypatch.delenv("BOOKAUDIT_DATABASE__POSTGRES_DSN", raising=False)
    settings = load_settings()
    settings.database.backend = "postgres"
    settings.database.postgres_dsn = None

    with pytest.raises(ValueError, match="POSTGRES_DSN"):
        get_engine(settings)

import pytest
from fastapi import HTTPException

from calibre_ai_auditor.config.settings import Settings
from calibre_ai_auditor.web.safety import require_write_confirmation


def test_require_write_confirmation_read_only() -> None:
    settings = Settings()
    settings.library.read_only = True
    with pytest.raises(HTTPException) as exc:
        require_write_confirmation(settings, force=True)
    assert exc.value.status_code == 400
    assert "read-only" in exc.value.detail.lower()


def test_require_write_confirmation_missing_force() -> None:
    settings = Settings()
    settings.library.read_only = False
    with pytest.raises(HTTPException) as exc:
        require_write_confirmation(settings, force=False)
    assert exc.value.status_code == 400
    assert "force" in exc.value.detail.lower()


def test_require_write_confirmation_ok() -> None:
    settings = Settings()
    settings.library.read_only = False
    require_write_confirmation(settings, force=True)

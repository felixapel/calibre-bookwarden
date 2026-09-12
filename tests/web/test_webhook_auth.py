import os

import pytest
from fastapi import HTTPException

from calibre_ai_auditor.config.settings import Settings
from calibre_ai_auditor.web.auth import verify_paperless_webhook


def test_paperless_webhook_secret_required(monkeypatch: pytest.MonkeyPatch) -> None:
    monkeypatch.setenv("PAPERLESS_WEBHOOK_SECRET", "test-secret")
    settings = Settings()

    class FakeRequest:
        headers: dict[str, str] = {}

    with pytest.raises(HTTPException) as exc:
        verify_paperless_webhook(FakeRequest(), settings)  # type: ignore[arg-type]
    assert exc.value.status_code == 401


def test_paperless_webhook_secret_accepts_header(monkeypatch: pytest.MonkeyPatch) -> None:
    monkeypatch.setenv("PAPERLESS_WEBHOOK_SECRET", "test-secret")
    settings = Settings()

    class FakeRequest:
        headers = {"X-Webhook-Secret": "test-secret"}

    verify_paperless_webhook(FakeRequest(), settings)  # type: ignore[arg-type]


def test_paperless_webhook_skips_when_unconfigured() -> None:
    os.environ.pop("PAPERLESS_WEBHOOK_SECRET", None)
    settings = Settings()

    class FakeRequest:
        headers: dict[str, str] = {}

    verify_paperless_webhook(FakeRequest(), settings)  # type: ignore[arg-type]


def test_paperless_webhook_non_ascii_secret_fails_closed(monkeypatch: pytest.MonkeyPatch) -> None:
    """Non-ASCII header bytes must yield 401, never an unhandled TypeError/500."""
    monkeypatch.setenv("PAPERLESS_WEBHOOK_SECRET", "test-secret")
    settings = Settings()

    class FakeRequest:
        headers = {"X-Webhook-Secret": "tëst-sëcret"}

    with pytest.raises(HTTPException) as exc:
        verify_paperless_webhook(FakeRequest(), settings)  # type: ignore[arg-type]
    assert exc.value.status_code == 401

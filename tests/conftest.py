import os
from collections.abc import Iterator

import pytest


@pytest.fixture(autouse=True)
def isolate_bookaudit_env(monkeypatch: pytest.MonkeyPatch) -> Iterator[None]:
    """Prevent developer .env / Docker paths from breaking unit tests."""
    preserved: dict[str, str | None] = {}
    for key in list(os.environ):
        if key.startswith("BOOKAUDIT_"):
            preserved[key] = os.environ.pop(key)
    monkeypatch.setenv("BOOKAUDIT_DATABASE__BACKEND", "sqlite")

    import calibre_ai_auditor.storage.db as db_module

    db_module._engine = None
    yield
    db_module._engine = None
    for key, value in preserved.items():
        if value is None:
            os.environ.pop(key, None)
        else:
            os.environ[key] = value

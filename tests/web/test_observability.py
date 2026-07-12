import json
import logging

from fastapi.testclient import TestClient

from calibre_ai_auditor.web.app import app
from calibre_ai_auditor.web.observability import JsonFormatter


def test_request_id_is_returned_and_sanitized() -> None:
    client = TestClient(app)

    generated = client.get("/api/health/live")
    sanitized = client.get("/api/health/live", headers={"X-Request-ID": "invalid id with spaces"})

    assert generated.headers["X-Request-ID"]
    assert sanitized.headers["X-Request-ID"] != "invalid id with spaces"


def test_json_formatter_emits_structured_record() -> None:
    record = logging.LogRecord("bookaudit.test", logging.INFO, __file__, 1, "ready %s", ("now",), None)

    payload = json.loads(JsonFormatter().format(record))

    assert payload["level"] == "INFO"
    assert payload["logger"] == "bookaudit.test"
    assert payload["message"] == "ready now"
    assert payload["timestamp"].endswith("Z")

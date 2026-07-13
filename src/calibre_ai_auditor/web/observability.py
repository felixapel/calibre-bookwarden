"""Structured logging and request-correlation primitives."""

import json
import logging
from contextvars import ContextVar
from datetime import UTC, datetime
from typing import Any

request_id_context: ContextVar[str | None] = ContextVar("request_id", default=None)


class JsonFormatter(logging.Formatter):
    """Render stable one-line JSON logs without serializing arbitrary objects."""

    def format(self, record: logging.LogRecord) -> str:
        payload: dict[str, Any] = {
            "timestamp": datetime.fromtimestamp(record.created, UTC).isoformat().replace("+00:00", "Z"),
            "level": record.levelname,
            "logger": record.name,
            "message": record.getMessage(),
        }
        request_id = request_id_context.get()
        if request_id:
            payload["request_id"] = request_id
        if record.exc_info:
            payload["exception"] = self.formatException(record.exc_info)
        return json.dumps(payload, ensure_ascii=True, separators=(",", ":"))


def configure_logging(level: str, *, json_enabled: bool) -> None:
    """Configure the process root logger once at application startup."""
    root = logging.getLogger()
    root.setLevel(level.upper())
    if not root.handlers:
        root.addHandler(logging.StreamHandler())
    formatter: logging.Formatter = (
        JsonFormatter() if json_enabled else logging.Formatter("%(asctime)s %(levelname)s %(name)s %(message)s")
    )
    for handler in root.handlers:
        handler.setFormatter(formatter)

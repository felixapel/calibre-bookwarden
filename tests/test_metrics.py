"""Tests for the v1.0 Prometheus metrics module."""

from __future__ import annotations

from sqlalchemy import create_engine, text

from calibre_ai_auditor.apply import heartbeat as heartbeat_module
from calibre_ai_auditor.config.settings import Settings
from calibre_ai_auditor.verification.metrics import Metrics, get_metrics, reset_metrics
from calibre_ai_auditor.web.api import health as health_api


def test_counter_and_labels() -> None:
    m = Metrics()
    m.inc("test_events", labels={"kind": "a"})
    m.inc("test_events", labels={"kind": "a"})
    m.inc("test_events", value=3, labels={"kind": "b"})
    out = m.render()
    assert 'test_events{kind="a"} 2' in out
    assert 'test_events{kind="b"} 3' in out


def test_gauge() -> None:
    m = Metrics()
    m.gauge("queue_depth", 5.0, labels={"queue": "ocr"})
    out = m.render()
    assert "queue_depth" in out
    assert 'queue="ocr"' in out


def test_histogram() -> None:
    m = Metrics()
    m.observe("latency_ms", 10.0, labels={"op": "ocr"})
    m.observe("latency_ms", 20.0, labels={"op": "ocr"})
    m.observe("latency_ms", 30.0, labels={"op": "ocr"})
    out = m.render()
    assert 'latency_ms_count{op="ocr"} 3' in out
    assert 'latency_ms_sum{op="ocr"} 60.0' in out
    assert 'latency_ms_avg{op="ocr"} 20.0' in out


def test_histogram_storage_is_constant_size() -> None:
    m = Metrics()
    for value in range(10_000):
        m.observe("latency_ms", float(value), labels={"op": "http"})
    aggregate = next(iter(m._histograms.values()))
    assert aggregate == (10_000, 49_995_000.0)


def test_record_book_action_helper() -> None:
    m = Metrics()
    m.record_book_action("suggest_fix", "run_001")
    m.record_book_action("suggest_fix", "run_001")
    m.record_book_action("needs_review", "run_001")
    out = m.render()
    assert 'bookaudit_books_total{action="suggest_fix"} 2' in out
    assert 'bookaudit_books_total{action="needs_review"} 1' in out
    assert "run_id" not in out


def test_record_http_request_uses_bounded_labels() -> None:
    m = Metrics()
    m.record_http_request("GET", "/api/books/{book_key}", 200, 0.125)
    out = m.render()
    labels = 'method="GET",route="/api/books/{book_key}",status="200"'
    assert f"bookaudit_http_requests_total{{{labels}}} 1" in out
    assert f"bookaudit_http_request_duration_seconds_count{{{labels}}} 1" in out
    assert f"bookaudit_http_request_duration_seconds_sum{{{labels}}} 0.125" in out


def test_operational_health_helpers_expose_writer_and_durable_queue() -> None:
    m = Metrics()
    m.set_writer_health(fresh=True)
    m.set_outbox_depth("pending", 7)
    m.set_operation_depth("failed_rollback_failed", 2)
    out = m.render()
    assert "bookaudit_writer_heartbeat_fresh 1.0" in out
    assert 'bookaudit_outbox_events{status="pending"} 7.0' in out
    assert 'bookaudit_operations{state="failed_rollback_failed"} 2.0' in out


def test_record_llm_call_helper() -> None:
    m = Metrics()
    m.record_llm_call(
        provider="ollama",
        model="qwen3:8b",
        duration_ms=1200,
        tokens_in=200,
        tokens_out=80,
    )
    out = m.render()
    assert 'bookaudit_llm_calls_total{model="qwen3:8b",provider="ollama"} 1' in out
    assert 'bookaudit_llm_tokens_in_total{model="qwen3:8b",provider="ollama"} 200' in out
    assert 'bookaudit_llm_duration_ms_count{model="qwen3:8b",provider="ollama"} 1' in out


def test_record_ocr_pages() -> None:
    m = Metrics()
    m.record_ocr_pages("tesseract", 42)
    m.record_ocr_pages("paddleocr", 17)
    out = m.render()
    assert 'bookaudit_ocr_pages_total{provider="tesseract"} 42' in out
    assert 'bookaudit_ocr_pages_total{provider="paddleocr"} 17' in out


def test_singleton() -> None:
    reset_metrics()
    m1 = get_metrics()
    m2 = get_metrics()
    assert m1 is m2


def test_label_escaping() -> None:
    m = Metrics()
    m.inc("escaped", labels={"path": 'a"b\nc\\d'})
    out = m.render()
    # Quotes, newlines, backslashes should be escaped
    assert 'a\\"b\\nc\\\\d' in out


def test_empty_metrics_renders_cleanly() -> None:
    m = Metrics()
    out = m.render()
    assert out.endswith("\n")  # valid format
    assert out  # at least a trailing newline
    # No metric lines, but no errors either
    assert "# TYPE" not in out  # no histogram types


async def test_metrics_endpoint_collects_writer_and_durable_state(monkeypatch) -> None:
    engine = create_engine("sqlite://")
    with engine.begin() as connection:
        connection.execute(text("CREATE TABLE outboxevent (status TEXT NOT NULL)"))
        connection.execute(text("CREATE TABLE operationledger (state TEXT NOT NULL)"))
        connection.execute(text("INSERT INTO outboxevent VALUES ('pending'), ('pending')"))
        connection.execute(text("INSERT INTO operationledger VALUES ('failed_rollback_failed')"))

    monkeypatch.setattr(health_api, "get_engine", lambda _settings: engine)
    monkeypatch.setattr(heartbeat_module, "read_writer_heartbeat", lambda *_args, **_kwargs: {"owner": "writer"})
    monkeypatch.setattr(heartbeat_module, "heartbeat_is_fresh", lambda *_args, **_kwargs: True)
    reset_metrics()

    response = await health_api.prometheus_metrics(Settings())
    body = response.body.decode()
    assert "bookaudit_writer_heartbeat_fresh 1.0" in body
    assert 'bookaudit_outbox_events{status="pending"} 2.0' in body
    assert 'bookaudit_operations{state="failed_rollback_failed"} 1.0' in body
    assert "bookaudit_operational_metrics_collection_success 1.0" in body

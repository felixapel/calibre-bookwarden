"""Tests for the v1.0 Prometheus metrics module."""

from __future__ import annotations

from calibre_ai_auditor.verification.metrics import Metrics, get_metrics, reset_metrics


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


def test_record_book_action_helper() -> None:
    m = Metrics()
    m.record_book_action("suggest_fix", "run_001")
    m.record_book_action("suggest_fix", "run_001")
    m.record_book_action("needs_review", "run_001")
    out = m.render()
    assert 'bookaudit_books_total{action="suggest_fix",run_id="run_001"} 2' in out
    assert 'bookaudit_books_total{action="needs_review",run_id="run_001"} 1' in out


def test_record_llm_call_helper() -> None:
    m = Metrics()
    m.record_llm_call(
        provider="ollama", model="qwen3:8b",
        duration_ms=1200, tokens_in=200, tokens_out=80,
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
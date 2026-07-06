"""Prometheus metrics for v1.0 observability.

Lightweight Prometheus client without external dependency.  Exposes counters
and gauges for:
  - Per-action book counts (no_change | suggest_fix | needs_review | defer)
  - Per-host LLM call counts and token totals
  - Per-provider OCR page counts
  - Per-run elapsed time + books/second throughput
  - Restore point count + oldest age

Exposed via /metrics endpoint (added to web/api/health.py).
"""

from __future__ import annotations

import threading
from collections import defaultdict
from typing import Any


class Metrics:
    """Thread-safe in-process metrics collector."""

    def __init__(self) -> None:
        self._lock = threading.Lock()
        self._counters: dict[tuple[str, tuple[tuple[str, str], ...]], int] = defaultdict(int)
        self._gauges: dict[tuple[str, tuple[tuple[str, str], ...]], float] = {}
        self._histograms: dict[tuple[str, tuple[tuple[str, str], ...]], list[float]] = defaultdict(list)
        self._started_at: float = 0.0
        self._labels_seen: dict[str, set[tuple[tuple[str, str], ...]]] = defaultdict(set)

    # ---- counters ----

    def inc(
        self,
        name: str,
        value: int = 1,
        *,
        labels: dict[str, str] | None = None,
    ) -> None:
        with self._lock:
            label_key = tuple(sorted((labels or {}).items()))
            self._counters[(name, label_key)] += value
            self._labels_seen[name].add(label_key)

    # ---- gauges ----

    def gauge(self, name: str, value: float, *, labels: dict[str, str] | None = None) -> None:
        with self._lock:
            label_key = tuple(sorted((labels or {}).items()))
            self._gauges[(name, label_key)] = value

    # ---- histograms (simple buckets) ----

    def observe(self, name: str, value: float, *, labels: dict[str, str] | None = None) -> None:
        with self._lock:
            label_key = tuple(sorted((labels or {}).items()))
            self._histograms[(name, label_key)].append(value)
            self._labels_seen[name].add(label_key)

    # ---- explicit helpers for our domain ----

    def record_book_action(self, action: str, run_id: str) -> None:
        self.inc("bookaudit_books_total", labels={"action": action, "run_id": run_id})

    def record_llm_call(
        self,
        *,
        provider: str,
        model: str,
        duration_ms: int,
        tokens_in: int = 0,
        tokens_out: int = 0,
    ) -> None:
        labels = {"provider": provider, "model": model}
        self.inc("bookaudit_llm_calls_total", labels=labels)
        self.inc("bookaudit_llm_tokens_in_total", value=tokens_in, labels=labels)
        self.inc("bookaudit_llm_tokens_out_total", value=tokens_out, labels=labels)
        self.observe("bookaudit_llm_duration_ms", float(duration_ms), labels=labels)

    def record_ocr_pages(self, provider: str, count: int) -> None:
        self.inc("bookaudit_ocr_pages_total", value=count, labels={"provider": provider})

    def record_restore_point(self, run_id: str, age_seconds: float = 0.0) -> None:
        self.inc("bookaudit_restore_points_total", labels={"run_id": run_id})
        self.gauge(
            "bookaudit_restore_point_age_seconds", age_seconds, labels={"run_id": run_id}
        )

    def set_run_progress(self, run_id: str, total: int, completed: int) -> None:
        self.gauge("bookaudit_run_total_books", float(total), labels={"run_id": run_id})
        self.gauge("bookaudit_run_completed_books", float(completed), labels={"run_id": run_id})

    # ---- export ----

    def render(self) -> str:
        """Render metrics in Prometheus text exposition format."""
        lines: list[str] = []

        with self._lock:
            # Counters
            for (name, labels), value in sorted(self._counters.items()):
                label_str = _render_labels(dict(labels))
                lines.append(f"{name}{label_str} {value}")

            # Gauges
            seen_gauges: set[str] = set()
            for key, raw_value in sorted(self._gauges.items()):
                gauge_name = key[0]
                gauge_labels = key[1]
                gauge_value: float = raw_value
                label_str = _render_labels(dict(gauge_labels))
                metric_name_with_type = f"{gauge_name}{label_str}"
                if gauge_name not in seen_gauges:
                    lines.append(f"# TYPE {gauge_name} gauge")
                    seen_gauges.add(gauge_name)
                lines.append(f"{metric_name_with_type} {gauge_value}")

            # Histograms (basic: count + sum + average)
            seen_hist: set[str] = set()
            for (name, labels), values in sorted(self._histograms.items()):
                if name not in seen_hist:
                    lines.append(f"# TYPE {name} summary")
                    seen_hist.add(name)
                label_str = _render_labels(dict(labels))
                if values:
                    lines.append(f"{name}_count{label_str} {len(values)}")
                    lines.append(f"{name}_sum{label_str} {sum(values)}")
                    lines.append(f"{name}_avg{label_str} {sum(values) / len(values)}")

        return "\n".join(lines) + "\n"


def _render_labels(labels: dict[str, str]) -> str:
    if not labels:
        return ""
    parts = [f'{k}="{_escape(v)}"' for k, v in sorted(labels.items())]
    return "{" + ",".join(parts) + "}"


def _escape(s: str) -> str:
    return s.replace("\\", "\\\\").replace('"', '\\"').replace("\n", "\\n")


# Singleton for the running process
_metrics: Metrics | None = None


def get_metrics() -> Metrics:
    global _metrics
    if _metrics is None:
        _metrics = Metrics()
    return _metrics


def reset_metrics() -> None:
    """For tests only."""
    global _metrics
    _metrics = Metrics()


__all__ = ["Metrics", "get_metrics", "reset_metrics"]
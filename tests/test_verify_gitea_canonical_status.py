"""Tests for the fail-closed owner-attested Gitea release evidence parser."""

from __future__ import annotations

import importlib.util
import io
import json
from pathlib import Path
from typing import Any

import pytest

SCRIPT = Path(__file__).resolve().parents[1] / "scripts" / "verify_gitea_canonical_status.py"
SPEC = importlib.util.spec_from_file_location("verify_gitea_canonical_status", SCRIPT)
assert SPEC is not None
assert SPEC.loader is not None
status_gate = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(status_gate)

SHA = "a" * 40
TARGET = "http://192.168.0.122:3010/felix/calibre-bookwarden/actions/runs/104"


def _status(**status_overrides: Any) -> dict[str, Any]:
    status = {
        "id": 104,
        "context": "bookwarden/gitea-canonical",
        "state": "success",
        "updated_at": "2026-09-16T12:00:00Z",
        "creator": {"login": "felixapel"},
        "target_url": TARGET,
    }
    status.update(status_overrides)
    return status


def _combined() -> dict[str, Any]:
    return {"sha": SHA, "statuses": [{"context": "bookwarden/gitea-canonical", "state": "success"}]}


def test_accepts_actual_combined_summary_without_creator_and_full_status_record(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    responses = {
        f"https://api.github.com/repos/felixapel/calibre-bookwarden/commits/{SHA}/status": _combined(),
        f"https://api.github.com/repos/felixapel/calibre-bookwarden/commits/{SHA}/statuses?per_page=100": [_status()],
    }
    requests: list[str] = []

    def fake_urlopen(request: Any, *, timeout: int) -> io.StringIO:
        assert timeout == 20
        requests.append(request.full_url)
        return io.StringIO(json.dumps(responses[request.full_url]))

    monkeypatch.setattr(status_gate, "urlopen", fake_urlopen)

    combined = status_gate.fetch_combined_status("felixapel/calibre-bookwarden", SHA, "token")
    statuses = status_gate.fetch_statuses("felixapel/calibre-bookwarden", SHA, "token")
    status_gate.validate_combined_status(combined, sha=SHA)
    accepted = status_gate.validate_statuses(statuses, creator="felixapel")

    assert requests == list(responses)
    assert accepted["target_url"] == TARGET


@pytest.mark.parametrize(
    ("payload", "message"),
    [
        (_status(state="failure"), "not successful"),
        (_status(target_url="http://192.168.0.122:3010/untrusted/run/104"), "anchored"),
        (None, "missing required"),
    ],
)
def test_rejects_wrong_or_missing_mocked_canonical_status_evidence(
    payload: dict[str, Any] | None, message: str
) -> None:
    statuses = [] if payload is None else [payload]
    with pytest.raises(status_gate.StatusValidationError, match=message):
        status_gate.validate_statuses(statuses, creator="felixapel")


def test_rejects_status_created_by_anyone_except_the_repository_owner() -> None:
    with pytest.raises(status_gate.StatusValidationError, match="repository owner"):
        status_gate.validate_statuses([_status(creator={"login": "other"})], creator="felixapel")


def test_rejects_a_combined_summary_for_a_different_sha() -> None:
    with pytest.raises(status_gate.StatusValidationError, match="SHA"):
        status_gate.validate_combined_status({"sha": "b" * 40}, sha=SHA)


def test_rejects_a_non_list_full_status_response(monkeypatch: pytest.MonkeyPatch) -> None:
    monkeypatch.setattr(status_gate, "_fetch_json", lambda *_args, **_kwargs: {"statuses": []})

    with pytest.raises(status_gate.StatusValidationError, match="not a list"):
        status_gate.fetch_statuses("felixapel/calibre-bookwarden", SHA, "token")


def test_fails_closed_when_the_full_status_endpoint_cannot_be_retrieved(monkeypatch: pytest.MonkeyPatch) -> None:
    def fail_urlopen(*args: Any, **kwargs: Any) -> None:
        raise OSError("offline")

    monkeypatch.setattr(status_gate, "urlopen", fail_urlopen)

    with pytest.raises(status_gate.StatusValidationError, match="full status list"):
        status_gate.fetch_statuses("felixapel/calibre-bookwarden", SHA, "token")

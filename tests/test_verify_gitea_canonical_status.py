"""Tests for the fail-closed owner-attested Gitea release evidence parser."""

from __future__ import annotations

import importlib.util
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


def _payload(**status_overrides: Any) -> dict[str, Any]:
    status = {
        "id": 104,
        "context": "bookwarden/gitea-canonical",
        "state": "success",
        "updated_at": "2026-09-16T12:00:00Z",
        "creator": {"login": "felixapel"},
        "target_url": TARGET,
    }
    status.update(status_overrides)
    return {"sha": SHA, "statuses": [status]}


def test_accepts_the_latest_owner_attested_canonical_run_for_the_exact_sha() -> None:
    payload = _payload()

    accepted = status_gate.validate_status_payload(payload, sha=SHA, creator="felixapel")

    assert accepted["target_url"] == TARGET


@pytest.mark.parametrize(
    ("payload", "message"),
    [
        ({"sha": "b" * 40, "statuses": []}, "SHA"),
        (_payload(state="failure"), "not successful"),
        (_payload(target_url="http://192.168.0.122:3010/untrusted/run/104"), "anchored"),
        ({"sha": SHA, "statuses": []}, "missing required"),
    ],
)
def test_rejects_wrong_or_missing_mocked_canonical_status_evidence(payload: dict[str, Any], message: str) -> None:
    with pytest.raises(status_gate.StatusValidationError, match=message):
        status_gate.validate_status_payload(payload, sha=SHA, creator="felixapel")


def test_rejects_status_created_by_anyone_except_the_repository_owner() -> None:
    with pytest.raises(status_gate.StatusValidationError, match="repository owner"):
        status_gate.validate_status_payload(_payload(creator={"login": "other"}), sha=SHA, creator="felixapel")

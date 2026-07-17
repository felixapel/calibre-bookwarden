#!/usr/bin/env python3
"""Enforce and attest the remote auditor's calibredb operation allowlist."""

from __future__ import annotations

import json
import os
import sys
from pathlib import Path

REAL_CALIBREDB = Path("/opt/calibre/calibredb")
ALLOWED_OPERATIONS = frozenset({"--version", "list", "export"})


def allowed_operation(arguments: list[str]) -> bool:
    return bool(arguments) and arguments[0] in ALLOWED_OPERATIONS


def _record(operation: str) -> None:
    raw_path = os.environ.get("BOOKAUDIT_CALIBRE_COMMAND_LOG")
    if not raw_path:
        return
    path = Path(raw_path)
    if not path.is_absolute() or path.parent != Path("/state"):
        raise RuntimeError("calibredb audit log must be an exact file beneath /state")
    descriptor = os.open(path, os.O_WRONLY | os.O_APPEND | os.O_CREAT | os.O_NOFOLLOW, 0o600)
    try:
        os.write(descriptor, (json.dumps({"operation": operation}, sort_keys=True) + "\n").encode())
        os.fsync(descriptor)
    finally:
        os.close(descriptor)


def main(arguments: list[str] | None = None) -> int:
    argv = list(sys.argv[1:] if arguments is None else arguments)
    enforce = os.environ.get("BOOKAUDIT_CALIBRE_READ_ONLY_COMMANDS") == "1"
    if enforce and not allowed_operation(argv):
        print("calibredb operation rejected by the read-only lab boundary", file=sys.stderr)
        return 64
    if enforce:
        _record(argv[0])
    os.execv(REAL_CALIBREDB, [str(REAL_CALIBREDB), *argv])
    return 127


if __name__ == "__main__":
    raise SystemExit(main())

#!/usr/bin/env python3
"""Create the verified paired-backup manifest required by retention."""

from __future__ import annotations

import argparse
import hashlib
import json
import os
from datetime import UTC, datetime
from pathlib import Path


def sha256(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as stream:
        for chunk in iter(lambda: stream.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("database_dump", type=Path)
    parser.add_argument("artifacts_archive", type=Path)
    parser.add_argument("output", type=Path)
    args = parser.parse_args()
    output = args.output.resolve()
    root = output.parent
    database_dump = args.database_dump.resolve()
    artifacts_archive = args.artifacts_archive.resolve()
    for path in (database_dump, artifacts_archive):
        if not path.is_file() or path.is_symlink() or path.parent != root:
            parser.error("backup inputs must be regular files beside the output manifest")
    payload = {
        "created_at": datetime.now(UTC).isoformat(),
        "database_dump": database_dump.name,
        "database_sha256": sha256(database_dump),
        "artifacts_archive": artifacts_archive.name,
        "artifacts_sha256": sha256(artifacts_archive),
    }
    temporary = output.with_name(f".{output.name}.tmp-{os.getpid()}")
    temporary.write_text(json.dumps(payload, indent=2) + "\n")
    temporary.chmod(0o600)
    os.replace(temporary, output)
    print(output)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())

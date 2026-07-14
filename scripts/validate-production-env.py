#!/usr/bin/env python3
"""Fail-fast semantic validation for production deployment inputs."""

from __future__ import annotations

import argparse
import os
import re
import stat
from pathlib import Path
from urllib.parse import unquote, urlparse

REQUIRED_ROLES = {
    "BOOKAUDIT_APP_POSTGRES_DSN": ("bookaudit_app", "POSTGRES_APP_PASSWORD"),
    "BOOKAUDIT_WRITER_POSTGRES_DSN": ("bookaudit_writer", "POSTGRES_WRITER_PASSWORD"),
    "BOOKAUDIT_MIGRATOR_POSTGRES_DSN": ("bookaudit_migrator", "POSTGRES_MIGRATOR_PASSWORD"),
}
SHA256_IMAGE_PATTERN = re.compile(r"@sha256:([0-9a-f]{64})$")
SHA256_DIGEST_PATTERN = re.compile(r"sha256:([0-9a-f]{64})$")


def _enabled(values: dict[str, str], key: str) -> bool:
    return values.get(key, "").strip().lower() in {"1", "true", "yes", "on"}


def load_env(path: Path) -> dict[str, str]:
    values: dict[str, str] = {}
    for raw_line in path.read_text().splitlines():
        line = raw_line.strip()
        if not line or line.startswith("#") or "=" not in line:
            continue
        key, value = line.split("=", 1)
        values[key.strip()] = value.strip().strip('"').strip("'")
    return values


def validate(path: Path, *, allow_local_image: bool = False) -> list[str]:
    errors: list[str] = []
    values = load_env(path)
    if stat.S_IMODE(path.stat().st_mode) & 0o077:
        errors.append(".env must not be readable or writable by group/other (chmod 600)")
    if any("replace-" in value or "/absolute/path" in value for value in values.values()):
        errors.append(".env still contains production placeholders")

    library = Path(values.get("BOOKAUDIT_LIBRARY_HOST_PATH", ""))
    if not library.is_absolute() or not library.is_dir():
        errors.append("BOOKAUDIT_LIBRARY_HOST_PATH must be an existing absolute directory")
    elif not os.access(library, os.R_OK | os.W_OK):
        errors.append("the library must be readable and writable by the deployment user")

    backup_value = values.get("BOOKAUDIT_BACKUP_HOST_PATH", "./backups")
    backup_path = Path(backup_value)
    if not backup_path.is_absolute():
        backup_path = path.resolve().parent / backup_path
    if not backup_path.is_dir() or not os.access(backup_path, os.R_OK):
        errors.append("BOOKAUDIT_BACKUP_HOST_PATH must be an existing readable directory")

    secrets = [
        values.get("POSTGRES_PASSWORD", ""),
        values.get("POSTGRES_APP_PASSWORD", ""),
        values.get("POSTGRES_WRITER_PASSWORD", ""),
        values.get("POSTGRES_MIGRATOR_PASSWORD", ""),
    ]
    if any(len(secret) < 24 for secret in secrets) or len(set(secrets)) != len(secrets):
        errors.append("PostgreSQL passwords must be distinct and at least 24 characters")

    api_key = values.get("BOOKAUDIT_API_KEY", "")
    if len(api_key) < 32 or len(set(api_key)) < 8:
        errors.append("BOOKAUDIT_API_KEY must be strong (32+ characters and 8+ distinct characters)")

    for dsn_key, (expected_user, password_key) in REQUIRED_ROLES.items():
        parsed = urlparse(values.get(dsn_key, ""))
        if (
            parsed.scheme not in {"postgresql", "postgresql+psycopg"}
            or parsed.username != expected_user
            or unquote(parsed.password or "") != values.get(password_key, "")
            or parsed.hostname != "postgres"
            or parsed.path != "/bookaudit"
        ):
            errors.append(f"{dsn_key} must match {expected_user}, {password_key}, postgres, and /bookaudit")

    trusted_hosts = [host.strip() for host in values.get("BOOKAUDIT_TRUSTED_HOSTS", "").split(",")]
    if not all(trusted_hosts) or any(host == "*" for host in trusted_hosts):
        errors.append("BOOKAUDIT_TRUSTED_HOSTS must contain explicit hosts and no wildcard")

    image = values.get("BOOKAUDIT_IMAGE", "")
    image_digest_match = SHA256_IMAGE_PATTERN.search(image)
    local_allowed = allow_local_image or values.get("BOOKAUDIT_ALLOW_LOCAL_IMAGE", "").lower() == "true"
    if not local_allowed and image_digest_match is None:
        errors.append("BOOKAUDIT_IMAGE must use an immutable sha256 digest")

    pilot_prefix = "BOOKAUDIT_MANIFESTATION_V2__SUPERVISED_PILOT__"
    if _enabled(values, f"{pilot_prefix}ENABLED"):
        pilot_id = values.get(f"{pilot_prefix}PILOT_ID", "").strip()
        if not pilot_id:
            errors.append("supervised V2 pilot ID must be non-empty")

        configured_digest = values.get(f"{pilot_prefix}RELEASE_DIGEST", "").strip()
        configured_digest_match = SHA256_DIGEST_PATTERN.fullmatch(configured_digest)
        if (
            image_digest_match is None
            or configured_digest_match is None
            or configured_digest_match.group(1) != image_digest_match.group(1)
        ):
            errors.append("supervised V2 pilot release digest must match BOOKAUDIT_IMAGE")

        maximum = values.get(f"{pilot_prefix}MAX_OPERATIONS", "").strip()
        try:
            maximum_value = int(maximum)
        except ValueError:
            maximum_value = 0
        if maximum_value not in range(1, 6):
            errors.append("supervised V2 pilot max operations must be between 1 and 5")

        if not _enabled(values, "BOOKAUDIT_REQUIRE_WRITER_READY"):
            errors.append("supervised V2 pilot requires writer readiness")
        if _enabled(values, "BOOKAUDIT_MANIFESTATION_V2__AUTO_APPLY__ENABLED"):
            errors.append("supervised V2 pilot requires auto-apply to remain disabled")
    return errors


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("path", type=Path)
    parser.add_argument("--allow-local-image", action="store_true")
    args = parser.parse_args()
    errors = validate(args.path, allow_local_image=args.allow_local_image)
    if errors:
        for error in errors:
            print(f"ERROR: {error}")
        return 1
    print("Production environment validation passed.")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())

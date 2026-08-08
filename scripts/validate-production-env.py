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
    "BOOKAUDIT_VERIFIER_POSTGRES_DSN": ("bookaudit_verifier", "POSTGRES_VERIFIER_PASSWORD"),
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

    runtime_ids = (values.get("UID", ""), values.get("GID", ""))
    if any(not value.isdigit() or int(value) <= 0 for value in runtime_ids):
        errors.append("UID and GID must be explicit positive non-root integers")

    library = Path(values.get("BOOKAUDIT_LIBRARY_HOST_PATH", ""))
    if not library.is_absolute() or not library.is_dir():
        errors.append("BOOKAUDIT_LIBRARY_HOST_PATH must be an existing absolute directory")
    elif not os.access(library, os.R_OK):
        errors.append("the library must be readable by the deployment user")

    backup_value = values.get("BOOKAUDIT_BACKUP_HOST_PATH", "./backups")
    backup_path = Path(backup_value)
    if not backup_path.is_absolute():
        backup_path = path.resolve().parent / backup_path
    if not backup_path.is_dir() or not os.access(backup_path, os.R_OK):
        errors.append("BOOKAUDIT_BACKUP_HOST_PATH must be an existing readable directory")

    secrets = [
        values.get("POSTGRES_PASSWORD", ""),
        values.get("POSTGRES_APP_PASSWORD", ""),
        values.get("POSTGRES_VERIFIER_PASSWORD", ""),
        values.get("POSTGRES_WRITER_PASSWORD", ""),
        values.get("POSTGRES_MIGRATOR_PASSWORD", ""),
    ]
    if any(len(secret) < 24 for secret in secrets) or len(set(secrets)) != len(secrets):
        errors.append("PostgreSQL passwords must be distinct and at least 24 characters")

    api_key = values.get("BOOKAUDIT_API_KEY", "")
    if len(api_key) < 32 or len(set(api_key)) < 8:
        errors.append("BOOKAUDIT_API_KEY must be strong (32+ characters and 8+ distinct characters)")
    elif api_key in secrets:
        errors.append("BOOKAUDIT_API_KEY must be distinct from every PostgreSQL password")

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
    elif not {"localhost", "127.0.0.1"}.issubset(set(trusted_hosts)):
        errors.append("BOOKAUDIT_TRUSTED_HOSTS must include localhost and 127.0.0.1 for private health checks")

    image = values.get("BOOKAUDIT_IMAGE", "")
    image_digest_match = SHA256_IMAGE_PATTERN.search(image)
    local_allowed = allow_local_image or values.get("BOOKAUDIT_ALLOW_LOCAL_IMAGE", "").lower() == "true"
    if not local_allowed and image_digest_match is None:
        errors.append("BOOKAUDIT_IMAGE must use an immutable sha256 digest")
    release_digest = values.get("BOOKAUDIT_RELEASE_DIGEST", "")
    release_digest_match = SHA256_DIGEST_PATTERN.fullmatch(release_digest)
    if release_digest_match is None or (
        not local_allowed
        and (image_digest_match is None or release_digest_match.group(1) != image_digest_match.group(1))
    ):
        errors.append("BOOKAUDIT_RELEASE_DIGEST must match BOOKAUDIT_IMAGE")

    if _enabled(values, "BOOKAUDIT_MANIFESTATION_V2__AUTO_APPLY__ENABLED"):
        errors.append("Certificate A requires auto-apply to remain disabled")
    if _enabled(values, "BOOKAUDIT_MANIFESTATION_V2__SUPERVISED_PILOT__ENABLED"):
        errors.append("Certificate A does not permit the supervised writer pilot")
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

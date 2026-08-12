#!/usr/bin/env python3
"""Fail-fast semantic validation for production deployment inputs."""

from __future__ import annotations

import argparse
import ipaddress
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
GIT_COMMIT_PATTERN = re.compile(r"[0-9a-f]{40}$")
TAILSCALE_DOMAIN_PATTERN = re.compile(r"(?=.{1,253}$)(?:[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?\.)+ts\.net$")
BASIC_AUTH_USER_PATTERN = re.compile(r"[A-Za-z0-9._-]{1,64}$")
BCRYPT_PATTERN = re.compile(r"\$2[aby]\$(?:0[4-9]|[12][0-9]|3[01])\$[./A-Za-z0-9]{53}$")


def validate_runtime_caller(values: dict[str, str], current_uid: int) -> str | None:
    """Require preflight to prove Tailscale access as Caddy's exact UID."""
    configured = values.get("UID", "")
    if not configured.isdigit() or int(configured) != current_uid:
        return (
            f"Run preflight as the configured runtime UID ({configured or 'unset'}) "
            "so Tailscale certificate access is proven for Caddy."
        )
    return None


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

    if values.get("COMPOSE_PROJECT_NAME") != "bookaudit-certificate-a":
        errors.append(
            "COMPOSE_PROJECT_NAME must be bookaudit-certificate-a to isolate Certificate A from legacy stacks"
        )

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
    elif not {"localhost", "127.0.0.1", "app"}.issubset(set(trusted_hosts)):
        errors.append("BOOKAUDIT_TRUSTED_HOSTS must include localhost, 127.0.0.1, and app for private health checks")

    domain = values.get("BOOKAUDIT_DOMAIN", "").lower()
    if TAILSCALE_DOMAIN_PATTERN.fullmatch(domain) is None:
        errors.append("BOOKAUDIT_DOMAIN must be an exact Tailscale HTTPS name")
    elif domain not in trusted_hosts:
        errors.append("BOOKAUDIT_TRUSTED_HOSTS must include BOOKAUDIT_DOMAIN")
    try:
        edge_ip = ipaddress.ip_address(values.get("BOOKAUDIT_EDGE_BIND_IP", ""))
    except ValueError:
        edge_ip = None
    if edge_ip is None or edge_ip.version != 4 or edge_ip not in ipaddress.ip_network("100.64.0.0/10"):
        errors.append("BOOKAUDIT_EDGE_BIND_IP must be an IPv4 address in 100.64.0.0/10")
    if BASIC_AUTH_USER_PATTERN.fullmatch(values.get("BOOKAUDIT_BASIC_AUTH_USER", "")) is None:
        errors.append("BOOKAUDIT_BASIC_AUTH_USER contains unsupported characters")
    if BCRYPT_PATTERN.fullmatch(values.get("BOOKAUDIT_BASIC_AUTH_HASH", "")) is None:
        errors.append("BOOKAUDIT_BASIC_AUTH_HASH must be a Caddy-supported bcrypt hash")

    image = values.get("BOOKAUDIT_IMAGE", "")
    image_digest_match = SHA256_IMAGE_PATTERN.search(image)
    local_allowed = allow_local_image or values.get("BOOKAUDIT_ALLOW_LOCAL_IMAGE", "").lower() == "true"
    if not local_allowed and image_digest_match is None:
        errors.append("BOOKAUDIT_IMAGE must use an immutable sha256 digest")
    edge_image = values.get("BOOKAUDIT_EDGE_IMAGE", "")
    if not local_allowed and SHA256_IMAGE_PATTERN.search(edge_image) is None:
        errors.append("BOOKAUDIT_EDGE_IMAGE must use an immutable sha256 digest")
    release_digest = values.get("BOOKAUDIT_RELEASE_DIGEST", "")
    release_digest_match = SHA256_DIGEST_PATTERN.fullmatch(release_digest)
    if release_digest_match is None or (
        not local_allowed
        and (image_digest_match is None or release_digest_match.group(1) != image_digest_match.group(1))
    ):
        errors.append("BOOKAUDIT_RELEASE_DIGEST must match BOOKAUDIT_IMAGE")

    if GIT_COMMIT_PATTERN.fullmatch(values.get("BOOKAUDIT_SOURCE_REVISION", "")) is None:
        errors.append("BOOKAUDIT_SOURCE_REVISION must be an exact 40-character Git commit")

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

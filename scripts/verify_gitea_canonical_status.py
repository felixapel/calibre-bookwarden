#!/usr/bin/env python3
"""Fail closed unless GitHub records the owner-attested canonical Gitea gate."""

from __future__ import annotations

import argparse
import json
import os
import re
import sys
from typing import Any
from urllib.error import HTTPError, URLError
from urllib.request import Request, urlopen

CONTEXT = "bookwarden/gitea-canonical"
CANONICAL_TARGET_URL = re.compile(r"http://192\.168\.0\.122:3010/felix/calibre-bookwarden/actions/runs/[0-9]+\Z")


class StatusValidationError(ValueError):
    """The GitHub status record is not acceptable release evidence."""


def validate_combined_status(payload: dict[str, Any], *, sha: str) -> None:
    """Bind release evidence to the exact revision reported by GitHub."""
    if payload.get("sha") != sha:
        raise StatusValidationError("combined status SHA does not match the release revision")


def validate_statuses(statuses: list[Any], *, creator: str) -> dict[str, Any]:
    """Return the latest full status record for the canonical context."""
    candidates = [status for status in statuses if isinstance(status, dict) and status.get("context") == CONTEXT]
    if not candidates:
        raise StatusValidationError(f"missing required GitHub status context {CONTEXT!r}")

    latest = max(candidates, key=lambda status: (str(status.get("updated_at", "")), int(status.get("id", 0))))
    if latest.get("state") != "success":
        raise StatusValidationError("latest canonical Gitea status is not successful")
    status_creator = latest.get("creator")
    if not isinstance(status_creator, dict) or status_creator.get("login") != creator:
        raise StatusValidationError("canonical Gitea status was not created by the repository owner")
    target_url = latest.get("target_url")
    if not isinstance(target_url, str) or not CANONICAL_TARGET_URL.fullmatch(target_url):
        raise StatusValidationError("canonical Gitea status target URL is not an anchored canonical run URL")
    return latest


def _fetch_json(url: str, token: str, *, error_label: str) -> Any:
    request = Request(
        url,
        headers={
            "Accept": "application/vnd.github+json",
            "Authorization": f"Bearer {token}",
            "X-GitHub-Api-Version": "2022-11-28",
        },
    )
    try:
        with urlopen(request, timeout=20) as response:  # noqa: S310 -- fixed GitHub API origin
            return json.load(response)
    except (HTTPError, URLError, OSError, json.JSONDecodeError) as error:
        raise StatusValidationError(f"could not retrieve the GitHub {error_label}") from error


def fetch_combined_status(repository: str, sha: str, token: str) -> dict[str, Any]:
    payload = _fetch_json(
        f"https://api.github.com/repos/{repository}/commits/{sha}/status", token, error_label="combined status"
    )
    if not isinstance(payload, dict):
        raise StatusValidationError("GitHub combined status response is not an object")
    return payload


def fetch_statuses(repository: str, sha: str, token: str) -> list[Any]:
    payload = _fetch_json(
        f"https://api.github.com/repos/{repository}/commits/{sha}/statuses?per_page=100",
        token,
        error_label="full status list",
    )
    if not isinstance(payload, list):
        raise StatusValidationError("GitHub full status list response is not a list")
    return payload


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--repository", required=True)
    parser.add_argument("--sha", required=True)
    parser.add_argument("--creator", required=True)
    args = parser.parse_args(argv)

    token = os.environ.get("GITHUB_TOKEN")
    if not token:
        print("GITHUB_TOKEN is required to read release status evidence", file=sys.stderr)
        return 2
    try:
        validate_combined_status(fetch_combined_status(args.repository, args.sha, token), sha=args.sha)
        validate_statuses(fetch_statuses(args.repository, args.sha, token), creator=args.creator)
    except StatusValidationError as error:
        print(f"Canonical Gitea status gate failed: {error}", file=sys.stderr)
        return 1
    print("Owner-attested canonical Gitea status is current and successful.")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())

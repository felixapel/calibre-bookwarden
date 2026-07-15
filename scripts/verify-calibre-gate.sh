#!/usr/bin/env bash
set -euo pipefail

cd "$(dirname "$0")/.."

uv lock --check
uv sync --frozen --extra dev
uv run ruff check .
uv run ruff format --check .
uv run mypy src
uv run pytest \
  -m 'not benchmark and not ocr_live and not network' \
  --ignore=tests/test_retention_postgres_valkey.py \
  --ignore=tests/test_v2_supervised_pilot_integration.py
uv run bookaudit --help >/dev/null

echo "Local deterministic gate passed; required live-service gates run in Gitea."

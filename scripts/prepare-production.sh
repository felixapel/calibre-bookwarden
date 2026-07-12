#!/usr/bin/env bash
set -euo pipefail

cd "$(dirname "${BASH_SOURCE[0]}")/.."

if [[ ! -f .env ]]; then
  echo "Missing .env; copy .env.example and replace every placeholder." >&2
  exit 1
fi

python scripts/validate-production-env.py .env

runtime_uid="$(sed -n 's/^UID=//p' .env | tail -n 1)"
runtime_gid="$(sed -n 's/^GID=//p' .env | tail -n 1)"
runtime_uid="${runtime_uid:-$(id -u)}"
runtime_gid="${runtime_gid:-$(id -g)}"
if [[ ! "$runtime_uid" =~ ^[0-9]+$ || ! "$runtime_gid" =~ ^[0-9]+$ ]]; then
  echo "UID and GID in .env must be numeric." >&2
  exit 1
fi
install -d -m 0750 -o "$runtime_uid" -g "$runtime_gid" \
  .state .artifacts .writer-artifacts user_library

docker compose --profile maintenance config -q
echo "Production bind mounts and Compose configuration are ready."

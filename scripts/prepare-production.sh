#!/usr/bin/env bash
set -euo pipefail

cd "$(dirname "${BASH_SOURCE[0]}")/.."

if [[ ! -f .env ]]; then
  echo "Missing .env; copy .env.example and replace every placeholder." >&2
  exit 1
fi

# Certificate A uses this directory for operator-owned PostgreSQL backups. It
# intentionally does not prepare any writer or legacy artifact directories.
install -d -m 0750 backups
python scripts/validate-production-env.py .env

runtime_uid="$(sed -n 's/^UID=//p' .env | tail -n 1)"
runtime_gid="$(sed -n 's/^GID=//p' .env | tail -n 1)"
runtime_uid="${runtime_uid:-$(id -u)}"
runtime_gid="${runtime_gid:-$(id -g)}"
if [[ ! "$runtime_uid" =~ ^[0-9]+$ || ! "$runtime_gid" =~ ^[0-9]+$ \
      || "$runtime_uid" -eq 0 || "$runtime_gid" -eq 0 ]]; then
  echo "UID and GID in .env must be positive non-root integers." >&2
  exit 1
fi
install -d -m 0750 -o "$runtime_uid" -g "$runtime_gid" \
  backups

compose=("./scripts/certificate-a-compose.sh")
"${compose[@]}" --profile maintenance config -q
actual_services="$("${compose[@]}" config --services | LC_ALL=C sort | tr '\n' ' ')"
if [[ "$actual_services" != "app postgres valkey verifier " ]]; then
  echo "Default Compose graph is not the exact Certificate A topology: $actual_services" >&2
  exit 1
fi
"./scripts/check-certificate-a-compose-project.sh"
echo "Certificate A bind mounts and exact default Compose graph are ready."

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

mapfile -t release_identity < <(
  python - <<'PY'
import runpy
from pathlib import Path

values = runpy.run_path("scripts/validate-production-env.py")["load_env"](Path(".env"))
print(values["BOOKAUDIT_IMAGE"])
print(values["BOOKAUDIT_SOURCE_REVISION"])
PY
)
release_image="${release_identity[0]}"
source_revision="${release_identity[1]}"
reviewed_revision="$(git rev-parse HEAD)"
if [[ "$source_revision" != "$reviewed_revision" ]]; then
  echo "BOOKAUDIT_SOURCE_REVISION does not match the reviewed checkout." >&2
  exit 1
fi
if ! image_revision="$(docker image inspect "$release_image" \
  --format '{{index .Config.Labels "org.opencontainers.image.revision"}}')"; then
  echo "The digest-pinned Certificate A image is not available for provenance verification." >&2
  exit 1
fi
if [[ "$image_revision" != "$source_revision" ]]; then
  echo "Certificate A image provenance does not match BOOKAUDIT_SOURCE_REVISION." >&2
  exit 1
fi

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

for monitored_path in \
  docker-compose.yml \
  ops/monitoring/prometheus.yml \
  ops/monitoring/alerts.yml; do
  if ! git diff --quiet HEAD -- "$monitored_path"; then
    echo "Production monitoring input differs from the reviewed commit: $monitored_path" >&2
    exit 1
  fi
  sha256sum "$monitored_path"
done

compose=("./scripts/certificate-a-compose.sh")
"${compose[@]}" --profile maintenance config -q
actual_services="$("${compose[@]}" config --services | LC_ALL=C sort | tr '\n' ' ')"
if [[ "$actual_services" != "app postgres valkey verifier " ]]; then
  echo "Default Compose graph is not the exact Certificate A topology: $actual_services" >&2
  exit 1
fi
monitoring_services="$("${compose[@]}" --profile monitoring config --services | LC_ALL=C sort | tr '\n' ' ')"
if [[ "$monitoring_services" != "app postgres prometheus valkey verifier " ]]; then
  echo "Monitoring Compose graph is not the exact Certificate A topology: $monitoring_services" >&2
  exit 1
fi
"./scripts/check-certificate-a-compose-project.sh"

# Materialize monitoring state only after every fail-closed validation passes.
# Prometheus runs as this same UID/GID, so the mode-0600 API key stays readable
# without weakening host permissions or running the collector as root.
monitoring_root=".monitoring"
install -d -m 0700 -o "$runtime_uid" -g "$runtime_gid" \
  "$monitoring_root"
install -d -m 0750 -o "$runtime_uid" -g "$runtime_gid" \
  "$monitoring_root/data"
api_key="$(python -c 'import runpy; from pathlib import Path; module = runpy.run_path("scripts/validate-production-env.py"); print(module["load_env"](Path(".env"))["BOOKAUDIT_API_KEY"], end="")')"
secret_tmp="$(mktemp "$monitoring_root/.bookaudit_api_key.XXXXXX")"
trap 'rm -f "$secret_tmp"' EXIT
chmod 0600 "$secret_tmp"
printf '%s' "$api_key" > "$secret_tmp"
chown "$runtime_uid:$runtime_gid" "$secret_tmp"
mv -f "$secret_tmp" "$monitoring_root/bookaudit_api_key"
trap - EXIT
echo "Certificate A bind mounts, monitoring secret, and exact Compose graphs are ready."

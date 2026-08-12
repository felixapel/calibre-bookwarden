#!/usr/bin/env bash
set -euo pipefail

cd "$(dirname "${BASH_SOURCE[0]}")/.."

if [[ ! -f .env ]]; then
  echo "Missing .env; copy .env.example and replace every placeholder." >&2
  exit 1
fi

python scripts/validate-production-env.py .env
python - <<'PY'
import os
import runpy
from pathlib import Path

module = runpy.run_path("scripts/validate-production-env.py")
values = module["load_env"](Path(".env"))
error = module["validate_runtime_caller"](values, os.getuid())
if error:
    raise SystemExit(error)
PY

mapfile -t release_identity < <(
  python - <<'PY'
import runpy
from pathlib import Path

values = runpy.run_path("scripts/validate-production-env.py")["load_env"](Path(".env"))
print(values["BOOKAUDIT_IMAGE"])
print(values["BOOKAUDIT_SOURCE_REVISION"])
print(values["BOOKAUDIT_DOMAIN"])
print(values["BOOKAUDIT_EDGE_BIND_IP"])
PY
)
release_image="${release_identity[0]}"
source_revision="${release_identity[1]}"
edge_domain="${release_identity[2]}"
edge_bind_ip="${release_identity[3]}"
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
if ! command -v tailscale >/dev/null || [[ ! -S /var/run/tailscale/tailscaled.sock ]]; then
  echo "The reviewed Caddy edge requires the local Tailscale daemon and socket." >&2
  exit 1
fi
mapfile -t tailscale_identity < <(
  tailscale status --json | python -c '
import json, sys
state = json.load(sys.stdin)
print(state.get("Self", {}).get("DNSName", "").rstrip("."))
print(next((ip for ip in state.get("Self", {}).get("TailscaleIPs", []) if "." in ip), ""))
'
)
if [[ "${tailscale_identity[0]:-}" != "$edge_domain" || \
      "${tailscale_identity[1]:-}" != "$edge_bind_ip" ]]; then
  echo "BOOKAUDIT_DOMAIN and BOOKAUDIT_EDGE_BIND_IP must match this Tailscale node." >&2
  exit 1
fi
for monitored_path in \
  docker-compose.yml \
  deploy/caddy/Caddyfile.example \
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
edge_services="$("${compose[@]}" --profile edge config --services | LC_ALL=C sort | tr '\n' ' ')"
if [[ "$edge_services" != "app caddy postgres valkey verifier " ]]; then
  echo "Edge Compose graph is not the exact Certificate A topology: $edge_services" >&2
  exit 1
fi
"./scripts/check-certificate-a-compose-project.sh"

# This authorization probe can ask tailscaled to issue or renew the configured
# certificate. It writes only temporary files, removed immediately. All other
# repository, topology, and project-state checks above are read-only.
certificate_probe="$(mktemp -d)"
trap 'rm -rf -- "$certificate_probe"' EXIT
if ! tailscale cert \
  --cert-file "$certificate_probe/edge.crt" \
  --key-file "$certificate_probe/edge.key" \
  "$edge_domain" >/dev/null; then
  echo "Tailscale certificate access is not granted to runtime UID $runtime_uid; set TS_PERMIT_CERT_UID=$runtime_uid in /etc/default/tailscaled and restart tailscaled." >&2
  exit 1
fi
rm -rf -- "$certificate_probe"
trap - EXIT

# Materialize state only after every fail-closed validation passes.
# Prometheus runs as this same UID/GID, so the mode-0600 API key stays readable
# without weakening host permissions or running the collector as root.
monitoring_root=".monitoring"
edge_root=".edge"
install -d -m 0750 -o "$runtime_uid" -g "$runtime_gid" backups
install -d -m 0700 -o "$runtime_uid" -g "$runtime_gid" \
  "$monitoring_root"
install -d -m 0750 -o "$runtime_uid" -g "$runtime_gid" \
  "$monitoring_root/data"
install -d -m 0700 -o "$runtime_uid" -g "$runtime_gid" \
  "$edge_root"
install -d -m 0750 -o "$runtime_uid" -g "$runtime_gid" \
  "$edge_root/data" "$edge_root/config"
api_key="$(python -c 'import runpy; from pathlib import Path; module = runpy.run_path("scripts/validate-production-env.py"); print(module["load_env"](Path(".env"))["BOOKAUDIT_API_KEY"], end="")')"
secret_tmp="$(mktemp "$monitoring_root/.bookaudit_api_key.XXXXXX")"
trap 'rm -f "$secret_tmp"' EXIT
chmod 0600 "$secret_tmp"
printf '%s' "$api_key" > "$secret_tmp"
chown "$runtime_uid:$runtime_gid" "$secret_tmp"
mv -f "$secret_tmp" "$monitoring_root/bookaudit_api_key"
trap - EXIT
echo "Certificate A bind mounts, edge state, monitoring secret, and exact Compose graphs are ready."

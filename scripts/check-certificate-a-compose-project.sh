#!/usr/bin/env bash
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
project="bookaudit-certificate-a"
project_kind="primary"
require_current_service=""
restore_drill=false
while [[ "$#" -gt 0 ]]; do
  case "$1" in
    --restore-drill)
      if [[ "$restore_drill" == true ]]; then
        echo "Unsupported Certificate A project check arguments." >&2
        exit 1
      fi
      restore_drill=true
      project="bookaudit-certificate-a-restore-drill"
      project_kind="restore"
      shift
      ;;
    --require-current)
      if [[ -n "$require_current_service" || "$#" -lt 2 ]]; then
        echo "Unsupported Certificate A project check arguments." >&2
        exit 1
      fi
      require_current_service="$2"
      shift 2
      ;;
    *)
      echo "Unsupported Certificate A project check arguments." >&2
      exit 1
      ;;
  esac
done

service_is_allowed() {
  local service="$1"
  if [[ "$project_kind" == "primary" ]]; then
    case "$service" in
      app|postgres|valkey|verifier) return 0 ;;
    esac
  elif [[ "$service" == "postgres" ]]; then
    return 0
  fi
  return 1
}

expected_config_hash=""
if [[ -n "$require_current_service" ]]; then
  if ! service_is_allowed "$require_current_service"; then
    echo "Unsupported Certificate A current-service check: $require_current_service" >&2
    exit 1
  fi
  if ! expected_hash_output="$(
    docker compose \
      --project-name "$project" \
      --file "$root/docker-compose.yml" \
      --env-file "$root/.env" \
      config --hash "$require_current_service"
  )"; then
    echo "Could not resolve the reviewed Certificate A service hash." >&2
    exit 1
  fi
  read -r hash_service expected_config_hash hash_extra <<< "$expected_hash_output"
  if [[ "$hash_service" != "$require_current_service" || -z "$expected_config_hash" || \
    -n "${hash_extra:-}" ]]; then
    echo "Could not resolve the reviewed Certificate A service hash." >&2
    exit 1
  fi
fi

if ! project_containers="$(
  docker ps --all \
    --filter "label=com.docker.compose.project=$project" \
    --format '{{.ID}}|{{.Label "com.docker.compose.service"}}|{{.Label "com.docker.compose.project.config_files"}}|{{.Label "com.docker.compose.config-hash"}}|{{.Label "com.docker.compose.oneoff"}}'
)"; then
  echo "Could not inspect the Certificate A Compose project." >&2
  exit 1
fi

if [[ -z "$project_containers" ]]; then
  exit 0
fi

declare -A seen_services=()
while IFS='|' read -r container_id project_service config_files config_hash oneoff extra; do
  if [[ -z "$container_id" || -z "$project_service" || -z "$config_files" || \
    -z "$config_hash" || -z "$oneoff" || -n "$extra" ]]; then
    echo "Unexpected container in the Certificate A Compose project: $container_id" >&2
    exit 1
  fi
  if ! service_is_allowed "$project_service"; then
    echo "Unexpected service in the Certificate A Compose project: $project_service" >&2
    exit 1
  fi
  if [[ "$config_files" != "$root/docker-compose.yml" ]]; then
    echo "Unexpected Compose file provenance for Certificate A service $project_service: $config_files" >&2
    exit 1
  fi
  if [[ "$oneoff" != "False" ]]; then
    echo "Unexpected one-off Certificate A container: $container_id" >&2
    exit 1
  fi
  if [[ "$project_service" == "$require_current_service" && \
    "$config_hash" != "$expected_config_hash" ]]; then
    echo "Certificate A service $project_service does not match the reviewed environment." >&2
    exit 1
  fi
  if [[ -n "${seen_services[$project_service]:-}" ]]; then
    echo "Duplicate Certificate A service instance: $project_service" >&2
    exit 1
  fi
  seen_services["$project_service"]="$container_id"
done <<< "$project_containers"

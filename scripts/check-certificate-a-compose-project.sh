#!/usr/bin/env bash
set -euo pipefail

project="bookaudit-certificate-a"
allowed_services="app postgres valkey verifier"
if [[ "${1:-}" == "--restore-drill" && "$#" -eq 1 ]]; then
  project="bookaudit-certificate-a-restore-drill"
  allowed_services="postgres"
elif [[ "$#" -ne 0 ]]; then
  echo "Unsupported Certificate A project check arguments." >&2
  exit 1
fi

if ! project_containers="$(
  docker ps --all \
    --filter "label=com.docker.compose.project=$project" \
    --format '{{.ID}}|{{.Label "com.docker.compose.service"}}'
)"; then
  echo "Could not inspect the Certificate A Compose project." >&2
  exit 1
fi

if [[ -z "$project_containers" ]]; then
  exit 0
fi

while IFS= read -r project_container; do
  container_id="${project_container%%|*}"
  project_service="${project_container#*|}"
  if [[ "$project_container" != *"|"* || -z "$project_service" ]]; then
    echo "Unexpected container in the Certificate A Compose project: $container_id" >&2
    exit 1
  fi
  case " $allowed_services " in
    *" $project_service "*) ;;
    *)
      echo "Unexpected service in the Certificate A Compose project: $project_service" >&2
      exit 1
      ;;
  esac
done <<< "$project_containers"

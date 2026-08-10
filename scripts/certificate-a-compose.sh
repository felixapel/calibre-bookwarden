#!/usr/bin/env bash
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
project="bookaudit-certificate-a"
if [[ "${1:-}" == "--restore-drill" ]]; then
  project="bookaudit-certificate-a-restore-drill"
  shift
fi

expect_profile=false
for argument in "$@"; do
  if [[ "$expect_profile" == true ]]; then
    if [[ "$argument" != "maintenance" ]]; then
      echo "Certificate A permits only the maintenance Compose profile." >&2
      exit 1
    fi
    expect_profile=false
    continue
  fi
  case "$argument" in
    --profile) expect_profile=true ;;
    --profile=maintenance) ;;
    --profile=*)
      echo "Certificate A permits only the maintenance Compose profile." >&2
      exit 1
      ;;
    -p|-p?*|--project-name|--project-name=*|-f|-f?*|--file|--file=*|--env-file|--env-file=*|--project-directory|--project-directory=*)
      echo "Certificate A Compose identity and input files cannot be overridden." >&2
      exit 1
      ;;
    writer|retention)
      echo "Certificate B services cannot be selected through the Certificate A wrapper." >&2
      exit 1
      ;;
  esac
done
if [[ "$expect_profile" == true ]]; then
  echo "Missing value for --profile." >&2
  exit 1
fi

if [[ "$project" == "bookaudit-certificate-a" ]]; then
  "$root/scripts/check-certificate-a-compose-project.sh"
else
  "$root/scripts/check-certificate-a-compose-project.sh" --restore-drill
fi

unset COMPOSE_ENV_FILES COMPOSE_FILE COMPOSE_PROJECT_NAME
export COMPOSE_PROFILES=""
cd "$root"
exec docker compose \
  --project-name "$project" \
  --file "$root/docker-compose.yml" \
  --env-file "$root/.env" \
  "$@"

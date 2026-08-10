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
    --scale|--scale=*|scale)
      echo "Certificate A services cannot be scaled through the production wrapper." >&2
      exit 1
      ;;
  esac
done
if [[ "$expect_profile" == true ]]; then
  echo "Missing value for --profile." >&2
  exit 1
fi

# Compose gives exported shell variables precedence over --env-file. Start the
# deployment command with an allowlisted environment so every interpolation
# input comes from the reviewed .env while retaining only Docker transport and
# registry connectivity settings.
clean_environment=(env -i "PATH=$PATH" "COMPOSE_PROFILES=")
for variable_name in \
  HOME XDG_CONFIG_HOME XDG_RUNTIME_DIR \
  DOCKER_API_VERSION DOCKER_CERT_PATH DOCKER_CONFIG DOCKER_CONTEXT DOCKER_HOST \
  DOCKER_TLS DOCKER_TLS_VERIFY \
  HTTP_PROXY HTTPS_PROXY NO_PROXY http_proxy https_proxy no_proxy \
  SSL_CERT_DIR SSL_CERT_FILE SSH_AUTH_SOCK TMPDIR; do
  if [[ -v "$variable_name" ]]; then
    clean_environment+=("$variable_name=${!variable_name}")
  fi
done

guard_arguments=()
if [[ "$project" == "bookaudit-certificate-a-restore-drill" ]]; then
  guard_arguments+=(--restore-drill)
fi

# Exec operates inside an existing container. Bind that target to the exact
# service configuration resolved from the reviewed .env before crossing the
# container boundary. Reconciliation commands such as up remain able to replace
# an older reviewed deployment.
arguments=("$@")
compose_command=""
compose_command_index=-1
for index in "${!arguments[@]}"; do
  case "${arguments[$index]}" in
    attach|build|commit|config|cp|create|down|events|exec|export|images|kill|logs|ls|pause|port|ps|publish|pull|push|restart|rm|run|start|stats|stop|top|unpause|up|version|volumes|wait|watch)
      compose_command="${arguments[$index]}"
      compose_command_index="$index"
      break
      ;;
  esac
done
if [[ "$compose_command" == "exec" ]]; then
  index=$((compose_command_index + 1))
  while [[ "$index" -lt "${#arguments[@]}" ]]; do
    argument="${arguments[$index]}"
    case "$argument" in
      -e|--env|--index|-u|--user|-w|--workdir)
        index=$((index + 2))
        ;;
      -e?*|--env=*|--index=*|-u?*|--user=*|-w?*|--workdir=*|-d|--detach|--privileged|-T|--no-TTY)
        index=$((index + 1))
        ;;
      --)
        index=$((index + 1))
        if [[ "$index" -lt "${#arguments[@]}" ]]; then
          guard_arguments+=(--require-current "${arguments[$index]}")
        fi
        break
        ;;
      -*) index=$((index + 1)) ;;
      *)
        guard_arguments+=(--require-current "$argument")
        break
        ;;
    esac
  done
fi

"${clean_environment[@]}" \
  "$root/scripts/check-certificate-a-compose-project.sh" "${guard_arguments[@]}"

cd "$root"
exec "${clean_environment[@]}" docker compose \
  --project-name "$project" \
  --file "$root/docker-compose.yml" \
  --env-file "$root/.env" \
  "$@"

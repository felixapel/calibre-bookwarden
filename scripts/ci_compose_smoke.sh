#!/usr/bin/env bash
# Helpers sourced by the Gitea Compose persistence smoke. They deliberately
# derive a disposable project name instead of accepting an ambient production
# Compose project.
set -euo pipefail

ci_compose_set_project_from_run_id() {
  local run_id="${GITHUB_RUN_ID:-}"
  if [[ ! "$run_id" =~ ^[0-9]{1,20}$ ]]; then
    echo "GITHUB_RUN_ID must be a non-empty decimal CI run identifier." >&2
    return 2
  fi

  readonly CI_COMPOSE_EXPECTED_PROJECT="bookaudit-ci-${run_id}"
  export COMPOSE_PROJECT_NAME="$CI_COMPOSE_EXPECTED_PROJECT"
}

ci_compose_validate_project() {
  if [[ ! "${CI_COMPOSE_EXPECTED_PROJECT:-}" =~ ^bookaudit-ci-[0-9]{1,20}$ ||
        "${COMPOSE_PROJECT_NAME:-}" != "$CI_COMPOSE_EXPECTED_PROJECT" ]]; then
    echo "Refusing to use an unscoped Compose project for the CI smoke." >&2
    return 2
  fi
}

ci_compose_smoke_cleanup() {
  local original_status=$?
  local cleanup_status=0
  local postgres_id

  trap - EXIT
  set +e
  if ! ci_compose_validate_project; then
    echo "Refusing cleanup outside the validated CI Compose project." >&2
    rm -f .env
    if ((original_status != 0)); then
      exit "$original_status"
    fi
    exit 2
  fi

  if ((original_status != 0)); then
    echo "Compose persistence smoke failed; collecting bounded PostgreSQL diagnostics." >&2
    docker compose ps --all >&2
    postgres_id="$(docker compose ps -q postgres)"
    if [[ -n "$postgres_id" ]]; then
      docker inspect "$postgres_id" --format '{{json .State}}' >&2
    fi
    docker compose logs --no-color --tail 200 postgres >&2
  fi

  # Both forms use the validated COMPOSE_PROJECT_NAME. The edge profile may
  # have created Caddy before the persistence stack starts.
  if ! docker compose --profile edge down --volumes --remove-orphans >&2; then
    echo "CI Compose edge-profile cleanup failed." >&2
    cleanup_status=1
  fi
  if ! docker compose down --volumes --remove-orphans >&2; then
    echo "CI Compose persistence cleanup failed." >&2
    cleanup_status=1
  fi
  if ! rm -f .env; then
    echo "CI Compose smoke could not remove its temporary .env." >&2
    cleanup_status=1
  fi

  if ((original_status != 0)); then
    exit "$original_status"
  fi
  exit "$cleanup_status"
}

if [[ "${BASH_SOURCE[0]}" == "$0" ]]; then
  if [[ "${1:-}" != "--validate-project" || $# -ne 1 ]]; then
    echo "Usage: $0 --validate-project" >&2
    exit 2
  fi
  ci_compose_set_project_from_run_id
  ci_compose_validate_project
  printf '%s\n' "$COMPOSE_PROJECT_NAME"
fi

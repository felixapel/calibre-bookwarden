"""Contracts for the disposable Compose project used by the container gate."""

import os
import shutil
import subprocess
from pathlib import Path

import pytest

ROOT = Path(__file__).resolve().parents[1]
SCRIPT = ROOT / "scripts" / "ci_compose_smoke.sh"


def _bash() -> str:
    candidates = [
        shutil.which("bash"),
        os.environ.get("PROGRAMFILES", "") + r"\Git\bin\bash.exe",
    ]
    for candidate in candidates:
        if candidate and Path(candidate).is_file():
            return candidate
    pytest.skip("Bash is required to validate the CI Compose helper")


def _validate_project(
    run_id: str, ambient_project: str = "bookaudit-certificate-a"
) -> subprocess.CompletedProcess[str]:
    environment = os.environ.copy()
    environment["GITHUB_RUN_ID"] = run_id
    environment["COMPOSE_PROJECT_NAME"] = ambient_project
    return subprocess.run(
        [_bash(), str(SCRIPT), "--validate-project"],
        capture_output=True,
        check=False,
        cwd=ROOT,
        env=environment,
        text=True,
    )


def test_ci_compose_project_is_derived_from_the_run_id_not_an_ambient_project() -> None:
    result = _validate_project("5675")

    assert result.returncode == 0
    assert result.stdout == "bookaudit-ci-5675\n"
    assert result.stderr == ""


@pytest.mark.parametrize("run_id", ["", "not-a-run", "5675;rm", "123456789012345678901"])
def test_ci_compose_project_rejects_malformed_run_ids(run_id: str) -> None:
    result = _validate_project(run_id)

    assert result.returncode == 2
    assert result.stdout == ""
    assert result.stderr == "GITHUB_RUN_ID must be a non-empty decimal CI run identifier.\n"


@pytest.mark.parametrize(
    ("original_status", "cleanup_status", "expected_status"),
    [(0, 0, 0), (7, 0, 7), (0, 1, 1), (7, 1, 7)],
)
def test_cleanup_preserves_failure_and_reports_cleanup_errors(
    tmp_path: Path, original_status: int, cleanup_status: int, expected_status: int
) -> None:
    environment = os.environ.copy()
    environment.update(
        GITHUB_RUN_ID="5676",
        ORIGINAL_STATUS=str(original_status),
        CLEANUP_STATUS=str(cleanup_status),
        HELPER=SCRIPT.as_posix(),
    )
    script = r"""source "$HELPER"
ci_compose_set_project_from_run_id
ci_compose_validate_project
docker() {
  printf '%s\n' "$*" >> calls.txt
  if [[ "$*" == "compose ps -q postgres" ]]; then printf 'fixture-postgres'; fi
  if [[ "$*" == *" down "* ]]; then return "$CLEANUP_STATUS"; fi
  return 0
}
trap 'ci_compose_smoke_cleanup' EXIT
printf fixture > .env
exit "$ORIGINAL_STATUS"
"""
    result = subprocess.run(
        [_bash(), "-c", script],
        cwd=tmp_path,
        env=environment,
        capture_output=True,
        text=True,
        check=False,
    )
    assert result.returncode == expected_status
    calls = (tmp_path / "calls.txt").read_text().splitlines()
    assert calls[-2:] == [
        "compose --profile edge down --volumes --remove-orphans",
        "compose down --volumes --remove-orphans",
    ]
    assert not (tmp_path / ".env").exists()
    if original_status:
        assert calls[:4] == [
            "compose ps --all",
            "compose ps -q postgres",
            "inspect fixture-postgres --format {{json .State}}",
            "compose logs --no-color --tail 200 postgres",
        ]
    else:
        assert len(calls) == 2
    assert ("cleanup failed" in result.stderr) == bool(cleanup_status)


@pytest.mark.parametrize(("original_status", "expected_status"), [(0, 2), (37, 37)])
def test_cleanup_rejects_a_valid_but_foreign_run_project(
    tmp_path: Path, original_status: int, expected_status: int
) -> None:
    environment = os.environ.copy()
    environment.update(
        GITHUB_RUN_ID="678",
        ORIGINAL_STATUS=str(original_status),
        HELPER=SCRIPT.as_posix(),
    )
    script = r"""source "$HELPER"
ci_compose_set_project_from_run_id
docker() { printf called >> calls.txt; }
trap 'ci_compose_smoke_cleanup' EXIT
COMPOSE_PROJECT_NAME=bookaudit-ci-999
GITHUB_RUN_ID=999
exit "$ORIGINAL_STATUS"
"""
    result = subprocess.run(
        [_bash(), "-c", script],
        cwd=tmp_path,
        env=environment,
        capture_output=True,
        text=True,
        check=False,
    )
    assert result.returncode == expected_status
    assert not (tmp_path / "calls.txt").exists()
    assert "Refusing cleanup outside the validated CI Compose project" in result.stderr

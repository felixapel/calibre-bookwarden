"""Static release-contract checks for the Certificate A deployment."""

import os
import subprocess
import tomllib
from pathlib import Path

import yaml

ROOT = Path(__file__).resolve().parents[1]
WORKFLOW = ROOT / ".gitea" / "workflows" / "v1-tests.yml"


def _fake_docker(tmp_path: Path, body: str) -> Path:
    executable = tmp_path / "docker"
    executable.write_text(f"#!/bin/sh\nset -eu\n{body}\n")
    executable.chmod(0o755)
    return executable


def test_compose_bootstraps_least_privilege_database_roles() -> None:
    compose = (ROOT / "docker-compose.yml").read_text()
    initializer = ROOT / "scripts" / "postgres-init-roles.sh"

    assert "postgres-init-roles.sh:/docker-entrypoint-initdb.d/10-bookaudit-roles.sh:ro" in compose
    script = initializer.read_text()
    for role in ("bookaudit_app", "bookaudit_verifier", "bookaudit_writer", "bookaudit_migrator"):
        assert role in script
    assert "ON_ERROR_STOP=1" in script
    assert "ALTER DEFAULT PRIVILEGES" in script
    assert "current_database()" in script


def test_compose_can_provision_roles_for_an_existing_postgres_volume() -> None:
    compose = yaml.safe_load((ROOT / "docker-compose.yml").read_text())
    provisioner = compose["services"]["provision-roles"]

    assert provisioner["profiles"] == ["maintenance"]
    assert provisioner["user"] == "70:70"
    assert provisioner["entrypoint"] == ["/opt/bookaudit/postgres-provision-roles.sh"]
    assert provisioner["environment"]["PGHOST"] == "postgres"
    assert provisioner["depends_on"]["postgres"]["condition"] == "service_healthy"
    assert "./scripts/postgres-init-roles.sh:/opt/bookaudit/postgres-provision-roles.sh:ro" in provisioner["volumes"]


def test_upgrade_runbook_provisions_roles_before_migrating() -> None:
    runbook = (ROOT / "docs" / "runbooks" / "production-operations.md").read_text()
    upgrade = runbook.split("## Upgrade", 1)[1].split("## Rollback", 1)[0]
    provision = "./scripts/certificate-a-compose.sh --profile maintenance run --rm provision-roles"
    migrate = "./scripts/certificate-a-compose.sh --profile maintenance run --rm migrate"

    assert provision in upgrade
    assert migrate in upgrade
    assert upgrade.index(provision) < upgrade.index(migrate)


def test_default_compose_is_the_exact_certificate_a_graph() -> None:
    compose = yaml.safe_load((ROOT / "docker-compose.yml").read_text())
    services = compose["services"]

    assert all("env_file" not in service for service in services.values())
    assert {name for name, service in services.items() if not service.get("profiles")} == {
        "app",
        "verifier",
        "postgres",
        "valkey",
    }
    assert not any("/library" in volume for volume in services["app"].get("volumes", []))
    assert any(volume.endswith(":/library:ro") for volume in services["verifier"]["volumes"])
    assert services["verifier"]["healthcheck"]["test"] == [
        "CMD",
        "bookaudit-certificate-a",
        "verifier-health",
    ]
    assert services["writer"]["profiles"] == ["writer"]
    assert services["retention"]["profiles"] == ["writer-maintenance"]
    assert any(volume.endswith(":/library:rw") for volume in services["writer"]["volumes"])


def test_stateful_dependencies_are_least_privilege_and_resource_bounded() -> None:
    compose = yaml.safe_load((ROOT / "docker-compose.yml").read_text())

    expected = {
        "postgres": {
            "user": "70:70",
            "mem_limit": "1g",
            "cpus": 1.0,
            "pids_limit": 128,
            "tmpfs": {
                "/tmp:size=64m,mode=1777",
                "/var/run/postgresql:size=16m,mode=0770,uid=70,gid=70",
            },
        },
        "valkey": {
            "user": "999:1000",
            "mem_limit": "512m",
            "cpus": 0.5,
            "pids_limit": 128,
            "tmpfs": {"/tmp:size=32m,mode=1777"},
        },
    }
    for name, contract in expected.items():
        service = compose["services"][name]
        assert service["read_only"] is True
        assert service["cap_drop"] == ["ALL"]
        assert service["security_opt"] == ["no-new-privileges:true"]
        assert service["init"] is True
        assert service["user"] == contract["user"]
        assert service["mem_limit"] == contract["mem_limit"]
        assert service["cpus"] == contract["cpus"]
        assert service["pids_limit"] == contract["pids_limit"]
        assert set(service["tmpfs"]) == contract["tmpfs"]


def test_prometheus_is_an_isolated_opt_in_certificate_a_service() -> None:
    compose = yaml.safe_load((ROOT / "docker-compose.yml").read_text())
    prometheus = compose["services"]["prometheus"]

    assert prometheus["profiles"] == ["monitoring"]
    assert prometheus["image"] == (
        "prom/prometheus@sha256:508729e0e2d18e11fd742a5a5ca70e557b940a93948c3c95fd0123a6fd538b69"
    )
    assert prometheus["user"] == "${UID:-1000}:${GID:-1000}"
    assert prometheus["read_only"] is True
    assert prometheus["cap_drop"] == ["ALL"]
    assert prometheus["security_opt"] == ["no-new-privileges:true"]
    assert prometheus["depends_on"]["app"]["condition"] == "service_healthy"
    assert prometheus["ports"] == ["127.0.0.1:${BOOKAUDIT_PROMETHEUS_PORT:-19090}:9090"]
    assert "./ops/monitoring/prometheus.yml:/etc/prometheus/prometheus.yml:ro" in prometheus["volumes"]
    assert "./ops/monitoring/alerts.yml:/etc/prometheus/alerts.yml:ro" in prometheus["volumes"]
    assert "./.monitoring/data:/prometheus" in prometheus["volumes"]
    assert prometheus["secrets"] == [{"source": "bookaudit-api-key", "target": "bookaudit_api_key"}]
    assert compose["secrets"]["bookaudit-api-key"]["file"] == "./.monitoring/bookaudit_api_key"
    assert prometheus["networks"] == ["monitoring"]
    assert compose["services"]["app"]["networks"] == ["default", "monitoring"]
    assert compose["networks"]["monitoring"] == {
        "driver_opts": {"com.docker.network.bridge.enable_ip_masquerade": "false"}
    }
    assert prometheus["healthcheck"]["test"] == [
        "CMD",
        "promtool",
        "check",
        "ready",
        "--url=http://localhost:9090",
    ]
    assert "--storage.tsdb.retention.time=30d" in prometheus["command"]
    assert "--storage.tsdb.retention.size=2GB" in prometheus["command"]
    assert not any("/library" in volume for volume in prometheus["volumes"])
    assert not any(name in prometheus.get("environment", {}) for name in ("POSTGRES", "VALKEY", "API_KEY"))


def test_compose_project_name_is_portable_to_legacy_compose() -> None:
    compose = yaml.safe_load((ROOT / "docker-compose.yml").read_text())
    environment_example = (ROOT / ".env.example").read_text().splitlines()

    assert "name" not in compose
    assert "COMPOSE_PROJECT_NAME=bookaudit-certificate-a" in environment_example


def test_certificate_a_compose_wrapper_ignores_ambient_project_override(tmp_path: Path) -> None:
    guard_log = tmp_path / "guard.log"
    _fake_docker(
        tmp_path,
        f'if [ "${{1:-}}" = ps ]; then printf "checked\\n" > "{guard_log}"; exit 0; fi\n'
        "printf 'env:%s|%s|%s|%s|%s|%s\\n' "
        '"${COMPOSE_PROJECT_NAME-unset}" "${COMPOSE_FILE-unset}" '
        '"${COMPOSE_PROFILES-unset}" "${COMPOSE_ENV_FILES-unset}" '
        '"${BOOKAUDIT_IMAGE-unset}" "${BOOKAUDIT_LIBRARY_HOST_PATH-unset}"\n'
        "printf '%s\\n' \"$@\"",
    )
    environment = os.environ.copy()
    environment["PATH"] = f"{tmp_path}:{environment['PATH']}"
    environment["COMPOSE_PROJECT_NAME"] = "calibre-ai-auditor"
    environment["COMPOSE_FILE"] = "compose.shadow-local.yml"
    environment["COMPOSE_PROFILES"] = "writer"
    environment["COMPOSE_ENV_FILES"] = "/tmp/hostile.env"
    environment["BOOKAUDIT_IMAGE"] = "calibre-ai-auditor:1.2.1-shadow"
    environment["BOOKAUDIT_LIBRARY_HOST_PATH"] = "/tmp/hostile-library"

    result = subprocess.run(
        [str(ROOT / "scripts" / "certificate-a-compose.sh"), "config", "--services"],
        check=True,
        capture_output=True,
        env=environment,
        text=True,
    )

    assert result.stdout.splitlines() == [
        "env:unset|unset||unset|unset|unset",
        "compose",
        "--project-name",
        "bookaudit-certificate-a",
        "--file",
        str(ROOT / "docker-compose.yml"),
        "--env-file",
        str(ROOT / ".env"),
        "config",
        "--services",
    ]
    assert guard_log.read_text() == "checked\n"


def test_certificate_a_project_guard_rejects_existing_writer(tmp_path: Path) -> None:
    _fake_docker(tmp_path, "printf '%s\\n' 'abc123|writer|/tmp/hostile.yml|deadbeef|False'")
    environment = os.environ.copy()
    environment["PATH"] = f"{tmp_path}:{environment['PATH']}"

    result = subprocess.run(
        [str(ROOT / "scripts" / "check-certificate-a-compose-project.sh")],
        check=False,
        capture_output=True,
        env=environment,
        text=True,
    )

    assert result.returncode == 1
    assert result.stderr == "Unexpected service in the Certificate A Compose project: writer\n"


def test_certificate_a_project_guard_fails_when_docker_inventory_fails(tmp_path: Path) -> None:
    _fake_docker(tmp_path, "exit 42")
    environment = os.environ.copy()
    environment["PATH"] = f"{tmp_path}:{environment['PATH']}"

    result = subprocess.run(
        [str(ROOT / "scripts" / "check-certificate-a-compose-project.sh")],
        check=False,
        capture_output=True,
        env=environment,
        text=True,
    )

    assert result.returncode == 1
    assert result.stderr == "Could not inspect the Certificate A Compose project.\n"


def test_certificate_a_project_guard_rejects_missing_service_label(tmp_path: Path) -> None:
    _fake_docker(tmp_path, "printf '%s\\n' 'abc123||/tmp/hostile.yml|deadbeef|False'")
    environment = os.environ.copy()
    environment["PATH"] = f"{tmp_path}:{environment['PATH']}"

    result = subprocess.run(
        [str(ROOT / "scripts" / "check-certificate-a-compose-project.sh")],
        check=False,
        capture_output=True,
        env=environment,
        text=True,
    )

    assert result.returncode == 1
    assert result.stderr == "Unexpected container in the Certificate A Compose project: abc123\n"


def test_certificate_a_project_guard_rejects_unreviewed_compose_file(tmp_path: Path) -> None:
    _fake_docker(
        tmp_path,
        "printf '%s\\n' 'abc123|app|/tmp/compose.shadow-local.yml|deadbeef|False'",
    )
    environment = os.environ.copy()
    environment["PATH"] = f"{tmp_path}:{environment['PATH']}"

    result = subprocess.run(
        [str(ROOT / "scripts" / "check-certificate-a-compose-project.sh")],
        check=False,
        capture_output=True,
        env=environment,
        text=True,
    )

    assert result.returncode == 1
    assert result.stderr == (
        "Unexpected Compose file provenance for Certificate A service app: /tmp/compose.shadow-local.yml\n"
    )


def test_certificate_a_project_guard_rejects_duplicate_service_instances(tmp_path: Path) -> None:
    compose_file = ROOT / "docker-compose.yml"
    _fake_docker(
        tmp_path,
        f"printf '%s\\n' 'abc123|app|{compose_file}|hash-one|False' 'def456|app|{compose_file}|hash-two|False'",
    )
    environment = os.environ.copy()
    environment["PATH"] = f"{tmp_path}:{environment['PATH']}"

    result = subprocess.run(
        [str(ROOT / "scripts" / "check-certificate-a-compose-project.sh")],
        check=False,
        capture_output=True,
        env=environment,
        text=True,
    )

    assert result.returncode == 1
    assert result.stderr == "Duplicate Certificate A service instance: app\n"


def test_certificate_a_project_guard_rejects_oneoff_container(tmp_path: Path) -> None:
    compose_file = ROOT / "docker-compose.yml"
    _fake_docker(
        tmp_path,
        f"printf '%s\\n' 'abc123|app|{compose_file}|deadbeef|True'",
    )
    environment = os.environ.copy()
    environment["PATH"] = f"{tmp_path}:{environment['PATH']}"

    result = subprocess.run(
        [str(ROOT / "scripts" / "check-certificate-a-compose-project.sh")],
        check=False,
        capture_output=True,
        env=environment,
        text=True,
    )

    assert result.returncode == 1
    assert result.stderr == "Unexpected one-off Certificate A container: abc123\n"


def test_certificate_a_project_guard_rejects_compound_service_label(tmp_path: Path) -> None:
    compose_file = ROOT / "docker-compose.yml"
    _fake_docker(
        tmp_path,
        f"printf '%s\\n' 'abc123|app postgres|{compose_file}|deadbeef|False'",
    )
    environment = os.environ.copy()
    environment["PATH"] = f"{tmp_path}:{environment['PATH']}"

    result = subprocess.run(
        [str(ROOT / "scripts" / "check-certificate-a-compose-project.sh")],
        check=False,
        capture_output=True,
        env=environment,
        text=True,
    )

    assert result.returncode == 1
    assert result.stderr == "Unexpected service in the Certificate A Compose project: app postgres\n"


def test_certificate_a_project_guard_rejects_stale_exec_service_hash(tmp_path: Path) -> None:
    compose_file = ROOT / "docker-compose.yml"
    _fake_docker(
        tmp_path,
        'if [ "${1:-}" = compose ]; then '
        "printf '%s\\n' 'app reviewed-hash'; exit 0; fi\n"
        f"printf '%s\\n' 'abc123|app|{compose_file}|stale-hash|False'",
    )
    environment = os.environ.copy()
    environment["PATH"] = f"{tmp_path}:{environment['PATH']}"

    result = subprocess.run(
        [
            str(ROOT / "scripts" / "check-certificate-a-compose-project.sh"),
            "--require-current",
            "app",
        ],
        check=False,
        capture_output=True,
        env=environment,
        text=True,
    )

    assert result.returncode == 1
    assert result.stderr == "Certificate A service app does not match the reviewed environment.\n"


def test_certificate_a_compose_wrapper_binds_exec_to_reviewed_service_hash(tmp_path: Path) -> None:
    compose_file = ROOT / "docker-compose.yml"
    _fake_docker(
        tmp_path,
        'if [ "${1:-}" = compose ]; then '
        "printf '%s\\n' 'app reviewed-hash'; exit 0; fi\n"
        f"printf '%s\\n' 'abc123|app|{compose_file}|stale-hash|False'",
    )
    environment = os.environ.copy()
    environment["PATH"] = f"{tmp_path}:{environment['PATH']}"

    result = subprocess.run(
        [str(ROOT / "scripts" / "certificate-a-compose.sh"), "exec", "-T", "app", "true"],
        check=False,
        capture_output=True,
        env=environment,
        text=True,
    )

    assert result.returncode == 1
    assert result.stderr == "Certificate A service app does not match the reviewed environment.\n"


def test_certificate_a_compose_wrapper_rejects_writer_and_identity_overrides(tmp_path: Path) -> None:
    _fake_docker(tmp_path, "exit 0")
    environment = os.environ.copy()
    environment["PATH"] = f"{tmp_path}:{environment['PATH']}"

    for arguments in (
        ("--profile", "writer", "config"),
        ("--profile=writer-maintenance", "config"),
        ("--project-name", "calibre-ai-auditor", "config"),
        ("-pcalibre-ai-auditor", "config"),
        ("--file=compose.shadow-local.yml", "config"),
        ("-fcompose.shadow-local.yml", "config"),
        ("--env-file", "/tmp/hostile.env", "config"),
        ("run", "writer"),
        ("up", "retention"),
        ("up", "--scale", "verifier=2"),
        ("up", "--scale=verifier=2"),
        ("scale", "verifier=2"),
        ("down", "--volumes"),
        ("down", "-v"),
    ):
        result = subprocess.run(
            [str(ROOT / "scripts" / "certificate-a-compose.sh"), *arguments],
            check=False,
            capture_output=True,
            env=environment,
            text=True,
        )

        assert result.returncode == 1


def test_certificate_a_compose_wrapper_permits_only_monitoring_or_maintenance_profiles(
    tmp_path: Path,
) -> None:
    _fake_docker(tmp_path, "exit 0")
    environment = os.environ.copy()
    environment["PATH"] = f"{tmp_path}:{environment['PATH']}"

    for arguments in (
        ("--profile", "monitoring", "config"),
        ("--profile=monitoring", "config"),
        ("--profile", "maintenance", "config"),
    ):
        result = subprocess.run(
            [str(ROOT / "scripts" / "certificate-a-compose.sh"), *arguments],
            check=False,
            capture_output=True,
            env=environment,
            text=True,
        )

        assert result.returncode == 0


def test_certificate_a_compose_wrapper_requires_safe_monitoring_selection(tmp_path: Path) -> None:
    _fake_docker(tmp_path, "exit 0")
    environment = os.environ.copy()
    environment["PATH"] = f"{tmp_path}:{environment['PATH']}"

    for arguments in (
        ("up", "prometheus"),
        ("--profile", "monitoring", "down"),
        ("--profile=monitoring", "down"),
    ):
        result = subprocess.run(
            [str(ROOT / "scripts" / "certificate-a-compose.sh"), *arguments],
            check=False,
            capture_output=True,
            env=environment,
            text=True,
        )

        assert result.returncode == 1


def test_monitoring_runbook_uses_unambiguous_force_option_for_rollback() -> None:
    runbook = (ROOT / "docs" / "runbooks" / "production-operations.md").read_text()

    assert "rm --force prometheus" in runbook
    assert "rm -f prometheus" not in runbook


def test_restore_drill_wrapper_permits_disposable_volume_removal(tmp_path: Path) -> None:
    _fake_docker(tmp_path, "exit 0")
    environment = os.environ.copy()
    environment["PATH"] = f"{tmp_path}:{environment['PATH']}"

    result = subprocess.run(
        [
            str(ROOT / "scripts" / "certificate-a-compose.sh"),
            "--restore-drill",
            "down",
            "--volumes",
        ],
        check=False,
        capture_output=True,
        env=environment,
        text=True,
    )

    assert result.returncode == 0


def test_restore_drill_uses_the_pinned_wrapper_project() -> None:
    runbook = (ROOT / "docs" / "runbooks" / "production-operations.md").read_text()
    restore = runbook.split("## Empty-environment restore drill", 1)[1].split("## Upgrade", 1)[0]

    assert "docker compose" not in restore
    assert restore.count("./scripts/certificate-a-compose.sh --restore-drill") == 4


def test_compose_uses_separate_configurable_certificate_and_writer_images() -> None:
    compose = (ROOT / "docker-compose.yml").read_text()

    assert compose.count("${BOOKAUDIT_IMAGE:-calibre-ai-auditor:1.2.1}") == 3
    assert compose.count("${BOOKAUDIT_WRITER_IMAGE:-calibre-ai-auditor-writer:1.2.1}") == 2
    assert '"127.0.0.1:${BOOKAUDIT_PORT:-8080}:8080"' in compose


def test_certificate_a_preflight_does_not_prepare_writer_or_legacy_state() -> None:
    script = (ROOT / "scripts" / "prepare-production.sh").read_text()

    assert "Certificate A bind mounts" in script
    assert 'compose=("./scripts/certificate-a-compose.sh")' in script
    assert '"./scripts/check-certificate-a-compose-project.sh"' in script
    assert 'actual_services="$("${compose[@]}" config --services' in script
    assert '"app postgres valkey verifier "' in script
    assert '"app postgres prometheus valkey verifier "' in script
    assert 'monitoring_root=".monitoring"' in script
    assert "bookaudit_api_key" in script
    assert "org.opencontainers.image.revision" in script
    assert "BOOKAUDIT_SOURCE_REVISION" in script
    assert 'docker image inspect "$release_image"' in script
    assert 'git diff --quiet HEAD -- "$monitored_path"' in script
    assert script.index('git diff --quiet HEAD -- "$monitored_path"') < script.index(
        'mv -f "$secret_tmp" "$monitoring_root/bookaudit_api_key"'
    )
    assert "backups" in script
    for excluded in (".state", ".artifacts", ".writer-artifacts", "user_library"):
        assert excluded not in script


def test_runtime_images_are_non_root_with_a_writable_ephemeral_home() -> None:
    dockerfile = (ROOT / "Dockerfile").read_text()

    assert "USER 10001:10001" in dockerfile
    assert "HOME=/tmp/bookaudit-home" in dockerfile
    assert "XDG_CACHE_HOME=/tmp/bookaudit-home/.cache" in dockerfile
    assert "XDG_CONFIG_HOME=/tmp/bookaudit-home/.config" in dockerfile


def test_release_images_embed_the_exact_source_revision() -> None:
    dockerfile = (ROOT / "Dockerfile").read_text()
    workflow = WORKFLOW.read_text()

    assert dockerfile.count("ARG BOOKAUDIT_BUILD_REVISION") == 2
    assert dockerfile.count('LABEL org.opencontainers.image.revision="$BOOKAUDIT_BUILD_REVISION"') == 2
    assert workflow.count('--build-arg BOOKAUDIT_BUILD_REVISION="$reviewed_revision"') == 2
    assert workflow.count("org.opencontainers.image.revision") >= 2
    assert 'reviewed_revision="$(git rev-parse HEAD)"' in workflow
    assert 'test "$GITHUB_SHA" = "$reviewed_revision"' in workflow
    assert "grep -Eq '^[0-9a-f]{40}$'" in workflow
    assert workflow.count('= "$reviewed_revision"') >= 3


def test_default_image_excludes_quarantined_packages_and_calibre() -> None:
    dockerfile = (ROOT / "Dockerfile").read_text()
    pyproject = (ROOT / "pyproject.toml").read_text()
    certificate = dockerfile.split("FROM runtime-common AS certificate-a", 1)[1]
    common = dockerfile.split("FROM python:3.12.13-slim-bookworm", 2)[2].split("FROM runtime-common AS writer", 1)[0]

    assert "uv sync --frozen --no-dev --no-editable" in dockerfile
    assert "--extra legacy --extra mcp --extra ingest" in dockerfile
    assert "FROM python-builder AS python-builder-legacy\n" in dockerfile
    assert "ENV UV_COMPILE_BYTECODE=0" in dockerfile
    assert 'ENTRYPOINT ["bookaudit-certificate-a"]' in certificate
    assert "calibre.txz" not in common
    assert "--extra legacy" not in certificate
    base_dependencies = pyproject.split("[project.optional-dependencies]", 1)[0]
    for dependency in ("openai", "google-genai", "qdrant-client", "fastmcp", "pymupdf4llm"):
        assert dependency not in base_dependencies


def test_canonical_gitea_pipeline_proves_the_exact_release_boundary() -> None:
    content = WORKFLOW.read_text()
    parsed = yaml.safe_load(content)

    assert set(parsed["jobs"]) == {"backend", "real-services", "webui", "container", "benchmarks"}
    assert "curl |" not in content
    assert "curl -s" not in content
    assert "uv lock --check" in content
    assert "uv run mypy src" in content
    assert "tests/test_runtime_role_acl_postgres.py" in content
    assert "tests/test_certificate_a_worker_postgres.py" in content
    assert "tests/test_certificate_a_real_boundaries.py" in content
    assert "uv run pytest tests/test_v2_supervised_pilot_integration.py" not in content
    assert "uv run --no-sync ocrmypdf --version" in content
    assert "command -v ocrmypdf" not in content
    real_services = content.split("\n  real-services:\n", 1)[1].split("\n  webui:\n", 1)[0]
    assert 'uv sync --python "$PYTHON_VERSION" --frozen --no-dev --group test' in real_services
    assert real_services.count("uv run --no-sync") == 4
    assert "uv run ocrmypdf" not in real_services
    assert "uv run bookaudit-certificate-a" not in real_services
    assert "uv run pytest" not in real_services
    assert (
        "mcr.microsoft.com/playwright:v1.61.1-jammy"
        "@sha256:e4f20543d7da3faeddbce0176b447331ad652f0ca8c669dc7e7b205f2067677e"
    ) in content
    assert "uvx pip-audit==2.10.1" in content
    assert "npm audit --audit-level=high" in content
    assert "--project=chromium --project=mobile-chromium" in content
    for action in ("actions/checkout@", "astral-sh/setup-uv@", "actions/setup-node@"):
        for line in (line.strip() for line in content.splitlines() if action in line):
            revision = line.rsplit("@", 1)[1]
            assert len(revision) == 40


def test_real_service_dependencies_are_a_small_locked_group() -> None:
    project = tomllib.loads((ROOT / "pyproject.toml").read_text())
    groups = project["dependency-groups"]

    assert set(groups["test"]) == {
        "pytest>=8.2.0",
        "pytest-asyncio>=1.3.0",
    }
    assert {"include-group": "test"} in groups["dev"]


def test_gitea_container_gate_builds_and_scans_both_separated_images() -> None:
    content = WORKFLOW.read_text()
    container = content.split("\n  container:\n", 1)[1]

    assert "docker build --target certificate-a" in container
    assert "docker build --target writer" in container
    assert "test ! -e /opt/calibre/calibredb" in container
    assert r"u.find_spec(\"openai\") is None" in container
    assert r"u.find_spec(\"qdrant_client\") is None" in container
    assert r"u.find_spec(\"fastmcp\") is None" in container
    assert "docker-compose " not in container
    assert "docker/compose/releases/download/v5.1.4/docker-compose-linux-x86_64" in container
    assert "33b208d7e76639db742fae84b966cc01dacae58ca3fc4dabbc907045aefdf0c4" in container
    profile_command = "--profile maintenance --profile writer --profile writer-maintenance"
    assert f"docker compose {profile_command} config --services" in container
    assert '"app migrate postgres provision-roles retention valkey verifier writer "' in container
    assert f"docker compose {profile_command} config -q" in container
    assert "docker compose --profile monitoring config --services" in container
    assert '"app postgres prometheus valkey verifier "' in container
    assert "docker compose up -d --wait postgres valkey" in container
    assert "CREATE TABLE provenance_smoke" in container
    assert "valkey-cli set production-smoke persisted" in container
    assert "docker compose restart postgres valkey" in container
    assert "SELECT value FROM provenance_smoke" in container
    assert "valkey-cli get production-smoke" in container
    assert "docker build --file ops/monitoring/Dockerfile.ci" in container
    assert "com.docker.network.bridge.enable_ip_masquerade=false" in container
    assert "-p 127.0.0.1::9090" in container
    assert 'index .NetworkSettings.Ports "9090/tcp"' in container
    assert "Prometheus unexpectedly reached an external address" in container
    assert '"$PWD/ops/monitoring' not in container
    assert '"$PWD/.monitoring-ci' not in container
    assert "docker cp ops/monitoring/.trivyignore" in container
    assert "--scanners vuln,secret" in container
    assert "--severity HIGH,CRITICAL --ignore-unfixed" in container


def test_webui_uses_only_the_npm_lockfile_and_a_static_mock_server() -> None:
    playwright = (ROOT / "webui" / "playwright.config.ts").read_text()
    html = (ROOT / "webui" / "index.html").read_text()

    assert (ROOT / "webui" / "package-lock.json").is_file()
    assert not (ROOT / "webui" / "pnpm-lock.yaml").exists()
    assert "npm run build && npm run preview" in playwright
    assert "BOOKAUDIT_DATABASE__BACKEND" not in playwright
    assert "fonts.googleapis.com" not in html
    assert "fonts.gstatic.com" not in html


def test_same_host_tls_proxy_authenticates_every_path() -> None:
    caddyfile = (ROOT / "deploy" / "caddy" / "Caddyfile.example").read_text()

    assert "{$BOOKAUDIT_DOMAIN} {" in caddyfile
    assert "basic_auth {" in caddyfile
    assert "basic_auth /api" not in caddyfile
    assert "reverse_proxy 127.0.0.1:{$BOOKAUDIT_PORT:8080}" in caddyfile
    assert "-Server" in caddyfile
    assert 'Strict-Transport-Security "max-age=31536000; includeSubDomains"' in caddyfile


def test_monitoring_is_certificate_a_only_and_uses_a_secret_file() -> None:
    alerts = (ROOT / "ops" / "monitoring" / "alerts.yml").read_text()
    prometheus = (ROOT / "ops" / "monitoring" / "prometheus.yml").read_text()

    assert "BookAuditVerifierHeartbeatStale" in alerts
    assert "BookAuditCertificateARunStalled" in alerts
    assert 'changes(bookaudit_certificate_a_runs{status=~"failed|source_changed|blocked_recovery"}[10m])' in alerts
    assert "bookaudit_certificate_a_metrics_collection_success" in alerts
    for excluded in ("WriterHeartbeat", "OutboxBacklog", "V2Pilot"):
        assert excluded not in alerts
    assert 'targets: ["app:8080"]' in prometheus
    assert "fallback_scrape_protocol: PrometheusText0.0.4" in prometheus
    assert 'files: ["/run/secrets/bookaudit_api_key"]' in prometheus
    assert "REPLACE_WITH" not in prometheus

    workflow = WORKFLOW.read_text()
    assert "promtool" in workflow
    assert "test rules alerts.test.yml" in workflow
    assert (ROOT / "ops" / "monitoring" / "alerts.test.yml").is_file()
    ignored = [
        line
        for line in (ROOT / "ops" / "monitoring" / ".trivyignore").read_text().splitlines()
        if line and not line.startswith("#")
    ]
    assert ignored == ["CVE-2026-42154"]
    assert "--ignorefile /tmp/bookaudit-prometheus.trivyignore" in workflow


def test_canonical_gpl_license_is_installed() -> None:
    license_text = (ROOT / "LICENSE").read_text()

    assert "GNU GENERAL PUBLIC LICENSE" in license_text
    assert "Version 3, 29 June 2007" in license_text
    assert "COPY LICENSE /app/LICENSE" in (ROOT / "Dockerfile").read_text()


def test_local_gate_uses_executable_checks_not_completion_markers() -> None:
    gate = (ROOT / "scripts" / "verify-calibre-gate.sh").read_text()

    for command in (
        "uv lock --check",
        "uv sync --frozen",
        "uv run ruff check .",
        "uv run ruff format --check .",
        "uv run mypy src",
        "uv run pytest",
        "uv run bookaudit-certificate-a --help",
    ):
        assert command in gate
    assert "|| true" not in gate

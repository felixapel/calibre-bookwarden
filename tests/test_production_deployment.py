"""Static release-contract checks for the Certificate A deployment."""

from pathlib import Path

import yaml

ROOT = Path(__file__).resolve().parents[1]
WORKFLOW = ROOT / ".gitea" / "workflows" / "v1-tests.yml"


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


def test_compose_uses_separate_configurable_certificate_and_writer_images() -> None:
    compose = (ROOT / "docker-compose.yml").read_text()

    assert compose.count("${BOOKAUDIT_IMAGE:-calibre-ai-auditor:1.2.1}") == 3
    assert compose.count("${BOOKAUDIT_WRITER_IMAGE:-calibre-ai-auditor-writer:1.2.1}") == 2
    assert '"127.0.0.1:${BOOKAUDIT_PORT:-8080}:8080"' in compose


def test_certificate_a_preflight_does_not_prepare_writer_or_legacy_state() -> None:
    script = (ROOT / "scripts" / "prepare-production.sh").read_text()

    assert "Certificate A bind mounts" in script
    assert 'actual_services="$(COMPOSE_PROFILES=' in script
    assert '"app postgres valkey verifier "' in script
    assert "backups" in script
    for excluded in (".state", ".artifacts", ".writer-artifacts", "user_library"):
        assert excluded not in script


def test_runtime_images_are_non_root_with_a_writable_ephemeral_home() -> None:
    dockerfile = (ROOT / "Dockerfile").read_text()

    assert "USER 10001:10001" in dockerfile
    assert "HOME=/tmp/bookaudit-home" in dockerfile
    assert "XDG_CACHE_HOME=/tmp/bookaudit-home/.cache" in dockerfile
    assert "XDG_CONFIG_HOME=/tmp/bookaudit-home/.config" in dockerfile


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
    assert "uv run ocrmypdf --version" in content
    assert "command -v ocrmypdf" not in content
    assert "uvx pip-audit==2.10.1" in content
    assert "npm audit --audit-level=high" in content
    assert "--project=chromium --project=mobile-chromium" in content
    for action in ("actions/checkout@", "astral-sh/setup-uv@", "actions/setup-node@"):
        for line in (line.strip() for line in content.splitlines() if action in line):
            revision = line.rsplit("@", 1)[1]
            assert len(revision) == 40


def test_gitea_container_gate_builds_and_scans_both_separated_images() -> None:
    content = WORKFLOW.read_text()
    container = content.split("\n  container:\n", 1)[1]

    assert "docker build --target certificate-a" in container
    assert "docker build --target writer" in container
    assert "test ! -e /opt/calibre/calibredb" in container
    assert r"u.find_spec(\"openai\") is None" in container
    assert r"u.find_spec(\"qdrant_client\") is None" in container
    assert r"u.find_spec(\"fastmcp\") is None" in container
    assert "docker-compose config --services" in container
    assert '"app postgres valkey verifier "' in container
    assert "--profile maintenance --profile writer --profile writer-maintenance config -q" in container
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
    assert 'targets: ["127.0.0.1:8080"]' in prometheus
    assert 'files: ["/etc/prometheus/secrets/bookaudit_api_key"]' in prometheus
    assert "REPLACE_WITH" not in prometheus


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

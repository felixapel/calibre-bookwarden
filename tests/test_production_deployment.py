from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]


def test_compose_bootstraps_least_privilege_database_roles() -> None:
    compose = (ROOT / "docker-compose.yml").read_text()
    initializer = ROOT / "scripts" / "postgres-init-roles.sh"

    assert initializer.is_file()
    assert "postgres-init-roles.sh:/docker-entrypoint-initdb.d/10-bookaudit-roles.sh:ro" in compose
    script = initializer.read_text()
    for role in ("bookaudit_app", "bookaudit_writer", "bookaudit_migrator"):
        assert role in script
    assert "ON_ERROR_STOP=1" in script
    assert "ALTER DEFAULT PRIVILEGES" in script


def test_compose_uses_current_configurable_image_reference() -> None:
    compose = (ROOT / "docker-compose.yml").read_text()

    assert compose.count("${BOOKAUDIT_IMAGE:-calibre-ai-auditor:1.2.1}") == 4
    assert "calibre-ai-auditor:v0.1" not in compose


def test_runtime_image_has_writable_non_root_home() -> None:
    dockerfile = (ROOT / "Dockerfile").read_text()

    assert "HOME=/tmp/bookaudit-home" in dockerfile
    assert "XDG_CACHE_HOME=/tmp/bookaudit-home/.cache" in dockerfile
    assert "XDG_CONFIG_HOME=/tmp/bookaudit-home/.config" in dockerfile


def test_compose_requires_and_health_checks_the_single_writer() -> None:
    compose = (ROOT / "docker-compose.yml").read_text()

    assert "BOOKAUDIT_REQUIRE_WRITER_READY: ${BOOKAUDIT_REQUIRE_WRITER_READY:-true}" in compose
    assert '["CMD", "bookaudit", "writer-health"]' in compose


def test_production_preflight_creates_all_bind_mount_targets() -> None:
    preflight = ROOT / "scripts" / "prepare-production.sh"

    assert preflight.is_file()
    script = preflight.read_text()
    for directory in (".state", ".artifacts", ".writer-artifacts", "user_library", "backups"):
        assert directory in script
    assert "docker compose" in script
    assert "config -q" in script


def test_retention_maintenance_service_is_explicit_and_fail_closed() -> None:
    compose = (ROOT / "docker-compose.yml").read_text()

    assert "retention:" in compose
    assert 'command: ["retention"]' in compose
    assert "./.writer-artifacts:/writer-artifacts" in compose
    assert "${BOOKAUDIT_BACKUP_HOST_PATH:-./backups}:/backups:ro" in compose
    assert "BOOKAUDIT_DATABASE__POSTGRES_DSN" in compose
    assert "--backup-reference is required with --execute" in (ROOT / "src/calibre_ai_auditor/cli/main.py").read_text()
    assert "acquire_writer_guard" in (ROOT / "src/calibre_ai_auditor/cli/main.py").read_text()
    runbook = (ROOT / "docs/runbooks/production-operations.md").read_text()
    assert "run --rm retention retention" in runbook


def test_release_workflow_publishes_only_after_full_gates() -> None:
    release = (ROOT / ".github" / "workflows" / "release.yml").read_text()
    ci = (ROOT / ".github" / "workflows" / "ci.yml").read_text()
    legacy_gate = (ROOT / "scripts" / "verify-calibre-gate.sh").read_text()

    assert "needs: verify" in release
    assert "cosign sign --yes" in release
    assert "attest-build-provenance@" in release
    assert "attest-sbom@" in release
    assert "push-by-digest=true" in release
    assert "docker buildx imagetools create" in release
    assert "UPDATE operationledger SET state = state WHERE false" in release
    assert "grep -F '42501'" in release
    assert "permission denied for table operationledger" in release
    assert release.index("cosign sign --yes") < release.index("docker buildx imagetools create")
    assert "Vendor-unfixed vulnerability exception expired" in release
    assert "benchmark_50k_metadata.py" in release
    assert "TEST_POSTGRES_DSN" in ci
    assert "benchmark_50k_metadata.py" in ci
    assert "|| true" not in legacy_gate


def test_all_production_gates_force_real_retention_services() -> None:
    workflows = (
        ROOT / ".gitea" / "workflows" / "v1-tests.yml",
        ROOT / ".github" / "workflows" / "ci.yml",
        ROOT / ".github" / "workflows" / "release.yml",
    )

    for workflow in workflows:
        content = workflow.read_text()
        assert "TEST_POSTGRES_DSN" in content
        assert "TEST_VALKEY_URL" in content
        assert "valkey/valkey:8.1.3-alpine@sha256:" in content
        assert "uv run bookaudit migrate" in content
        assert "uv run pytest tests/test_retention_postgres_valkey.py -q" in content


def test_browser_gate_bootstraps_and_launches_backend_portably() -> None:
    playwright = (ROOT / "webui" / "playwright.config.ts").read_text()
    ci = (ROOT / ".github" / "workflows" / "ci.yml").read_text()
    frontend = ci.split("  frontend:\n", 1)[1].split("\n  container:\n", 1)[0]

    assert "source .venv/bin/activate" not in playwright
    assert "cwd: ROOT_DIR" in playwright
    assert "BOOKAUDIT_STATIC_DIR: STATIC_DIR" in playwright
    assert "astral-sh/setup-uv@" in frontend
    assert 'python-version: "3.12.13"' in frontend
    assert "uv sync --frozen --extra dev" in frontend


def test_image_gates_keep_secret_scanning_with_one_exact_dependency_exclusion() -> None:
    workflows = (
        ROOT / ".gitea" / "workflows" / "v1-tests.yml",
        ROOT / ".github" / "workflows" / "ci.yml",
        ROOT / ".github" / "workflows" / "release.yml",
    )
    action = "uses: aquasecurity/trivy-action@915b19bbe73b92a6cf82a1bc12b087c9a19a5fe2"
    excluded_file = "opt/venv/lib/python3.12/site-packages/google/auth/crypt/__pycache__/_python_rsa.cpython-312.pyc"

    for workflow in workflows:
        content = workflow.read_text()
        assert content.count(action) == 1
        scan = content.split(action, 1)[1].split("\n      - ", 1)[0]
        settings = [line.strip() for line in scan.splitlines()]
        assert [line for line in settings if line.startswith("scanners:")] == ["scanners: vuln,secret"]
        assert [line for line in settings if line.startswith("skip-files:")] == [f"skip-files: {excluded_file}"]
        assert [line for line in settings if line.startswith("exit-code:")] == ['exit-code: "1"']
        assert [line for line in settings if line.startswith("severity:")] == ["severity: HIGH,CRITICAL"]

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


def test_github_image_gates_keep_secret_scanning_with_one_exact_dependency_exclusion() -> None:
    workflows = (
        ROOT / ".github" / "workflows" / "ci.yml",
        ROOT / ".github" / "workflows" / "release.yml",
    )
    action = "uses: aquasecurity/trivy-action@ed142fd0673e97e23eac54620cfb913e5ce36c25"
    trivy_image = "aquasec/trivy@sha256:c42bb3221509b0a9fa2291cd79a3a818b30a172ab87e9aac8a43997a5b56f293"
    excluded_file = "opt/venv/lib/python3.12/site-packages/google/auth/crypt/__pycache__/_python_rsa.cpython-312.pyc"

    for workflow in workflows:
        content = workflow.read_text()
        assert content.count(trivy_image) == 1
        install = content.split("      - name: Install exact Trivy scanner\n", 1)[1].split("\n      - ", 1)[0]
        assert f"TRIVY_IMAGE: {trivy_image}" in install
        assert 'docker pull "$TRIVY_IMAGE"' in install
        assert 'container_id=$(docker create "$TRIVY_IMAGE")' in install
        assert 'docker cp "$container_id:/usr/local/bin/trivy" "$RUNNER_TEMP/trivy-bin/trivy"' in install
        assert 'test "$("$RUNNER_TEMP/trivy-bin/trivy" --version)" = "Version: 0.56.1"' in install
        assert 'printf \'%s\\n\' "$RUNNER_TEMP/trivy-bin" >> "$GITHUB_PATH"' in install
        assert content.count(action) == 1
        scan = content.split(action, 1)[1].split("\n      - ", 1)[0]
        settings = [line.strip() for line in scan.splitlines()]
        assert [line for line in settings if line.startswith("scanners:")] == ["scanners: vuln,secret"]
        assert [line for line in settings if line.startswith("skip-files:")] == [f"skip-files: {excluded_file}"]
        assert [line for line in settings if line.startswith("exit-code:")] == ['exit-code: "1"']
        assert [line for line in settings if line.startswith("severity:")] == ["severity: HIGH,CRITICAL"]
        assert [line for line in settings if line.startswith("ignore-unfixed:")] == ["ignore-unfixed: true"]
        assert [line for line in settings if line.startswith("skip-setup-trivy:")] == ["skip-setup-trivy: true"]
        assert not [line for line in settings if line.startswith("version:")]
        assert [line for line in settings if line.startswith("cache:")] == ['cache: "false"']


def test_gitea_image_gate_bootstraps_docker_and_runs_pinned_trivy() -> None:
    content = (ROOT / ".gitea" / "workflows" / "v1-tests.yml").read_text()
    container = content.split("\n  container:\n", 1)[1]
    excluded_file = "opt/venv/lib/python3.12/site-packages/google/auth/crypt/__pycache__/_python_rsa.cpython-312.pyc"
    trivy_image = "aquasec/trivy@sha256:c42bb3221509b0a9fa2291cd79a3a818b30a172ab87e9aac8a43997a5b56f293"

    assert 'DOCKER_BUILDKIT: "1"' in container
    assert "apt-get install -y --no-install-recommends docker.io docker-compose" in container
    assert "docker/setup-buildx-action@" not in container
    assert "aquasecurity/trivy-action@" not in container
    assert "docker-compose --profile maintenance --profile optional config -q" in container
    assert '-v "$PWD/ops/monitoring:/etc/prometheus:ro"' not in container
    assert "docker cp ops/monitoring/." in container

    scan = container.split("      - name: Scan image for high vulnerabilities\n", 1)[1]
    settings = [line.strip().removesuffix("\\").strip() for line in scan.splitlines()]
    assert [line for line in settings if line.startswith("aquasec/trivy@sha256:")] == [f"{trivy_image} image"]
    assert [line for line in settings if line.startswith("--scanners ")] == ["--scanners vuln,secret"]
    assert [line for line in settings if line.startswith("--skip-files ")] == [f"--skip-files {excluded_file}"]
    assert [line for line in settings if line.startswith("--exit-code ")] == ["--exit-code 1"]
    assert [line for line in settings if line.startswith("--severity ")] == ["--severity HIGH,CRITICAL"]
    assert [line for line in settings if line.startswith("--ignore-unfixed")] == ["--ignore-unfixed"]


def test_ci_image_contracts_supply_an_ephemeral_compose_env_file() -> None:
    workflows = (
        (ROOT / ".gitea" / "workflows" / "v1-tests.yml", "docker-compose"),
        (ROOT / ".github" / "workflows" / "ci.yml", "docker compose"),
    )

    for workflow, compose in workflows:
        container = workflow.read_text().split("\n  container:\n", 1)[1]
        cleanup = "trap 'rm -f .env' EXIT"
        create = "install -m 0600 /dev/null .env"
        validate = f"{compose} --profile maintenance --profile optional config -q"
        assert cleanup in container
        assert create in container
        assert container.index(cleanup) < container.index(create) < container.index(validate)


def test_github_diagnostic_artifacts_cannot_mask_quality_gate_results() -> None:
    ci = (ROOT / ".github" / "workflows" / "ci.yml").read_text()
    upload = "uses: actions/upload-artifact@ea165f8d65b6e75b540449e92b4886f43607fa02"

    assert ci.count(upload) == 2
    for block in ci.split(upload)[1:]:
        step = block.split("\n      - ", 1)[0]
        assert "continue-on-error: true" in step
        assert "if: always()" in step
        assert "retention-days: 1" in step


def test_github_image_cache_export_cannot_mask_the_build_result() -> None:
    ci = (ROOT / ".github" / "workflows" / "ci.yml").read_text()
    container = ci.split("\n  container:\n", 1)[1]

    assert "cache-from: type=gha" in container
    assert "cache-to: type=gha,mode=max,ignore-error=true" in container


def test_github_local_image_gate_does_not_request_unloadable_attestations() -> None:
    ci = (ROOT / ".github" / "workflows" / "ci.yml").read_text()
    container = ci.split("\n  container:\n", 1)[1]
    build = container.split("      - name: Build production image from locks\n", 1)[1].split("\n      - ", 1)[0]

    assert "load: true" in build
    assert "provenance: false" in build
    assert "sbom: false" in build

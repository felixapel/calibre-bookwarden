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

    assert compose.count("${BOOKAUDIT_IMAGE:-calibre-ai-auditor:1.2.0}") == 4
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
    for directory in (".state", ".artifacts", ".writer-artifacts", "user_library"):
        assert directory in script
    assert "docker compose" in script
    assert "config -q" in script


def test_retention_maintenance_service_is_explicit_and_fail_closed() -> None:
    compose = (ROOT / "docker-compose.yml").read_text()

    assert "retention:" in compose
    assert 'command: ["retention"]' in compose
    assert "./.writer-artifacts:/writer-artifacts" in compose
    assert "--backup-reference is required with --execute" in (ROOT / "src/calibre_ai_auditor/cli/main.py").read_text()


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

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

    assert compose.count("${BOOKAUDIT_IMAGE:-calibre-ai-auditor:1.2.0}") == 3
    assert "calibre-ai-auditor:v0.1" not in compose


def test_compose_requires_and_health_checks_the_single_writer() -> None:
    compose = (ROOT / "docker-compose.yml").read_text()

    assert "BOOKAUDIT_REQUIRE_WRITER_READY: ${BOOKAUDIT_REQUIRE_WRITER_READY:-true}" in compose
    assert '["CMD", "bookaudit", "writer-health"]' in compose

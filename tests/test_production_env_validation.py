import importlib.util
from pathlib import Path

SCRIPT = Path(__file__).resolve().parents[1] / "scripts" / "validate-production-env.py"
SPEC = importlib.util.spec_from_file_location("validate_production_env", SCRIPT)
assert SPEC
assert SPEC.loader
MODULE = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(MODULE)


def _valid_env(library: Path, backups: Path) -> str:
    return f"""\
UID=1000
GID=1000
COMPOSE_PROJECT_NAME=bookaudit-certificate-a
BOOKAUDIT_LIBRARY_HOST_PATH={library}
BOOKAUDIT_BACKUP_HOST_PATH={backups}
POSTGRES_PASSWORD=admin-password-abcdefghijklmnopqrstuvwxyz
POSTGRES_APP_PASSWORD=app-password-abcdefghijklmnopqrstuvwxyz
POSTGRES_VERIFIER_PASSWORD=verifier-password-abcdefghijklmnopqrstuvwxyz
POSTGRES_WRITER_PASSWORD=writer-password-abcdefghijklmnopqrstuvwxyz
POSTGRES_MIGRATOR_PASSWORD=migrator-password-abcdefghijklmnopqrstuvwxyz
BOOKAUDIT_APP_POSTGRES_DSN=postgresql+psycopg://bookaudit_app:app-password-abcdefghijklmnopqrstuvwxyz@postgres:5432/bookaudit
BOOKAUDIT_VERIFIER_POSTGRES_DSN=postgresql+psycopg://bookaudit_verifier:verifier-password-abcdefghijklmnopqrstuvwxyz@postgres:5432/bookaudit
BOOKAUDIT_WRITER_POSTGRES_DSN=postgresql+psycopg://bookaudit_writer:writer-password-abcdefghijklmnopqrstuvwxyz@postgres:5432/bookaudit
BOOKAUDIT_MIGRATOR_POSTGRES_DSN=postgresql+psycopg://bookaudit_migrator:migrator-password-abcdefghijklmnopqrstuvwxyz@postgres:5432/bookaudit
BOOKAUDIT_API_KEY=api-key-with-32-characters-and-entropy-9Z
BOOKAUDIT_TRUSTED_HOSTS=localhost,127.0.0.1,app,books.example.test
BOOKAUDIT_IMAGE=registry.example.test/bookaudit@sha256:{"a" * 64}
BOOKAUDIT_RELEASE_DIGEST=sha256:{"a" * 64}
"""


def test_legacy_compose_project_name_is_rejected(tmp_path: Path) -> None:
    library = tmp_path / "library"
    library.mkdir()
    backups = tmp_path / "backups"
    backups.mkdir()
    env = tmp_path / ".env"
    env.write_text(
        _valid_env(library, backups).replace(
            "COMPOSE_PROJECT_NAME=bookaudit-certificate-a",
            "COMPOSE_PROJECT_NAME=calibre-ai-auditor",
        )
    )
    env.chmod(0o600)

    assert MODULE.validate(env) == [
        "COMPOSE_PROJECT_NAME must be bookaudit-certificate-a to isolate Certificate A from legacy stacks"
    ]


def test_valid_production_environment_passes(tmp_path: Path) -> None:
    library = tmp_path / "library"
    library.mkdir()
    backups = tmp_path / "backups"
    backups.mkdir()
    env = tmp_path / ".env"
    env.write_text(_valid_env(library, backups))
    env.chmod(0o600)

    assert MODULE.validate(env) == []


def test_placeholders_shared_secrets_and_mutable_image_fail(tmp_path: Path) -> None:
    library = tmp_path / "library"
    library.mkdir()
    backups = tmp_path / "backups"
    backups.mkdir()
    env = tmp_path / ".env"
    content = _valid_env(library, backups)
    content = content.replace("registry.example.test/bookaudit@sha256:" + "a" * 64, "bookaudit:latest")
    content = content.replace("app-password-abcdefghijklmnopqrstuvwxyz", "replace-shared-password")
    content = content.replace("writer-password-abcdefghijklmnopqrstuvwxyz", "replace-shared-password")
    env.write_text(content)
    env.chmod(0o644)

    errors = MODULE.validate(env)
    assert any("placeholders" in error for error in errors)
    assert any("distinct" in error for error in errors)
    assert any("immutable" in error for error in errors)
    assert any("chmod 600" in error for error in errors)


def test_private_health_hosts_are_required(tmp_path: Path) -> None:
    library = tmp_path / "library"
    library.mkdir()
    backups = tmp_path / "backups"
    backups.mkdir()
    env = tmp_path / ".env"
    env.write_text(
        _valid_env(library, backups).replace(
            "localhost,127.0.0.1,app,books.example.test",
            "books.example.test",
        )
    )
    env.chmod(0o600)

    errors = MODULE.validate(env)

    assert errors == ["BOOKAUDIT_TRUSTED_HOSTS must include localhost, 127.0.0.1, and app for private health checks"]


def test_root_runtime_identity_is_rejected(tmp_path: Path) -> None:
    library = tmp_path / "library"
    library.mkdir()
    backups = tmp_path / "backups"
    backups.mkdir()
    env = tmp_path / ".env"
    env.write_text(_valid_env(library, backups).replace("UID=1000\nGID=1000", "UID=0\nGID=0"))
    env.chmod(0o600)

    assert MODULE.validate(env) == ["UID and GID must be explicit positive non-root integers"]


def test_api_key_cannot_reuse_a_database_password(tmp_path: Path) -> None:
    library = tmp_path / "library"
    library.mkdir()
    backups = tmp_path / "backups"
    backups.mkdir()
    env = tmp_path / ".env"
    env.write_text(
        _valid_env(library, backups).replace(
            "api-key-with-32-characters-and-entropy-9Z",
            "admin-password-abcdefghijklmnopqrstuvwxyz",
        )
    )
    env.chmod(0o600)

    assert MODULE.validate(env) == ["BOOKAUDIT_API_KEY must be distinct from every PostgreSQL password"]


def test_certificate_a_rejects_supervised_writer_pilot(tmp_path: Path) -> None:
    library = tmp_path / "library"
    library.mkdir()
    backups = tmp_path / "backups"
    backups.mkdir()
    env = tmp_path / ".env"
    content = _valid_env(library, backups)
    content += f"""\
BOOKAUDIT_REQUIRE_WRITER_READY=true
BOOKAUDIT_MANIFESTATION_V2__AUTO_APPLY__ENABLED=false
BOOKAUDIT_MANIFESTATION_V2__SUPERVISED_PILOT__ENABLED=true
BOOKAUDIT_MANIFESTATION_V2__SUPERVISED_PILOT__PILOT_ID=pilot-2026-07-14
BOOKAUDIT_MANIFESTATION_V2__SUPERVISED_PILOT__RELEASE_DIGEST=sha256:{"a" * 64}
BOOKAUDIT_MANIFESTATION_V2__SUPERVISED_PILOT__MAX_OPERATIONS=5
"""
    env.write_text(content)
    env.chmod(0o600)

    errors = MODULE.validate(env)

    assert errors == ["Certificate A does not permit the supervised writer pilot"]


def test_supervised_pilot_rejects_unsafe_or_mismatched_binding(tmp_path: Path) -> None:
    library = tmp_path / "library"
    library.mkdir()
    backups = tmp_path / "backups"
    backups.mkdir()
    env = tmp_path / ".env"
    content = _valid_env(library, backups)
    content += f"""\
BOOKAUDIT_REQUIRE_WRITER_READY=false
BOOKAUDIT_MANIFESTATION_V2__AUTO_APPLY__ENABLED=true
BOOKAUDIT_MANIFESTATION_V2__SUPERVISED_PILOT__ENABLED=true
BOOKAUDIT_MANIFESTATION_V2__SUPERVISED_PILOT__PILOT_ID=
BOOKAUDIT_MANIFESTATION_V2__SUPERVISED_PILOT__RELEASE_DIGEST=sha256:{"b" * 64}
BOOKAUDIT_MANIFESTATION_V2__SUPERVISED_PILOT__MAX_OPERATIONS=6
"""
    env.write_text(content)
    env.chmod(0o600)

    errors = MODULE.validate(env)

    assert any("auto-apply" in error for error in errors)
    assert any("does not permit" in error for error in errors)

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
BOOKAUDIT_LIBRARY_HOST_PATH={library}
BOOKAUDIT_BACKUP_HOST_PATH={backups}
POSTGRES_PASSWORD=admin-password-abcdefghijklmnopqrstuvwxyz
POSTGRES_APP_PASSWORD=app-password-abcdefghijklmnopqrstuvwxyz
POSTGRES_WRITER_PASSWORD=writer-password-abcdefghijklmnopqrstuvwxyz
POSTGRES_MIGRATOR_PASSWORD=migrator-password-abcdefghijklmnopqrstuvwxyz
BOOKAUDIT_APP_POSTGRES_DSN=postgresql+psycopg://bookaudit_app:app-password-abcdefghijklmnopqrstuvwxyz@postgres:5432/bookaudit
BOOKAUDIT_WRITER_POSTGRES_DSN=postgresql+psycopg://bookaudit_writer:writer-password-abcdefghijklmnopqrstuvwxyz@postgres:5432/bookaudit
BOOKAUDIT_MIGRATOR_POSTGRES_DSN=postgresql+psycopg://bookaudit_migrator:migrator-password-abcdefghijklmnopqrstuvwxyz@postgres:5432/bookaudit
BOOKAUDIT_API_KEY=api-key-with-32-characters-and-entropy-9Z
BOOKAUDIT_TRUSTED_HOSTS=books.example.test
BOOKAUDIT_IMAGE=registry.example.test/bookaudit@sha256:{"a" * 64}
"""


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

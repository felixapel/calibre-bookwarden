import json
from pathlib import Path

import pytest
import yaml

import scripts.disposable_calibre_lab as disposable_lab
from scripts.calibredb_readonly_wrapper import allowed_operation
from scripts.disposable_calibre_fixture import (
    FIXTURE_MANIFEST,
    FixtureError,
    _is_authorization_denial,
    _new_password,
    _require_success,
    synthetic_isbn,
)
from scripts.disposable_calibre_lab import (
    LabError,
    validate_attestation,
    validate_audit,
    validate_inventory,
    validate_rendered_compose,
)

ROOT = Path(__file__).resolve().parents[1]


def _safe_compose() -> dict[str, object]:
    project = "bookaudit-lab-012345abcdef"
    document: dict[str, object] = {
        "name": project,
        "services": {
            "fixture-builder": {
                "network_mode": "none",
                "volumes": [
                    {"type": "volume", "source": "lab-library", "target": "/lab-library"},
                    {"type": "volume", "source": "lab-auth", "target": "/lab-auth"},
                    {"type": "volume", "source": "lab-state", "target": "/state"},
                    {"type": "volume", "source": "lab-credentials", "target": "/credentials"},
                ],
            },
            "calibre-server": {
                "networks": ["offline"],
                "volumes": [
                    {
                        "type": "volume",
                        "source": "lab-library",
                        "target": "/library",
                        "read_only": False,
                    },
                    {"type": "volume", "source": "lab-auth", "target": "/auth", "read_only": True},
                ],
            },
            "audit-client": {
                "network_mode": "service:calibre-server",
                "volumes": [
                    {"type": "volume", "source": "lab-state", "target": "/state"},
                    {
                        "type": "volume",
                        "source": "lab-credentials",
                        "target": "/credentials",
                        "read_only": True,
                    },
                ],
            },
            "attestor": {
                "network_mode": "none",
                "volumes": [
                    {
                        "type": "volume",
                        "source": "lab-library",
                        "target": "/library",
                        "read_only": True,
                    },
                    {"type": "volume", "source": "lab-state", "target": "/state", "read_only": True},
                ],
            },
            "web-smoke": {"profiles": ["web"], "networks": ["web"], "volumes": []},
        },
        "networks": {"offline": {"internal": True}, "web": {"internal": False}},
        "volumes": {
            name: {"name": f"{project}_{name}"} for name in ("lab-library", "lab-auth", "lab-state", "lab-credentials")
        },
    }
    services = document["services"]
    assert isinstance(services, dict)
    for service in services.values():
        assert isinstance(service, dict)
        service.update(
            {
                "user": "10001:10001",
                "read_only": True,
                "cap_drop": ["ALL"],
                "security_opt": ["no-new-privileges:true"],
                "tmpfs": ["/tmp"],
            }
        )
    return document


def test_compose_contract_is_isolated_from_host_and_live_library() -> None:
    rendered = yaml.safe_dump(_safe_compose())

    validate_rendered_compose(rendered)


@pytest.mark.parametrize(
    ("service", "unsafe_change"),
    [
        ("calibre-server", {"ports": ["8086:8086"]}),
        ("calibre-server", {"network_mode": "host"}),
        (
            "calibre-server",
            {"volumes": [{"type": "bind", "source": "/mnt/books", "target": "/library"}]},
        ),
        ("audit-client", {"volumes": [{"type": "bind", "source": "/home/felix", "target": "/host"}]}),
        ("fixture-builder", {"privileged": True}),
        ("fixture-builder", {"volumes_from": ["host-container"]}),
        ("audit-client", {"configs": [{"source": "host-config", "target": "/host"}]}),
    ],
)
def test_compose_contract_rejects_host_and_privilege_escape(service: str, unsafe_change: dict[str, object]) -> None:
    document = _safe_compose()
    services = document["services"]
    assert isinstance(services, dict)
    target = services[service]
    assert isinstance(target, dict)
    target.update(unsafe_change)

    with pytest.raises(LabError):
        validate_rendered_compose(yaml.safe_dump(document))


@pytest.mark.parametrize("control", ["user", "read_only", "cap_drop", "security_opt", "tmpfs"])
def test_compose_contract_requires_every_hardening_control(control: str) -> None:
    document = _safe_compose()
    services = document["services"]
    assert isinstance(services, dict)
    builder = services["fixture-builder"]
    assert isinstance(builder, dict)
    builder.pop(control)

    with pytest.raises(LabError):
        validate_rendered_compose(yaml.safe_dump(document))


def test_compose_contract_rejects_external_or_undeclared_volumes() -> None:
    external = _safe_compose()
    volumes = external["volumes"]
    assert isinstance(volumes, dict)
    volumes["lab-library"] = {"external": True}
    with pytest.raises(LabError, match="external"):
        validate_rendered_compose(yaml.safe_dump(external))

    undeclared = _safe_compose()
    services = undeclared["services"]
    assert isinstance(services, dict)
    builder = services["fixture-builder"]
    assert isinstance(builder, dict)
    builder["volumes"][0]["source"] = "some-existing-volume"
    with pytest.raises(LabError, match="undeclared"):
        validate_rendered_compose(yaml.safe_dump(undeclared))


def test_compose_contract_rejects_live_host_marker_even_outside_mounts() -> None:
    document = _safe_compose()
    services = document["services"]
    assert isinstance(services, dict)
    server = services["calibre-server"]
    assert isinstance(server, dict)
    server["environment"] = {"TARGET": "192.168.0.122"}

    with pytest.raises(LabError, match="forbidden live-resource marker"):
        validate_rendered_compose(yaml.safe_dump(document))


def test_inventory_validator_accepts_only_expected_aggregate_contract() -> None:
    report = {
        "schema_version": 1,
        "captured_at": "2026-07-16T00:00:00Z",
        "source_fingerprint": "a" * 64,
        "manifest_sha256": "b" * 64,
        "counts": {"books": 5, "formats": 5, "multi_format": 1, "no_format": 1},
        "formats": {"AZW3": 1, "EPUB": 3, "PDF": 1},
        "languages": {"ENG": 3, "FRA": 1},
        "isbn": {"invalid": 0, "missing": 2, "valid": 3},
        "incomplete_fields": {"authors": 0, "languages": 1, "pubdate": 0, "publisher": 5, "title": 0},
        "last_modified": {
            "minimum": "2026-07-16T00:00:00Z",
            "maximum": "2026-07-16T00:01:00Z",
            "missing": 0,
            "invalid": 0,
        },
    }

    validate_inventory(report)

    report["counts"]["title"] = 1
    with pytest.raises(LabError, match="aggregate-only schema"):
        validate_inventory(report)


def test_audit_validator_requires_exact_distinct_fixture_references() -> None:
    fingerprint = "a" * 64
    report = {
        "run_id": "remote_verify_20260716_120000_0123abcd",
        "status": "completed",
        "verdicts": [
            {
                "book_key": f"calibre-server:{fingerprint}:{book_id}",
                "state": "review",
                "tier": "B",
                "risk_flags": ["fixture-review"],
            }
            for book_id in range(1, 6)
        ],
    }

    assert validate_audit(report, expected_fingerprint=fingerprint) == {"review": 5}

    report["verdicts"][4]["book_key"] = report["verdicts"][0]["book_key"]
    with pytest.raises(LabError, match="distinct"):
        validate_audit(report, expected_fingerprint=fingerprint)


def test_audit_validator_rejects_failed_or_source_changed_fixture() -> None:
    fingerprint = "a" * 64
    report = {
        "run_id": "remote_verify_20260716_120000_0123abcd",
        "status": "completed",
        "verdicts": [
            {
                "book_key": f"calibre-server:{fingerprint}:{book_id}",
                "state": "review",
                "tier": "B",
                "risk_flags": ["fixture-review"],
            }
            for book_id in range(1, 6)
        ],
    }
    for unsafe_state in ("failed", "source_changed"):
        report["verdicts"][0]["state"] = unsafe_state
        with pytest.raises(LabError, match="unexpected fixture verdict"):
            validate_audit(report, expected_fingerprint=fingerprint)


def test_attestation_requires_identical_library_and_read_only_commands() -> None:
    report = {
        "identical": True,
        "baseline_sha256": "c" * 64,
        "current_sha256": "c" * 64,
        "operations": ["list", "export"],
        "scratch_empty": True,
        "credential_files": {"password_mode": "0600"},
    }

    validate_attestation(report)

    report["operations"] = ["list", "set_metadata"]
    with pytest.raises(LabError, match="forbidden calibredb operation"):
        validate_attestation(report)


def test_repository_contract_uses_a_dedicated_lab_compose_and_pinned_calibre() -> None:
    compose = (ROOT / "compose.disposable-calibre.yml").read_text()
    dockerfile = (ROOT / "Dockerfile").read_text()

    assert "docker-compose.yml" not in compose
    assert "env_file:" not in compose
    assert "ports:" not in compose
    assert "internal: true" in compose
    assert "--disable-local-write" in compose
    assert "192.168.0.122" not in compose
    assert "ARG CALIBRE_VERSION=9.11.0" in dockerfile
    assert "calibre-${CALIBRE_VERSION}-x86_64.txz" in dockerfile
    assert (
        "ARG CALIBRE_X86_64_SHA512="
        "4b2250124e73b907dc84f30d413e095193735ffe3f933793a7d021885efbb37b2"
        "a92254e36c17e0a52c555729a7cd67c5229a1b62b9968baf61764463aeea47e"
    ) in dockerfile
    assert "sha512sum --check --strict" in dockerfile
    assert "      calibre \\\n" not in dockerfile
    assert "PATH=/opt/venv/bin:/usr/local/bin:$PATH" in dockerfile
    assert "ENV PATH=/opt/calibre:$PATH" in dockerfile
    assert "      libglx0 \\\n" in dockerfile
    assert "      libxkbcommon0 \\\n" in dockerfile


def test_runner_report_is_json_serializable_without_private_metadata() -> None:
    payload = {
        "run_id": "bookaudit-lab-abc123",
        "status": "passed",
        "calibre_version": "9.11.0",
        "inventory": {"books": 5, "formats": 5},
        "audit": {"processed": 5, "states": {"review": 4, "failed": 1}},
        "acl_write_rejected": True,
        "library_identical": True,
        "cleanup": "completed",
    }

    encoded = json.dumps(payload, sort_keys=True)
    for forbidden in ("password", "/tmp/", "/home/", "title", "author"):
        assert forbidden not in encoded.lower()


@pytest.mark.parametrize("operation", ["--version", "list", "export"])
def test_read_only_calibredb_wrapper_allows_only_auditor_operations(operation: str) -> None:
    assert allowed_operation([operation]) is True


@pytest.mark.parametrize("operation", ["set_metadata", "add", "remove", "add_format", "restore_database"])
def test_read_only_calibredb_wrapper_rejects_mutators(operation: str) -> None:
    assert allowed_operation([operation]) is False


def test_fixture_manifest_is_deterministic_and_covers_remote_edge_cases() -> None:
    assert [item["kind"] for item in FIXTURE_MANIFEST] == [
        "correct_epub",
        "mismatched_epub",
        "multi_format",
        "no_format",
        "image_only_pdf",
    ]
    assert [synthetic_isbn(index) for index in range(3)] == [
        "9780000000002",
        "9780000000019",
        "9780000000026",
    ]


def test_fixture_command_failure_diagnostic_does_not_expose_arguments(monkeypatch: pytest.MonkeyPatch) -> None:
    class Failed:
        returncode = 9

    monkeypatch.setattr("scripts.disposable_calibre_fixture._run", lambda *_args, **_kwargs: Failed())

    with pytest.raises(FixtureError, match=r"calibredb add failed with exit code 9") as caught:
        _require_success(["/opt/calibre/calibredb", "add", "/secret/book.epub", "--password", "secret"])

    assert "secret" not in str(caught.value)
    assert "/secret/book.epub" not in str(caught.value)


def test_generated_password_cannot_be_parsed_as_a_manage_users_option(monkeypatch: pytest.MonkeyPatch) -> None:
    monkeypatch.setattr("scripts.disposable_calibre_fixture.secrets.token_urlsafe", lambda _size: "-unsafe")

    assert _new_password() == "L-unsafe"


@pytest.mark.parametrize(
    "message",
    [
        "Forbidden",
        "Enter the password:\nForbidden",
        "You are not allowed to make changes to this library",
        "The read-only user cannot modify metadata",
        "Permission denied while changing the library",
    ],
)
def test_acl_probe_accepts_only_specific_authorization_denials(message: str) -> None:
    assert _is_authorization_denial(message) is True


@pytest.mark.parametrize(
    "message",
    [
        "connection refused",
        "Not Found",
        "incorrect password",
        "usage: calibredb set_metadata",
        "permission denied: incorrect password",
    ],
)
def test_acl_probe_rejects_unrelated_command_failures(message: str) -> None:
    assert _is_authorization_denial(message) is False


def test_cleanup_includes_the_optional_web_profile(monkeypatch: pytest.MonkeyPatch) -> None:
    commands: list[list[str]] = []

    def record(command, **_kwargs):
        commands.append(list(command))
        return ""

    monkeypatch.setattr(disposable_lab, "_run_command", record)

    disposable_lab._cleanup("bookaudit-lab-012345abcdef", {})

    assert commands[0][-9:] == [
        "--profile",
        "web",
        "down",
        "--volumes",
        "--remove-orphans",
        "--rmi",
        "all",
        "--timeout",
        "5",
    ]
    assert len(commands) == 5


def test_local_docker_gate_rejects_endpoint_overrides_before_any_command(monkeypatch: pytest.MonkeyPatch) -> None:
    called = False

    def unexpected(*_args, **_kwargs):
        nonlocal called
        called = True
        return ""

    monkeypatch.setattr(disposable_lab, "_run_command", unexpected)

    with pytest.raises(LabError, match="overrides"):
        disposable_lab._assert_local_docker({"DOCKER_HOST": "ssh://remote.example"})

    assert called is False


def test_local_docker_gate_pins_the_verified_unix_context(monkeypatch: pytest.MonkeyPatch) -> None:
    responses = iter(
        [
            "desktop-linux\n",
            json.dumps([{"Endpoints": {"docker": {"Host": "unix:///run/user/1000/docker.sock"}}}]),
        ]
    )
    monkeypatch.setattr(disposable_lab, "_run_command", lambda *_args, **_kwargs: next(responses))
    environment: dict[str, str] = {}

    disposable_lab._assert_local_docker(environment)

    assert environment["DOCKER_CONTEXT"] == "desktop-linux"

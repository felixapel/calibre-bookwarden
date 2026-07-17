#!/usr/bin/env python3
"""Run the disposable, offline Calibre Content Server safety gate."""

from __future__ import annotations

import argparse
import json
import os
import platform
import re
import secrets
import stat
import subprocess
import sys
from collections.abc import Mapping, Sequence
from pathlib import Path
from typing import Any

import yaml
from pydantic import ValidationError

from calibre_ai_auditor.calibre.content_server import InventoryReport

ROOT = Path(__file__).resolve().parents[1]
COMPOSE_FILE = ROOT / "compose.disposable-calibre.yml"
REPORT_ROOT = ROOT / "reports" / "disposable-calibre"
RUN_ID_PATTERN = re.compile(r"^bookaudit-lab-[0-9a-f]{12}$")
EXPECTED_SERVICES = {"fixture-builder", "calibre-server", "audit-client", "attestor", "web-smoke"}
EXPECTED_COUNTS = {"books": 5, "formats": 5, "multi_format": 1, "no_format": 1}
EXPECTED_FORMATS = {"AZW3": 1, "EPUB": 3, "PDF": 1}
EXPECTED_LANGUAGES = {"ENG": 3, "FRA": 1}
EXPECTED_ISBN = {"invalid": 0, "missing": 2, "valid": 3}
EXPECTED_INCOMPLETE_FIELDS = {"authors": 0, "languages": 1, "pubdate": 0, "publisher": 5, "title": 0}
FORBIDDEN_MARKERS = (
    "192.168.0.122",
    "/mnt/",
    "/media/",
    "/.ssh",
    "host.docker.internal",
    "/var/run/docker.sock",
)
DOCKER_OVERRIDE_KEYS = ("DOCKER_HOST", "DOCKER_CONTEXT", "DOCKER_TLS_VERIFY", "DOCKER_CERT_PATH")


class LabError(RuntimeError):
    """The safety gate failed closed."""


def _mapping(value: object, *, label: str) -> Mapping[str, Any]:
    if not isinstance(value, Mapping):
        raise LabError(f"{label} must be a mapping")
    return value


def _service_networks(service: Mapping[str, Any]) -> set[str]:
    raw = service.get("networks", {})
    if isinstance(raw, Mapping):
        return {str(name) for name in raw}
    if isinstance(raw, list):
        return {str(name) for name in raw}
    if raw in (None, []):
        return set()
    raise LabError("service networks must be a mapping or list")


def _service_volumes(service: Mapping[str, Any], *, name: str, allowed_sources: set[str]) -> dict[str, bool]:
    result: dict[str, bool] = {}
    raw_volumes = service.get("volumes", [])
    if not isinstance(raw_volumes, list):
        raise LabError(f"{name} volumes must be a list")
    for raw in raw_volumes:
        if not isinstance(raw, Mapping) or raw.get("type") != "volume":
            raise LabError(f"{name} may use named volumes only")
        source = raw.get("source")
        target = raw.get("target")
        if not isinstance(source, str) or not source or not isinstance(target, str) or not target.startswith("/"):
            raise LabError(f"{name} contains an invalid volume")
        if source not in allowed_sources:
            raise LabError(f"{name} references an undeclared volume")
        if target in result:
            raise LabError(f"{name} contains a duplicate volume target")
        result[target] = bool(raw.get("read_only", False))
    return result


def validate_rendered_compose(rendered: str) -> None:
    """Reject any Compose topology capable of reaching host or live-library resources."""
    try:
        document = yaml.safe_load(rendered)
    except yaml.YAMLError as exc:
        raise LabError("rendered Compose is invalid") from exc
    root = _mapping(document, label="Compose document")
    project_name = root.get("name")
    if not isinstance(project_name, str) or not RUN_ID_PATTERN.fullmatch(project_name):
        raise LabError("Compose project name is not a disposable run id")
    services = _mapping(root.get("services"), label="Compose services")
    if set(services) != EXPECTED_SERVICES:
        raise LabError("Compose service set is not the disposable lab contract")

    lowered = rendered.lower()
    if any(marker.lower() in lowered for marker in FORBIDDEN_MARKERS):
        raise LabError("rendered Compose contains a forbidden live-resource marker")

    networks = _mapping(root.get("networks"), label="Compose networks")
    offline = _mapping(networks.get("offline"), label="offline network")
    web = _mapping(networks.get("web"), label="web network")
    if offline.get("internal") is not True or web.get("internal") is True:
        raise LabError("Compose networks do not preserve offline/web separation")

    volumes = _mapping(root.get("volumes"), label="Compose volumes")
    if set(volumes) != {"lab-library", "lab-auth", "lab-state", "lab-credentials"}:
        raise LabError("Compose volume set is not the disposable lab contract")
    declared_volume_names: set[str] = set(volumes)
    for key, raw_volume in volumes.items():
        volume = _mapping(raw_volume, label=f"volume {key}")
        if volume.get("external") is True:
            raise LabError("Compose cannot use external volumes")
        actual_name = volume.get("name")
        if actual_name != f"{project_name}_{key}":
            raise LabError("Compose volume name is outside the disposable project")
        declared_volume_names.add(actual_name)

    expected_volumes = {
        "fixture-builder": {"/lab-library": False, "/lab-auth": False, "/state": False, "/credentials": False},
        # Calibre probes case sensitivity with a create/unlink cycle when it
        # opens a library. This is safe only because the volume is generated,
        # disposable, API writes are disabled, and the full tree is attested.
        "calibre-server": {"/library": False, "/auth": True},
        "audit-client": {"/state": False, "/credentials": True},
        "attestor": {"/library": True, "/state": True},
        "web-smoke": {},
    }
    expected_network_modes = {
        "fixture-builder": "none",
        "audit-client": "service:calibre-server",
        "attestor": "none",
    }
    expected_networks = {"calibre-server": {"offline"}, "web-smoke": {"web"}}

    for name, raw_service in services.items():
        service = _mapping(raw_service, label=f"service {name}")
        for forbidden_key in (
            "ports",
            "env_file",
            "devices",
            "cap_add",
            "secrets",
            "configs",
            "volumes_from",
            "links",
            "external_links",
        ):
            if service.get(forbidden_key):
                raise LabError(f"{name} contains forbidden {forbidden_key}")
        if service.get("privileged") is True:
            raise LabError(f"{name} cannot be privileged")
        if service.get("user") != "10001:10001":
            raise LabError(f"{name} must run as the unprivileged lab user")
        if service.get("read_only") is not True:
            raise LabError(f"{name} root filesystem must be read-only")
        if service.get("cap_drop") != ["ALL"]:
            raise LabError(f"{name} must drop all capabilities")
        if service.get("security_opt") != ["no-new-privileges:true"]:
            raise LabError(f"{name} must disable privilege escalation")
        tmpfs = service.get("tmpfs")
        tmpfs_targets = set(tmpfs) if isinstance(tmpfs, Mapping) else set(tmpfs or [])
        if not any(str(target).split(":", 1)[0] == "/tmp" for target in tmpfs_targets):
            raise LabError(f"{name} must use an isolated /tmp tmpfs")
        for namespace in ("pid", "ipc", "uts"):
            if str(service.get(namespace, "")).lower() == "host":
                raise LabError(f"{name} cannot join the host {namespace} namespace")
        mode = service.get("network_mode")
        if name in expected_network_modes:
            if mode != expected_network_modes[name]:
                raise LabError(f"{name} has an unexpected network mode")
        elif mode is not None:
            raise LabError(f"{name} cannot set network_mode")
        if name in expected_networks and _service_networks(service) != expected_networks[name]:
            raise LabError(f"{name} is attached to an unexpected network")
        if _service_volumes(service, name=name, allowed_sources=declared_volume_names) != expected_volumes[name]:
            raise LabError(f"{name} volume contract is unsafe")

    web_service = _mapping(services["web-smoke"], label="web-smoke service")
    if web_service.get("profiles") != ["web"]:
        raise LabError("web-smoke must remain explicitly opt-in")


def validate_inventory(report: Mapping[str, Any]) -> None:
    try:
        inventory = InventoryReport.model_validate(report)
    except ValidationError:
        raise LabError("inventory does not match the aggregate-only schema") from None
    payload = inventory.model_dump(mode="json")
    if payload["counts"] != EXPECTED_COUNTS:
        raise LabError("inventory counts do not match the fixture manifest")
    if payload["formats"] != EXPECTED_FORMATS or payload["languages"] != EXPECTED_LANGUAGES:
        raise LabError("inventory distributions do not match the fixture manifest")
    if payload["isbn"] != EXPECTED_ISBN or payload["incomplete_fields"] != EXPECTED_INCOMPLETE_FIELDS:
        raise LabError("inventory quality counts do not match the fixture manifest")
    modified = payload["last_modified"]
    if modified["missing"] != 0 or modified["invalid"] != 0 or not modified["minimum"] or not modified["maximum"]:
        raise LabError("inventory modification range is incomplete")


def validate_audit(report: Mapping[str, Any], *, expected_fingerprint: str) -> dict[str, int]:
    if set(report) != {"run_id", "status", "verdicts"} or report.get("status") != "completed":
        raise LabError("remote audit did not complete")
    run_id = report.get("run_id")
    if not isinstance(run_id, str) or not re.fullmatch(r"remote_verify_[0-9]{8}_[0-9]{6}_[0-9a-f]{8}", run_id):
        raise LabError("remote audit returned an invalid run id")
    verdicts = report.get("verdicts")
    if not isinstance(verdicts, list) or len(verdicts) != EXPECTED_COUNTS["books"]:
        raise LabError("remote audit did not return one verdict per fixture")
    seen_ids: set[int] = set()
    for verdict in verdicts:
        item = _mapping(verdict, label="audit verdict")
        if set(item) != {"book_key", "state", "tier", "risk_flags"}:
            raise LabError("audit verdict does not match the expected schema")
        book_key = item.get("book_key")
        match = (
            re.fullmatch(r"calibre-server:([0-9a-f]{64}):([1-9][0-9]*)", book_key)
            if isinstance(book_key, str)
            else None
        )
        if match is None or match.group(1) != expected_fingerprint:
            raise LabError("audit exposed an invalid book reference")
        seen_ids.add(int(match.group(2)))
        flags = item.get("risk_flags")
        if (
            item.get("state") != "review"
            or item.get("tier") != "B"
            or not isinstance(flags, list)
            or not flags
            or not all(isinstance(flag, str) and flag for flag in flags)
        ):
            raise LabError("audit returned an unexpected fixture verdict")
    if seen_ids != set(range(1, EXPECTED_COUNTS["books"] + 1)):
        raise LabError("audit did not return five distinct fixture ids")
    return {"review": EXPECTED_COUNTS["books"]}


def validate_attestation(report: Mapping[str, Any]) -> None:
    if report.get("identical") is not True or report.get("scratch_empty") is not True:
        raise LabError("library or scratch attestation failed")
    baseline = report.get("baseline_sha256")
    current = report.get("current_sha256")
    if not isinstance(baseline, str) or not re.fullmatch(r"[0-9a-f]{64}", baseline) or current != baseline:
        raise LabError("library digest changed during the read-only gate")
    operations = report.get("operations")
    if not isinstance(operations, list):
        raise LabError("required calibredb reads were not observed")
    if any(operation not in {"--version", "list", "export"} for operation in operations):
        raise LabError("forbidden calibredb operation was observed")
    if not {"list", "export"}.issubset(set(operations)):
        raise LabError("required calibredb reads were not observed")
    credentials = _mapping(report.get("credential_files"), label="credential attestation")
    if credentials.get("password_mode") != "0600":
        raise LabError("credential file mode was not private")


def _load_json(raw: str, *, label: str) -> Mapping[str, Any]:
    try:
        parsed = json.loads(raw)
    except json.JSONDecodeError as exc:
        raise LabError(f"{label} did not return valid JSON") from exc
    return _mapping(parsed, label=label)


def _run_command(
    command: Sequence[str],
    *,
    env: Mapping[str, str] | None = None,
    timeout: float = 1200,
    label: str,
) -> str:
    try:
        result = subprocess.run(
            list(command),
            cwd=ROOT,
            env=dict(env) if env is not None else None,
            text=True,
            capture_output=True,
            check=False,
            timeout=timeout,
        )
    except (OSError, subprocess.TimeoutExpired) as exc:
        raise LabError(f"{label} could not be executed") from exc
    if result.returncode != 0:
        raise LabError(f"{label} failed with exit code {result.returncode}")
    return result.stdout


def _compose(run_id: str, *arguments: str) -> list[str]:
    return [
        "docker",
        "compose",
        "--env-file",
        "/dev/null",
        "--project-name",
        run_id,
        "--file",
        str(COMPOSE_FILE),
        *arguments,
    ]


def _run_environment(run_id: str) -> dict[str, str]:
    environment = dict(os.environ)
    environment["LAB_RUN_ID"] = run_id
    return environment


def _assert_local_docker(env: dict[str, str]) -> None:
    if platform.machine().lower() not in {"x86_64", "amd64"}:
        raise LabError("the pinned Calibre lab supports x86_64 only")
    if any(env.get(key) for key in DOCKER_OVERRIDE_KEYS):
        raise LabError("Docker endpoint overrides are forbidden for the disposable lab")
    context = _run_command(["docker", "context", "show"], env=env, label="Docker context selection").strip()
    if not context or any(character.isspace() for character in context):
        raise LabError("Docker context could not be verified")
    raw = _run_command(
        ["docker", "context", "inspect", context],
        env=env,
        label="Docker context inspection",
    )
    try:
        contexts = json.loads(raw)
        endpoint = contexts[0]["Endpoints"]["docker"]["Host"]
    except (IndexError, KeyError, TypeError, json.JSONDecodeError) as exc:
        raise LabError("Docker context could not be verified") from exc
    if not isinstance(endpoint, str) or not endpoint.startswith("unix://"):
        raise LabError("the disposable lab requires a local Unix-socket Docker engine")
    # Bind every subsequent command, including cleanup, to the exact context
    # whose local endpoint was inspected above.
    env["DOCKER_CONTEXT"] = context


def _write_report(run_id: str, payload: Mapping[str, Any]) -> Path:
    REPORT_ROOT.mkdir(parents=True, exist_ok=True, mode=0o700)
    os.chmod(REPORT_ROOT, 0o700, follow_symlinks=False)
    target = REPORT_ROOT / f"{run_id}.json"
    encoded = (json.dumps(payload, indent=2, sort_keys=True) + "\n").encode()
    descriptor = os.open(target, os.O_WRONLY | os.O_CREAT | os.O_TRUNC | os.O_NOFOLLOW, 0o600)
    try:
        os.write(descriptor, encoded)
        os.fsync(descriptor)
    finally:
        os.close(descriptor)
    if stat.S_IMODE(target.stat(follow_symlinks=False).st_mode) != 0o600:
        raise LabError("lab report permissions are unsafe")
    return target


def _cleanup(run_id: str, env: Mapping[str, str]) -> None:
    _run_command(
        _compose(
            run_id,
            "--profile",
            "web",
            "down",
            "--volumes",
            "--remove-orphans",
            "--rmi",
            "all",
            "--timeout",
            "5",
        ),
        env=env,
        timeout=180,
        label="disposable lab cleanup",
    )
    residue_queries = (
        [
            "docker",
            "container",
            "ls",
            "--all",
            "--filter",
            f"label=com.docker.compose.project={run_id}",
            "--format",
            "{{.ID}}",
        ],
        [
            "docker",
            "volume",
            "ls",
            "--filter",
            f"label=com.docker.compose.project={run_id}",
            "--format",
            "{{.Name}}",
        ],
        [
            "docker",
            "network",
            "ls",
            "--filter",
            f"label=com.docker.compose.project={run_id}",
            "--format",
            "{{.Name}}",
        ],
        [
            "docker",
            "image",
            "ls",
            "--filter",
            f"reference=calibre-ai-auditor-lab:{run_id}",
            "--format",
            "{{.ID}}",
        ],
    )
    for command in residue_queries:
        if _run_command(command, env=env, timeout=30, label="cleanup residue inspection").strip():
            raise LabError("disposable lab cleanup left run-scoped Docker resources")


def _new_run_id() -> str:
    return f"bookaudit-lab-{secrets.token_hex(6)}"


def run_lab(*, keep: bool, with_web_smoke: bool) -> Path:
    run_id = _new_run_id()
    env = _run_environment(run_id)
    report: dict[str, Any] = {
        "run_id": run_id,
        "status": "failed",
        "calibre_version": "9.11.0",
        "cleanup": "pending",
    }
    failure: LabError | None = None
    docker_validated = False
    try:
        _assert_local_docker(env)
        docker_validated = True
        rendered = _run_command(
            _compose(run_id, "--profile", "web", "config"),
            env=env,
            label="Compose rendering",
        )
        validate_rendered_compose(rendered)
        _run_command(_compose(run_id, "build"), env=env, timeout=1800, label="lab image build")
        _run_command(
            _compose(run_id, "run", "--rm", "--no-deps", "fixture-builder"),
            env=env,
            timeout=600,
            label="fixture construction",
        )
        _run_command(
            _compose(run_id, "up", "--detach", "--wait", "calibre-server"),
            env=env,
            timeout=180,
            label="Calibre server startup",
        )
        version = _run_command(
            _compose(run_id, "run", "--rm", "--no-deps", "audit-client", "calibredb-version"),
            env=env,
            label="Calibre client version check",
        ).strip()
        if version != "9.11.0":
            raise LabError("Calibre client version does not match the pinned server version")
        _run_command(
            _compose(run_id, "run", "--rm", "--no-deps", "audit-client", "migrate"),
            env=env,
            label="disposable schema migration",
        )
        inventory = _load_json(
            _run_command(
                _compose(run_id, "run", "--rm", "--no-deps", "audit-client", "inventory"),
                env=env,
                timeout=300,
                label="aggregate inventory",
            ),
            label="aggregate inventory",
        )
        validate_inventory(inventory)
        audit = _load_json(
            _run_command(
                _compose(run_id, "run", "--rm", "--no-deps", "audit-client", "verify"),
                env=env,
                timeout=1200,
                label="remote shadow audit",
            ),
            label="remote shadow audit",
        )
        state_counts = validate_audit(audit, expected_fingerprint=str(inventory["source_fingerprint"]))
        acl = _load_json(
            _run_command(
                _compose(run_id, "run", "--rm", "--no-deps", "audit-client", "acl-probe"),
                env=env,
                label="read-only ACL probe",
            ),
            label="read-only ACL probe",
        )
        if set(acl) != {"control_succeeded", "write_rejected"} or acl.get("control_succeeded") is not True:
            raise LabError("Calibre ACL control operation did not succeed")
        if acl.get("write_rejected") is not True:
            raise LabError("Calibre accepted a forbidden metadata write")
        attestation = _load_json(
            _run_command(
                _compose(run_id, "run", "--rm", "--no-deps", "attestor"),
                env=env,
                label="library attestation",
            ),
            label="library attestation",
        )
        validate_attestation(attestation)
        report.update(
            {
                "status": "passed",
                "inventory": dict(EXPECTED_COUNTS),
                "audit": {"processed": EXPECTED_COUNTS["books"], "states": state_counts},
                "acl_write_rejected": True,
                "library_identical": True,
            }
        )
        if with_web_smoke:
            try:
                web = _load_json(
                    _run_command(
                        _compose(
                            run_id,
                            "--profile",
                            "web",
                            "run",
                            "--rm",
                            "--no-deps",
                            "web-smoke",
                        ),
                        env=env,
                        timeout=120,
                        label="optional provider smoke",
                    ),
                    label="optional provider smoke",
                )
                report["web_smoke"] = web
            except LabError:
                report["web_smoke"] = {"gating": False, "status": "unavailable"}
    except LabError as exc:
        failure = exc
        report["failure"] = str(exc)
    finally:
        if not docker_validated:
            report["cleanup"] = "not-started"
        elif keep:
            report["cleanup"] = "kept-by-request"
        else:
            try:
                _cleanup(run_id, env)
                report["cleanup"] = "completed"
            except LabError as cleanup_error:
                report["cleanup"] = "failed"
                if failure is None:
                    failure = cleanup_error
                    report["status"] = "failed"
                    report["failure"] = str(cleanup_error)
    target = _write_report(run_id, report)
    if failure is not None:
        raise failure
    return target


def cleanup_run(run_id: str) -> None:
    if not RUN_ID_PATTERN.fullmatch(run_id):
        raise LabError("cleanup requires an exact disposable run id")
    env = _run_environment(run_id)
    _assert_local_docker(env)
    _cleanup(run_id, env)


def _parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(description=__doc__)
    subparsers = parser.add_subparsers(dest="command", required=True)
    run = subparsers.add_parser("run", help="run the offline disposable gate")
    run.add_argument("--keep", action="store_true", help="keep only this run's Docker resources")
    run.add_argument("--with-web-smoke", action="store_true", help="run the isolated, non-gating provider smoke")
    cleanup = subparsers.add_parser("cleanup", help="remove one retained run")
    cleanup.add_argument("--run-id", required=True)
    return parser


def main(argv: Sequence[str] | None = None) -> int:
    args = _parser().parse_args(argv)
    try:
        if args.command == "run":
            report = run_lab(keep=bool(args.keep), with_web_smoke=bool(args.with_web_smoke))
            print(f"Disposable Calibre gate passed. Sanitized report: {report}")
        else:
            cleanup_run(str(args.run_id))
            print(f"Disposable Calibre run removed: {args.run_id}")
    except LabError as exc:
        print(f"Disposable Calibre gate failed safely: {exc}", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    raise SystemExit(main())

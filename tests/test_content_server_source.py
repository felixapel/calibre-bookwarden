import json
import subprocess
from collections.abc import Sequence
from pathlib import Path

import pytest

from calibre_ai_auditor.calibre.content_server import (
    ContentServerError,
    ContentServerSource,
    aggregate_inventory,
)


class RecordingRunner:
    def __init__(self, outputs: Sequence[str]) -> None:
        self.outputs = list(outputs)
        self.calls: list[tuple[list[str], str, float]] = []

    def __call__(self, command: list[str], password: str, timeout: float) -> subprocess.CompletedProcess[str]:
        self.calls.append((command, password, timeout))
        return subprocess.CompletedProcess(command, 0, stdout=self.outputs.pop(0), stderr="")


@pytest.mark.parametrize(
    "url",
    [
        "https://127.0.0.1:18086",
        "http://localhost:18086",
        "http://192.168.0.122:8086",
        "http://127.0.0.1:18086/path",
        "http://user:secret@127.0.0.1:18086",
        "http://127.0.0.1:18086?next=http://example.com",
        "http://127.0.0.1:18086/#other",
    ],
)
def test_content_server_source_accepts_only_an_explicit_loopback_tunnel(url: str) -> None:
    with pytest.raises(ValueError, match="loopback"):
        ContentServerSource(
            url,
            library_id="library",
            username="auditor",
            source_identity="SHA256:test-host",
            password="secret",
        )


def test_source_fingerprint_is_stable_across_tunnel_ports_and_bound_to_remote_identity() -> None:
    common = {
        "library_id": "library",
        "username": "auditor",
        "source_identity": "SHA256:verified-host",
        "password": "secret",
    }
    first = ContentServerSource("http://127.0.0.1:18086", **common)
    second = ContentServerSource("http://127.0.0.1:28086", **common)
    other = ContentServerSource(
        "http://127.0.0.1:18086",
        **{**common, "source_identity": "SHA256:different-host"},
    )

    assert first.fingerprint == second.fingerprint
    assert first.fingerprint != other.fingerprint


def test_list_books_passes_password_only_over_stdin() -> None:
    runner = RecordingRunner([json.dumps([{"id": 7, "title": "Private title", "formats": ["/library/book.epub"]}])])
    source = ContentServerSource(
        "http://127.0.0.1:18086",
        library_id="library-id",
        username="auditor",
        source_identity="SHA256:test-host",
        password="top-secret",
        runner=runner,
    )

    assert source.list_books()[0]["id"] == 7

    command, password, timeout = runner.calls[0]
    assert command[:2] == ["calibredb", "list"]
    assert "--for-machine" in command
    assert "--fields" in command
    assert "all" in command
    assert "--password" in command
    assert command[command.index("--password") + 1] == "<stdin>"
    assert "top-secret" not in " ".join(command)
    assert password == "top-secret"
    assert timeout == 120.0


def test_list_books_rejects_untrusted_or_ambiguous_machine_output() -> None:
    malformed = RecordingRunner(["not-json"])
    duplicate = RecordingRunner([json.dumps([{"id": 1}, {"id": 1}])])

    with pytest.raises(ContentServerError, match="valid JSON"):
        ContentServerSource(
            "http://127.0.0.1:18086",
            library_id="library",
            username="auditor",
            source_identity="SHA256:test-host",
            password="secret",
            runner=malformed,
        ).list_books()

    with pytest.raises(ContentServerError, match="duplicate"):
        ContentServerSource(
            "http://127.0.0.1:18086",
            library_id="library",
            username="auditor",
            source_identity="SHA256:test-host",
            password="secret",
            runner=duplicate,
        ).list_books()


def test_show_metadata_requires_one_exact_book_id() -> None:
    runner = RecordingRunner([json.dumps([{"id": 7, "title": "Private"}])])
    source = ContentServerSource(
        "http://127.0.0.1:18086",
        library_id="library",
        username="auditor",
        source_identity="SHA256:test-host",
        password="secret",
        runner=runner,
    )

    assert source.show_metadata(7)["id"] == 7
    command = runner.calls[0][0]
    assert command[:2] == ["calibredb", "list"]
    assert command[command.index("--search") + 1] == "id:7"

    missing = RecordingRunner(["[]"])
    wrong = RecordingRunner([json.dumps([{"id": 8}])])
    for invalid_runner in (missing, wrong):
        invalid = ContentServerSource(
            "http://127.0.0.1:18086",
            library_id="library",
            username="auditor",
            source_identity="SHA256:test-host",
            password="secret",
            runner=invalid_runner,
        )
        with pytest.raises(ContentServerError, match="exact book"):
            invalid.show_metadata(7)


def test_logical_format_references_are_stable_and_reject_ambiguous_formats() -> None:
    source = ContentServerSource(
        "http://127.0.0.1:18086",
        library_id="library",
        username="auditor",
        source_identity="SHA256:test-host",
        password="secret",
        runner=RecordingRunner([]),
    )
    references = source.format_references(7, ["/private/book.epub", {"format": "pdf"}])

    assert references == [
        f"calibre-server:{source.fingerprint}:7:EPUB",
        f"calibre-server:{source.fingerprint}:7:PDF",
    ]
    assert source.format_from_reference(references[0]) == "EPUB"
    with pytest.raises(ContentServerError, match="duplicate"):
        source.format_references(7, ["one.epub", "two.epub"])
    with pytest.raises(ValueError, match="invalid"):
        source.format_from_reference("calibre-server:wrong:7:EPUB")


def test_command_failure_does_not_echo_secret_or_remote_output() -> None:
    secret = "never-print-this"

    def failing_runner(command: list[str], password: str, timeout: float) -> subprocess.CompletedProcess[str]:
        assert password == secret
        return subprocess.CompletedProcess(command, 1, stdout="Private title", stderr=f"bad password {secret}")

    source = ContentServerSource(
        "http://127.0.0.1:18086",
        library_id="library",
        username="auditor",
        source_identity="SHA256:test-host",
        password=secret,
        runner=failing_runner,
    )

    with pytest.raises(ContentServerError) as error:
        source.list_books()

    assert secret not in str(error.value)
    assert "Private title" not in str(error.value)


def test_runner_exception_chain_is_removed_at_the_source_boundary() -> None:
    secret = "reflected-secret"

    def raising_runner(_command: list[str], _password: str, _timeout: float):
        raise RuntimeError(secret)

    source = ContentServerSource(
        "http://127.0.0.1:18086",
        library_id="library",
        username="auditor",
        source_identity="SHA256:test-host",
        password=secret,
        runner=raising_runner,
    )

    with pytest.raises(ContentServerError) as error:
        source.list_books()

    assert error.value.__cause__ is None
    assert error.value.__context__ is None
    assert secret not in repr(error.value)


def test_production_runner_bounds_machine_output_before_reading_it(tmp_path: Path, monkeypatch) -> None:
    import calibre_ai_auditor.calibre.content_server as module

    executable = tmp_path / "oversized-output"
    executable.write_text("#!/usr/bin/env python3\nimport sys\nsys.stdout.write('x' * 4096)\n")
    executable.chmod(0o700)
    monkeypatch.setattr(module, "MAX_MACHINE_OUTPUT_BYTES", 128)

    with pytest.raises(ContentServerError, match="exceeded"):
        module._subprocess_runner([str(executable), "list"], "secret", 5.0)


def test_aggregate_inventory_contains_counts_but_no_book_metadata() -> None:
    books = [
        {
            "id": 1,
            "title": "Sensitive title",
            "authors": ["Private author"],
            "publisher": "Publisher",
            "pubdate": "2020-01-01T00:00:00+00:00",
            "last_modified": "2026-07-01T10:00:00+00:00",
            "languages": ["en"],
            "identifiers": {"isbn": "9780306406157"},
            "formats": ["/secret/path/book.epub", "/secret/path/book.pdf"],
            "comments": "Private notes",
        },
        {
            "id": 2,
            "title": "",
            "authors": "",
            "publisher": "",
            "pubdate": None,
            "last_modified": None,
            "languages": [],
            "isbn": "not-an-isbn",
            "formats": [],
        },
    ]

    report = aggregate_inventory(books, source_fingerprint="a" * 64)
    payload = report.model_dump(mode="json")
    encoded = json.dumps(payload, sort_keys=True)

    assert payload["counts"] == {"books": 2, "formats": 2, "multi_format": 1, "no_format": 1}
    assert payload["formats"] == {"EPUB": 1, "PDF": 1}
    assert payload["languages"] == {"EN": 1}
    assert payload["isbn"] == {"invalid": 1, "missing": 0, "valid": 1}
    assert payload["incomplete_fields"] == {
        "authors": 1,
        "languages": 1,
        "publisher": 1,
        "pubdate": 1,
        "title": 1,
    }
    assert payload["last_modified"]["missing"] == 1
    assert "Sensitive title" not in encoded
    assert "Private author" not in encoded
    assert "/secret/path" not in encoded
    assert "Private notes" not in encoded


def test_aggregate_inventory_buckets_untrusted_format_and_language_keys() -> None:
    report = aggregate_inventory(
        [
            {
                "id": 1,
                "title": "Sensitive title",
                "formats": ["SECRETTITLE"],
                "languages": ["PRIVATE-AUTHOR"],
            }
        ],
        source_fingerprint="a" * 64,
    )
    encoded = json.dumps(report.model_dump(mode="json"), sort_keys=True)

    assert report.formats == {"OTHER": 1}
    assert report.languages == {"OTHER": 1}
    assert "SECRET" not in encoded
    assert "PRIVATE" not in encoded


def test_export_format_uses_read_only_flags_and_private_containment(tmp_path: Path) -> None:
    def exporting_runner(command: list[str], password: str, timeout: float) -> subprocess.CompletedProcess[str]:
        target = Path(command[command.index("--to-dir") + 1])
        (target / "book.epub").write_bytes(b"ebook")
        return subprocess.CompletedProcess(command, 0, stdout="", stderr="")

    source = ContentServerSource(
        "http://127.0.0.1:18086",
        library_id="library",
        username="auditor",
        source_identity="SHA256:test-host",
        password="secret",
        runner=exporting_runner,
    )

    with source.export_format(7, "epub", scratch_root=tmp_path) as exported:
        assert exported.read_bytes() == b"ebook"
        assert exported.is_relative_to(tmp_path)
        command = source.command_history[-1]
        assert command[:2] == ["calibredb", "export"]
        assert "--dont-update-metadata" in command
        assert "--dont-save-cover" in command
        assert "--dont-save-extra-files" in command
        assert "--dont-write-opf" in command
        assert command[command.index("--formats") + 1] == "EPUB"

    assert list(tmp_path.iterdir()) == []


def test_export_format_rejects_symlinks_and_cleans_scratch(tmp_path: Path) -> None:
    outside = tmp_path.parent / "outside.epub"
    outside.write_bytes(b"outside")

    test_link = tmp_path / "probe_symlink"
    try:
        test_link.symlink_to(outside)
        test_link.unlink()
    except OSError:
        pytest.skip("Symlink creation requires elevated privileges on this OS")

    def symlink_runner(command: list[str], password: str, timeout: float) -> subprocess.CompletedProcess[str]:
        target = Path(command[command.index("--to-dir") + 1])
        (target / "book.epub").symlink_to(outside)
        return subprocess.CompletedProcess(command, 0, stdout="", stderr="")

    source = ContentServerSource(
        "http://127.0.0.1:18086",
        library_id="library",
        username="auditor",
        source_identity="SHA256:test-host",
        password="secret",
        runner=symlink_runner,
    )

    with (
        pytest.raises(ContentServerError, match="regular file"),
        source.export_format(7, "EPUB", scratch_root=tmp_path),
    ):
        pass

    assert list(tmp_path.iterdir()) == []


def test_export_format_rejects_hardlinks_and_cleans_scratch(tmp_path: Path) -> None:
    outside = tmp_path.parent / "outside-hardlink.epub"
    outside.write_bytes(b"outside")

    def hardlink_runner(command: list[str], password: str, timeout: float) -> subprocess.CompletedProcess[str]:
        target = Path(command[command.index("--to-dir") + 1])
        (target / "book.epub").hardlink_to(outside)
        return subprocess.CompletedProcess(command, 0, stdout="", stderr="")

    source = ContentServerSource(
        "http://127.0.0.1:18086",
        library_id="library",
        username="auditor",
        source_identity="SHA256:test-host",
        password="secret",
        runner=hardlink_runner,
    )

    with (
        pytest.raises(ContentServerError, match="regular file"),
        source.export_format(7, "EPUB", scratch_root=tmp_path),
    ):
        pass

    assert list(tmp_path.iterdir()) == []

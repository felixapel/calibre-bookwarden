import re

from typer.testing import CliRunner

from calibre_ai_auditor.production_cli import app


def test_certificate_a_cli_exposes_only_runtime_commands() -> None:
    result = CliRunner().invoke(app, ["--help"])

    assert result.exit_code == 0
    for command in ("web", "verifier", "verifier-health", "migrate"):
        assert re.search(rf"^\s*│\s*{re.escape(command)}(?:\s|│)", result.stdout, re.MULTILINE)
    for quarantined in ("writer", "audit", "scan", "inspect", "mcp", "ingest", "retention"):
        assert not re.search(rf"^\s*│\s*{re.escape(quarantined)}(?:\s|│)", result.stdout, re.MULTILINE)

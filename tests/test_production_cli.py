import re

from click import unstyle
from typer.testing import CliRunner

from calibre_ai_auditor.production_cli import app


def test_certificate_a_cli_exposes_only_runtime_commands() -> None:
    result = CliRunner().invoke(
        app,
        ["--help"],
        env={
            "CI": "true",
            "CLICOLOR_FORCE": "1",
            "FORCE_COLOR": "1",
            "PY_COLORS": "1",
            "TERM": "xterm-256color",
        },
    )

    assert result.exit_code == 0
    output = unstyle(result.stdout)
    for command in ("web", "verifier", "verifier-health", "migrate"):
        assert re.search(rf"^\s*│\s*{re.escape(command)}(?:\s|│)", output, re.MULTILINE)
    for quarantined in ("writer", "audit", "scan", "inspect", "mcp", "ingest", "retention"):
        assert not re.search(rf"^\s*│\s*{re.escape(quarantined)}(?:\s|│)", output, re.MULTILINE)

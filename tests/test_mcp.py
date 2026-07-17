"""Basic tests for the v1.2 MCP server (read-only tools).

These tests exercise the MCP-exposed query functions using an in-memory
SQLite DB. They verify:
- Tool functions are importable (when fastmcp present)
- query_book_audit returns verdict + book data
- list_problematic_books filters by status and includes decimal chapter fields
- get_run_metrics aggregates correctly

Decimal chapter/volume/series_position values are preserved (Weebarr convention).
No writes are performed by the tools under test.
"""

from __future__ import annotations

from collections.abc import Generator
from typing import Any

import pytest
from sqlmodel import Session, SQLModel, create_engine

from calibre_ai_auditor.mcp_server import (  # noqa: E402
    get_run_metrics,
    get_v2_verification_run,
    list_problematic_books,
    list_recent_runs,
    list_v2_verification_runs,
    mcp,
    query_book_audit,
)
from calibre_ai_auditor.storage.models import (  # noqa: E402
    BookRecord,
    EvidencePackage,
    Run,
    VerificationResult,
    VerificationRun,
)


@pytest.fixture(name="mcp_test_db")
def mcp_test_db_fixture(tmp_path: Any) -> Generator[tuple[Any, Session], None, None]:
    """Create an isolated in-memory engine + session populated with test data."""
    db_file = tmp_path / "mcp_test.db"
    engine = create_engine(f"sqlite:///{db_file}", connect_args={"check_same_thread": False})
    SQLModel.metadata.create_all(engine)

    # Monkey-patch get_engine + load_settings so tools use our tmp DB and never touch /state
    import calibre_ai_auditor.config.settings as settings_mod
    import calibre_ai_auditor.mcp_server as mcpserver_mod
    import calibre_ai_auditor.storage.db as dbmod

    original_get_engine = dbmod.get_engine
    original_load = settings_mod.load_settings
    test_engine = engine
    tmp_state = tmp_path / "state"
    tmp_art = tmp_path / "artifacts"
    tmp_state.mkdir(parents=True, exist_ok=True)
    tmp_art.mkdir(parents=True, exist_ok=True)

    class _FakeStorage:
        sqlite_path = db_file
        artifacts_dir = tmp_art

    class _FakeLibrary:
        path = None
        read_only = True

    class _FakeSettings:
        database = type("D", (), {"backend": "sqlite", "postgres_dsn": "", "sqlite_path": db_file})()
        storage = _FakeStorage()
        library = _FakeLibrary()
        manga_mode = type("M", (), {"enabled": False, "providers": type("P", (), {"komf": False})()})()
        # minimal attrs accessed indirectly
        vectors = type("V", (), {"enabled": False})()
        extractors = type("E", (), {"tika": type("T", (), {"enabled": False})()})()
        preview = type("Pr", (), {"gotenberg_enabled": False})()
        paperless = type("Pa", (), {"enabled": False})()

    def _fake_load_settings(config_path: Any = None) -> Any:  # noqa: ARG001
        return _FakeSettings()

    def _fake_get_engine(settings: Any) -> Any:  # noqa: ARG001
        return test_engine

    dbmod.get_engine = _fake_get_engine  # type: ignore[assignment]
    settings_mod.load_settings = _fake_load_settings  # type: ignore[assignment]
    # Also patch on the module that already imported the name (defensive)
    if hasattr(mcpserver_mod, "load_settings"):
        mcpserver_mod.load_settings = _fake_load_settings  # type: ignore[attr-defined]

    with Session(test_engine) as session:
        run = Run(run_id="mcp_test_run", status="completed")
        session.add(run)

        # Book 1: clean
        b1 = BookRecord(
            book_key="calibre:101",
            run_id="mcp_test_run",
            calibre_book_id=101,
            source="calibre",
            current_metadata={
                "title": "Clean Book",
                "authors": ["Good Author"],
                "volume": 1,
                "chapter": 2.0,
                "series_position": 1.5,
            },
            files=[],
            status="no_change",
        )
        session.add(b1)

        # Book 2: problematic with risk + decimal chapter
        b2 = BookRecord(
            book_key="calibre:202",
            run_id="mcp_test_run",
            calibre_book_id=202,
            source="calibre",
            current_metadata={
                "title": "Problem Manga",
                "authors": ["Manga Artist"],
                "series": "Test Series",
                "volume": 3,
                "chapter": 4.5,  # decimal per Weebarr
                "series_position": 2.5,
            },
            files=[],
            status="needs_review",
        )
        session.add(b2)

        # Evidence for book 2 (verdict shape)
        verdict = {
            "book_key": "calibre:202",
            "run_id": "mcp_test_run",
            "field_verdicts": {
                "title": {"field": "title", "verdict": "mismatch", "confidence": 85},
                "chapter": {"field": "chapter", "verdict": "confirmed", "confidence": 90},
            },
            "overall_confidence": 82,
            "risk_flags": ["edition_ambiguous"],
            "action": "needs_review",
            "auto_apply_eligible": False,
        }
        pkg = EvidencePackage(
            evidence_id="ev_mcp_202",
            book_key="calibre:202",
            run_id="mcp_test_run",
            current=b2.current_metadata,
            extracted=verdict,
            decision=verdict,
            risk_flags=["edition_ambiguous"],
        )
        session.add(pkg)
        session.commit()

    yield engine, Session(engine)

    # restore
    dbmod.get_engine = original_get_engine  # type: ignore[assignment]
    settings_mod.load_settings = original_load  # type: ignore[assignment]


def test_mcp_server_object_exists_when_fastmcp_installed() -> None:
    """FastMCP server instance is created when the optional extra is present."""
    pytest.importorskip("fastmcp")
    assert mcp is not None
    # FastMCP exposes tools via internal registry in recent versions; just ensure no crash
    assert hasattr(mcp, "tool") or hasattr(mcp, "_tool_manager")


def test_mcp_tool_functions_importable_without_fastmcp() -> None:
    """Read tools remain plain callables even if FastMCP is absent."""
    assert callable(query_book_audit)
    assert callable(list_problematic_books)
    assert callable(get_run_metrics)
    assert callable(list_recent_runs)


def test_query_book_audit_returns_verdict_and_preserves_decimals(mcp_test_db: tuple[Any, Session]) -> None:
    engine, session = mcp_test_db  # noqa: F841
    result = query_book_audit("calibre:202")
    assert result["book_key"] == "calibre:202"
    assert result["status"] == "needs_review"
    assert result["has_verdict"] is True
    assert "verdict" in result
    assert result["verdict"] is not None
    assert result["verdict"]["action"] == "needs_review"
    assert result["verdict"]["risk_flags"] == ["edition_ambiguous"]

    # Decimal chapter from current_metadata
    ch = result["book"]["current_metadata"].get("chapter")
    assert ch == 4.5
    assert isinstance(ch, float)

    vol = result["book"]["current_metadata"].get("volume")
    assert vol == 3


def test_query_book_audit_missing_returns_error() -> None:
    result = query_book_audit("calibre:does-not-exist")
    assert result.get("error") == "not_found"


def test_list_problematic_books_defaults_and_decimal_fields(mcp_test_db: tuple[Any, Session]) -> None:
    rows = list_problematic_books(limit=10)
    # Should surface the needs_review book
    keys = [r["book_key"] for r in rows]
    assert "calibre:202" in keys
    manga_row = next(r for r in rows if r["book_key"] == "calibre:202")
    assert manga_row["status"] == "needs_review"
    assert manga_row["chapter"] == 4.5  # decimal
    assert manga_row["volume"] == 3
    assert "risk_flags" in manga_row


def test_list_problematic_books_status_filter(mcp_test_db: tuple[Any, Session]) -> None:
    rows = list_problematic_books(limit=5, status="no_change")
    keys = [r["book_key"] for r in rows]
    assert "calibre:101" in keys
    assert all(r["status"] == "no_change" for r in rows)


def test_get_run_metrics_aggregates(mcp_test_db: tuple[Any, Session]) -> None:
    metrics = get_run_metrics(run_id="mcp_test_run")
    assert metrics["run_id"] == "mcp_test_run"
    assert metrics["total_books"] >= 2
    counts = metrics["counts_by_status"]
    assert counts.get("needs_review", 0) >= 1
    assert counts.get("no_change", 0) >= 1
    assert isinstance(metrics["sample_book_keys"], list)


def test_get_run_metrics_latest_when_no_run_id(mcp_test_db: tuple[Any, Session]) -> None:
    metrics = get_run_metrics(run_id=None)
    assert "run_id" in metrics
    assert metrics["total_books"] >= 2


def test_list_recent_runs(mcp_test_db: tuple[Any, Session]) -> None:
    runs = list_recent_runs(limit=5)
    assert isinstance(runs, list)
    assert runs, "seeded mcp_test_run must appear"
    assert "run_id" in runs[0]
    assert runs[0]["run_id"] == "mcp_test_run"
    assert runs[0]["status"] == "completed"


def test_list_and_get_v2_verification_runs(mcp_test_db: tuple[Any, Session]) -> None:
    engine, session = mcp_test_db
    session.add(
        VerificationRun(
            run_id="v2_run_mcp",
            status="completed",
            pipeline_version="manifestation-v2",
            mode="shadow",
            total=1,
            completed=1,
            counts={"review": 1},
        )
    )
    session.add(
        VerificationResult(
            result_id="vr_1",
            run_id="v2_run_mcp",
            book_key="calibre:9",
            state="review",
            evidence_id="ev_v2_9",
            verdict={
                "schema_version": 2,
                "state": "review",
                "identity": {"tier": "B"},
            },
        )
    )
    session.commit()

    listed = list_v2_verification_runs(limit=10)
    assert any(r["run_id"] == "v2_run_mcp" for r in listed)
    row = next(r for r in listed if r["run_id"] == "v2_run_mcp")
    assert row["pipeline_version"] == "manifestation-v2"
    assert row["counts"].get("review") == 1

    detail = get_v2_verification_run("v2_run_mcp")
    assert detail["run_id"] == "v2_run_mcp"
    assert detail["status"] == "completed"
    assert detail["books"]
    assert detail["books"][0]["book_key"] == "calibre:9"
    assert detail["books"][0]["tier"] == "B"

    missing = get_v2_verification_run("nope")
    assert missing.get("error") == "not_found"

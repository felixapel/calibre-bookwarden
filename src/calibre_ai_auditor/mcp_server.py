"""Minimal MCP server for Calibre AI Auditor (v1.2).

STDIO transport for Hermes and other MCP clients.

Exposes read-only tools over the existing verification/storage surface:
- query_book_audit: fetch persisted BookRecord + latest BookVerdict (via EvidencePackage)
- list_problematic_books: books needing attention (status + risk), decimal chapter safe
- get_run_metrics: aggregates from BookRecord by run
- list_recent_runs: discovery helper

Reuses:
- ContentVerificationEngine / BookVerdict / FieldVerdict models (via persisted decision)
- storage/models (BookRecord, EvidencePackage, Run) + db.get_engine
- comics/pipeline decimal fields (volume/chapter/series_position as int/float)
- web/api patterns for querying

Strictly read-only. No DB mutations, no apply paths.

Tool functions are always importable. Starting the STDIO server requires the
optional ``mcp`` extra (fastmcp).

Run:
  bookaudit mcp
Or directly (after `pip install fastmcp` or `uv pip install -e '.[mcp]'`):
  python -m calibre_ai_auditor.mcp_server
"""

from __future__ import annotations

import logging
from collections.abc import Callable
from typing import Any

from sqlmodel import Session, desc, select

from calibre_ai_auditor.config.settings import load_settings
from calibre_ai_auditor.storage.db import get_engine
from calibre_ai_auditor.storage.models import BookRecord, EvidencePackage, Run

logger = logging.getLogger(__name__)

try:
    from fastmcp import FastMCP
except ImportError:  # pragma: no cover - optional dep
    FastMCP = None  # type: ignore[misc, assignment]
    mcp: Any = None
else:
    mcp = FastMCP("calibre-audit")


def _tool[F: Callable[..., Any]](fn: F) -> F:
    """Register with FastMCP when available; always return the plain callable."""
    if mcp is not None:
        return mcp.tool(fn)  # type: ignore[no-any-return]
    return fn


def _open_session() -> Session:
    """Open a DB session using project settings (sqlite or postgres)."""
    settings = load_settings()
    engine = get_engine(settings)
    return Session(engine)


def _latest_evidence(session: Session, book_key: str) -> EvidencePackage | None:
    return session.exec(
        select(EvidencePackage)
        .where(EvidencePackage.book_key == book_key)
        .order_by(desc(EvidencePackage.created_at))
        .limit(1)
    ).first()


@_tool
def query_book_audit(book_key: str) -> dict[str, Any]:
    """Return the BookRecord and latest persisted audit verdict (BookVerdict shape) for a book.

    The 'verdict' is the full v1.0/v1.1 BookVerdict (per-field FieldVerdicts, action,
    confidence, risk_flags, proposed_patch, decimal chapter/volume/series_position preserved).

    Read-only. No side effects.
    """
    if not book_key or not isinstance(book_key, str):
        return {"error": "invalid_book_key", "detail": "book_key must be a non-empty string"}

    with _open_session() as session:
        book = session.exec(select(BookRecord).where(BookRecord.book_key == book_key)).first()
        if not book:
            return {"error": "not_found", "book_key": book_key}

        pkg = _latest_evidence(session, book_key)
        verdict = pkg.decision if pkg and pkg.decision else None
        risk_flags = pkg.risk_flags if pkg else (book.field_locks.get("risk_flags") if book.field_locks else [])

        return {
            "book_key": book_key,
            "run_id": book.run_id,
            "status": book.status,
            "book": {
                "calibre_book_id": book.calibre_book_id,
                "source": book.source,
                "current_metadata": book.current_metadata,
                "files": book.files,
                "paperless_document_id": book.paperless_document_id,
            },
            "verdict": verdict,
            "risk_flags": risk_flags or [],
            "has_verdict": verdict is not None,
        }


@_tool
def list_problematic_books(
    limit: int = 50,
    status: str | None = None,
    has_risk: bool = True,
) -> list[dict[str, Any]]:
    """List books that are problematic (needs attention).

    Defaults to statuses that indicate work required: needs_review, suggest_fix, defer.
    When status is provided, it is used as an exact filter (e.g. "needs_review").

    Each row includes key fields for quick triage, including comic decimal fields
    (volume, chapter, series_position) per Weebarr convention.

    Read-only. Results are capped (max 500).
    """
    safe_limit = max(1, min(int(limit or 50), 500))

    problematic_statuses = ("needs_review", "suggest_fix", "defer")

    with _open_session() as session:
        stmt = select(BookRecord)
        # Fetch extra rows when filtering problematic statuses in Python.
        stmt = stmt.where(BookRecord.status == status) if status else stmt.limit(safe_limit * 2)

        books = session.exec(stmt).all()

        out: list[dict[str, Any]] = []
        for b in books:
            if not status and b.status not in problematic_statuses:
                continue
            meta: dict[str, Any] = b.current_metadata or {}
            row = {
                "book_key": b.book_key,
                "run_id": b.run_id,
                "status": b.status,
                "title": meta.get("title"),
                "authors": meta.get("authors") or [],
                "series": meta.get("series"),
                "volume": meta.get("volume"),
                "chapter": meta.get("chapter"),  # float/decimal preserved
                "series_position": meta.get("series_position"),
                "calibre_id": b.calibre_book_id,
            }
            pkg = _latest_evidence(session, b.book_key)
            row["risk_flags"] = (pkg.risk_flags if pkg else []) or []
            if has_risk and not row["risk_flags"] and b.status not in ("needs_review", "suggest_fix"):
                pass
            out.append(row)
            if len(out) >= safe_limit:
                break
        return out


@_tool
def get_run_metrics(run_id: str | None = None) -> dict[str, Any]:
    """Aggregate simple metrics for a run (or the most recent run).

    Returns total book count + breakdown by status (which mirrors VerdictAction values:
    no_change, suggest_fix, needs_review, defer).

    Also includes a compact sample of book_keys for the run.
    Read-only. Computed from persisted BookRecord rows.
    """
    with _open_session() as session:
        if run_id is None:
            latest = session.exec(select(Run).order_by(desc(Run.created_at)).limit(1)).first()
            if not latest:
                return {"error": "no_runs", "detail": "No runs recorded in the database."}
            run_id = latest.run_id

        books = session.exec(select(BookRecord).where(BookRecord.run_id == run_id)).all()
        counts: dict[str, int] = {}
        sample: list[str] = []
        for b in books:
            counts[b.status] = counts.get(b.status, 0) + 1
            if len(sample) < 5:
                sample.append(b.book_key)

        return {
            "run_id": run_id,
            "total_books": len(books),
            "counts_by_status": counts,
            "sample_book_keys": sample,
            "note": (
                "Counts derived from BookRecord.status after verification/audit. "
                "Decimal fields supported in source data."
            ),
        }


@_tool
def list_recent_runs(limit: int = 10) -> list[dict[str, Any]]:
    """List the most recent audit/verify runs (read-only)."""
    safe_limit = max(1, min(int(limit or 10), 100))
    with _open_session() as session:
        runs = session.exec(select(Run).order_by(desc(Run.created_at)).limit(safe_limit)).all()
        return [
            {
                "run_id": r.run_id,
                "created_at": r.created_at.isoformat() if r.created_at else None,
                "status": r.status,
                "metadata_filter": r.metadata_filter,
            }
            for r in runs
        ]


def run_stdio_server() -> None:
    """Start the FastMCP STDIO server. Requires the optional ``mcp`` extra."""
    if mcp is None:
        raise ImportError(
            "fastmcp is required for the MCP server. Install with: uv pip install -e '.[mcp]' or pip install fastmcp"
        )
    mcp.run()


if __name__ == "__main__":
    run_stdio_server()

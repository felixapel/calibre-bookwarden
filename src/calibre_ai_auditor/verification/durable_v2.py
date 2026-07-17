"""Restart-safe claim/lease helpers for Manifestation V2 verify runs.

WebUI/API verify used to fire-and-forget with asyncio.create_task. Progress is
already durable in VerificationRun/Result; these helpers add a worker lease so
a restarted app process can reclaim orphaned runs and resume incomplete books
through the existing SQLAuditStore.resumable_book_keys() skip set.
"""

from __future__ import annotations

import contextlib
import logging
from datetime import UTC, datetime, timedelta
from typing import Any
from uuid import uuid4

from sqlalchemy.engine import Engine
from sqlmodel import Session, col, select

from calibre_ai_auditor.storage.models import VerificationResult, VerificationRun
from calibre_ai_auditor.verification.persistence_v2 import SQLAuditStore
from calibre_ai_auditor.verification.pipeline_v2 import TERMINAL_BOOK_STATES, BookAuditState

logger = logging.getLogger(__name__)

DEFAULT_LEASE_TTL_SECONDS = 120
ACTIVE_RUN_STATUSES = frozenset({"running", "pending"})
TERMINAL_RUN_STATUSES = frozenset({"completed", "completed_with_errors", "failed", "blocked_recovery"})


def new_worker_id(prefix: str = "worker") -> str:
    return f"{prefix}-{uuid4().hex[:12]}"


def _utc_naive(moment: datetime | None = None) -> datetime:
    """Return UTC wall time without tzinfo (SQLModel/SQLite DateTime is naive)."""
    value = moment or datetime.now(UTC)
    if value.tzinfo is not None:
        return value.astimezone(UTC).replace(tzinfo=None)
    return value


def claim_verification_run(
    engine: Engine,
    run_id: str,
    *,
    owner: str,
    now: datetime | None = None,
    ttl_seconds: int = DEFAULT_LEASE_TTL_SECONDS,
) -> bool:
    """Atomically claim a non-terminal run for ``owner``.

    Succeeds when the run has no lease, the lease is expired, or the same owner
    refreshes its lease. Returns False when another live owner holds the lease
    or the run is terminal/missing.
    """
    if not owner:
        raise ValueError("lease owner must be non-empty")
    moment = _utc_naive(now)
    expires = moment + timedelta(seconds=max(1, ttl_seconds))
    with Session(engine) as session:
        run = session.exec(select(VerificationRun).where(VerificationRun.run_id == run_id)).first()
        if run is None:
            return False
        if run.status in TERMINAL_RUN_STATUSES or run.finished_at is not None:
            return False
        lease_expires = _utc_naive(run.lease_expires_at) if run.lease_expires_at is not None else None
        if run.lease_owner and run.lease_owner != owner and lease_expires is not None and lease_expires > moment:
            return False
        run.lease_owner = owner
        run.lease_expires_at = expires
        if run.status not in ACTIVE_RUN_STATUSES:
            run.status = "running"
        session.add(run)
        session.commit()
        return True


def heartbeat_verification_run(
    engine: Engine,
    run_id: str,
    *,
    owner: str,
    now: datetime | None = None,
    ttl_seconds: int = DEFAULT_LEASE_TTL_SECONDS,
) -> bool:
    """Extend a lease held by ``owner``. Fails if the owner lost the claim."""
    moment = _utc_naive(now)
    expires = moment + timedelta(seconds=max(1, ttl_seconds))
    with Session(engine) as session:
        run = session.exec(select(VerificationRun).where(VerificationRun.run_id == run_id)).first()
        if run is None or run.lease_owner != owner:
            return False
        if run.status in TERMINAL_RUN_STATUSES:
            return False
        run.lease_expires_at = expires
        session.add(run)
        session.commit()
        return True


def release_verification_run(
    engine: Engine,
    run_id: str,
    *,
    owner: str,
) -> None:
    """Drop the lease when still owned by ``owner`` (best-effort)."""
    with Session(engine) as session:
        run = session.exec(select(VerificationRun).where(VerificationRun.run_id == run_id)).first()
        if run is None or run.lease_owner != owner:
            return
        run.lease_owner = None
        run.lease_expires_at = None
        session.add(run)
        session.commit()


def list_recoverable_v2_run_ids(engine: Engine, *, now: datetime | None = None) -> list[str]:
    """Return V2 run ids that are non-terminal and free or lease-expired."""
    moment = _utc_naive(now)
    with Session(engine) as session:
        rows = session.exec(
            select(VerificationRun)
            .where(VerificationRun.pipeline_version == "manifestation-v2")
            .where(col(VerificationRun.finished_at).is_(None))
            .order_by(col(VerificationRun.started_at))
        ).all()
        recoverable: list[str] = []
        for run in rows:
            if run.status in TERMINAL_RUN_STATUSES:
                continue
            lease_expires = _utc_naive(run.lease_expires_at) if run.lease_expires_at is not None else None
            if run.lease_owner and lease_expires is not None and lease_expires > moment:
                continue
            recoverable.append(run.run_id)
        return recoverable


def incomplete_book_keys(engine: Engine, run_id: str) -> set[str]:
    """Book keys still needing work (not sealed-terminal)."""
    store = SQLAuditStore(engine, run_id)
    sealed_done = store.resumable_book_keys()
    with Session(engine) as session:
        rows = session.exec(select(VerificationResult).where(VerificationResult.run_id == run_id)).all()
        return {row.book_key for row in rows if row.book_key not in sealed_done}


def reset_stale_in_flight_states(engine: Engine, run_id: str) -> int:
    """Reset non-terminal in-flight book states to pending so resume restarts cleanly."""
    in_flight = {
        BookAuditState.snapshotting.value,
        BookAuditState.extracting.value,
        BookAuditState.resolving.value,
        BookAuditState.decided.value,
        BookAuditState.applying.value,
    }
    terminal = {state.value for state in TERMINAL_BOOK_STATES}
    reset = 0
    with Session(engine) as session:
        rows = session.exec(select(VerificationResult).where(VerificationResult.run_id == run_id)).all()
        for row in rows:
            if row.state in terminal and row.evidence_id:
                continue
            if row.state in in_flight or (row.state not in terminal and row.state != BookAuditState.pending.value):
                row.state = BookAuditState.pending.value
                session.add(row)
                reset += 1
        session.commit()
    return reset


async def execute_claimed_v2_library_audit(
    *,
    cli: object,
    database_engine: Engine,
    run_id: str,
    owner: str,
    use_llm: bool,
    use_ocr: bool = True,
    use_vision: bool = False,
    allow_remote_text: bool = False,
    allow_remote_images: bool = False,
    settings: object | None = None,
    books: list[dict[str, Any]] | None = None,
    lease_ttl_seconds: int = DEFAULT_LEASE_TTL_SECONDS,
) -> object:
    """Run (or resume) a claimed V2 audit; releases the lease when finished."""
    from calibre_ai_auditor.verification.pipeline_v2 import AuditMode
    from calibre_ai_auditor.verification.service_v2 import build_v2_enricher, run_persisted_library_audit

    if not claim_verification_run(
        database_engine,
        run_id,
        owner=owner,
        ttl_seconds=lease_ttl_seconds,
    ):
        raise RuntimeError(f"could not claim verification run {run_id}")

    reset_stale_in_flight_states(database_engine, run_id)

    try:
        if not heartbeat_verification_run(
            database_engine,
            run_id,
            owner=owner,
            ttl_seconds=lease_ttl_seconds,
        ):
            raise RuntimeError(f"lost lease for verification run {run_id} before start")

        if settings is None:
            from calibre_ai_auditor.config.settings import load_settings

            settings = load_settings()

        enricher = build_v2_enricher(
            settings,  # type: ignore[arg-type]
            use_llm=use_llm,
            use_ocr=use_ocr,
            use_vision=use_vision,
            run_allows_remote_text=allow_remote_text,
            run_allows_remote_images=allow_remote_images,
        )
        result = await run_persisted_library_audit(
            cli=cli,
            database_engine=database_engine,
            run_id=run_id,
            limit=0,
            mode=AuditMode.shadow,
            evidence_enricher=enricher,
            use_llm=use_llm,
            books=books,
            settings=settings,  # type: ignore[arg-type]
        )
        return result
    except Exception:
        logger.exception("Durable V2 verify run %s failed under owner %s", run_id, owner)
        SQLAuditStore(database_engine, run_id).finish("failed")
        raise
    finally:
        release_verification_run(database_engine, run_id, owner=owner)


async def recover_orphaned_v2_runs(
    *,
    database_engine: Engine,
    settings: object,
    library_cli_factory: object | None = None,
) -> list[str]:
    """Claim and re-dispatch orphaned V2 runs. Returns run_ids scheduled."""
    import asyncio

    from calibre_ai_auditor.calibre.cli import CalibreCLI

    recoverable = list_recoverable_v2_run_ids(database_engine)
    scheduled: list[str] = []
    for run_id in recoverable:
        incomplete = incomplete_book_keys(database_engine, run_id)
        if not incomplete:
            # All books sealed; just mark finished if still open.
            store = SQLAuditStore(database_engine, run_id)
            with contextlib.suppress(ValueError):
                store.finish("completed")
            continue
        owner = new_worker_id("recover")
        if not claim_verification_run(database_engine, run_id, owner=owner):
            continue

        with Session(database_engine) as session:
            run = session.exec(select(VerificationRun).where(VerificationRun.run_id == run_id)).one()
            use_llm = bool(run.use_llm)

        lib_path = getattr(getattr(settings, "library", None), "path", None)
        if lib_path is None:
            SQLAuditStore(database_engine, run_id).finish("failed")
            release_verification_run(database_engine, run_id, owner=owner)
            continue

        cli = library_cli_factory(lib_path) if library_cli_factory is not None else CalibreCLI(lib_path)  # type: ignore[operator]

        async def _resume(
            rid: str = run_id,
            own: str = owner,
            llm: bool = use_llm,
            c: object = cli,
        ) -> None:
            try:
                await execute_claimed_v2_library_audit(
                    cli=c,
                    database_engine=database_engine,
                    run_id=rid,
                    owner=own,
                    use_llm=llm,
                    settings=settings,
                )
            except Exception:
                logger.exception("Recovery worker failed for V2 run %s", rid)

        asyncio.create_task(_resume())
        scheduled.append(run_id)
        logger.info("Scheduled recovery for orphaned V2 verify run %s", run_id)
    return scheduled

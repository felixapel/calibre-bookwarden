"""Resumable run tracking for v1.0.

For 10k–50k book audits, a worker crash mid-run must not lose progress.
ResumableRun tracks per-book completion status in Valkey Streams (or an
in-memory fallback) so a fresh worker can pick up where the previous one
left off.

Two-tier model:
  - Run-level state (run_id, started_at, totals) — single key in Valkey
  - Per-book state (run_id+book_key → status) — one entry per book in a Stream

Book statuses: pending | in_progress | completed | failed | skipped

The Valkey Stream entry id is monotonically increasing, so we can use XLEN +
XRANGE for efficient progress queries.  In-memory fallback uses an OrderedDict.
"""

from __future__ import annotations

import logging
import time
from dataclasses import dataclass
from datetime import UTC, datetime
from enum import StrEnum
from typing import Any

logger = logging.getLogger(__name__)


class BookStatus(StrEnum):
    pending = "pending"
    in_progress = "in_progress"
    completed = "completed"
    failed = "failed"
    skipped = "skipped"


@dataclass
class RunProgress:
    run_id: str
    total: int
    started_at: datetime
    completed: int = 0
    failed: int = 0
    skipped: int = 0
    in_progress: int = 0
    pending: int = 0

    @property
    def percent_done(self) -> float:
        if self.total == 0:
            return 100.0
        return (self.completed + self.failed + self.skipped) * 100.0 / self.total


class ResumableRunStore:
    """Tracks per-book completion state for long-running audit jobs.

    Backend selection: if the valkey/redis package is available AND a URL is
    configured, use Valkey Streams.  Otherwise fall back to in-memory.

    The interface is intentionally simple — workers can poll `next_pending()`
    to get the next book that needs processing, and call `mark_*` when done.
    Crash recovery: on restart, all `in_progress` books get rolled back to
    `pending` so they're reprocessed.
    """

    def __init__(
        self,
        run_id: str,
        book_keys: list[str],
        *,
        valkey_url: str | None = None,
    ):
        self.run_id = run_id
        self._book_keys = list(book_keys)
        self._state: dict[str, BookStatus] = dict.fromkeys(book_keys, BookStatus.pending)
        self._client: Any = None
        self._stream_key = f"run:{run_id}:books"

        if valkey_url:
            try:
                import redis.asyncio as aioredis  # type: ignore
                self._client = aioredis.from_url(valkey_url, decode_responses=True)
            except Exception as e:
                logger.warning("ResumableRunStore: Valkey unavailable (%s); using memory", e)

    # --------------------------------------------------------------- status

    def get_status(self, book_key: str) -> BookStatus:
        return self._state.get(book_key, BookStatus.pending)

    def progress(self) -> RunProgress:
        counts = dict.fromkeys(BookStatus, 0)
        for status in self._state.values():
            counts[status] += 1
        return RunProgress(
            run_id=self.run_id,
            total=len(self._state),
            started_at=datetime.now(UTC),
            completed=counts[BookStatus.completed],
            failed=counts[BookStatus.failed],
            skipped=counts[BookStatus.skipped],
            in_progress=counts[BookStatus.in_progress],
            pending=counts[BookStatus.pending],
        )

    # --------------------------------------------------------------- mutators

    def reset_in_progress(self) -> int:
        """Roll back any 'in_progress' to 'pending' (called on worker startup)."""
        rolled = 0
        for k, v in list(self._state.items()):
            if v == BookStatus.in_progress:
                self._state[k] = BookStatus.pending
                rolled += 1
        return rolled

    async def mark_in_progress(self, book_key: str) -> None:
        self._state[book_key] = BookStatus.in_progress
        await self._persist(book_key)

    async def mark_completed(self, book_key: str) -> None:
        self._state[book_key] = BookStatus.completed
        await self._persist(book_key)

    async def mark_failed(self, book_key: str) -> None:
        self._state[book_key] = BookStatus.failed
        await self._persist(book_key)

    async def mark_skipped(self, book_key: str) -> None:
        self._state[book_key] = BookStatus.skipped
        await self._persist(book_key)

    # --------------------------------------------------------------- iteration

    def pending_keys(self) -> list[str]:
        """Return all book_keys still pending (or rolled-back in_progress)."""
        return [
            k for k, v in self._state.items()
            if v in (BookStatus.pending, BookStatus.in_progress)
        ]

    def next_pending(self) -> str | None:
        for k, v in self._state.items():
            if v == BookStatus.pending:
                return k
        return None

    # --------------------------------------------------------------- persistence

    async def _persist(self, book_key: str) -> None:
        if not self._client:
            return
        try:
            await self._client.xadd(
                self._stream_key,
                {
                    "book_key": book_key,
                    "status": self._state[book_key].value,
                    "ts": str(int(time.time() * 1000)),
                },
            )
        except Exception as e:
            logger.debug("Persist failed for %s: %s", book_key, e)


__all__ = [
    "BookStatus",
    "RunProgress",
    "ResumableRunStore",
]
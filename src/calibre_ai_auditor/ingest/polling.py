import asyncio
import logging
from collections import OrderedDict
from pathlib import Path
from typing import Any

logger = logging.getLogger(__name__)


class PollingWatcher:
    def __init__(
        self,
        folders: list[Path],
        interval: int = 60,
        supported_extensions: list[str] | None = None,
        max_seen_files: int = 50_000,
    ):
        self.folders = folders
        self.interval = interval
        self.supported_extensions = supported_extensions or [
            ".epub",
            ".pdf",
            ".mobi",
            ".azw3",
        ]
        self.running = False
        # OrderedDict as an insertion-ordered set: `in` checks membership,
        # eviction drops the least-recently-seen entries first.
        self.seen_files: OrderedDict[Path, None] = OrderedDict()
        self.max_seen_files = max_seen_files

    def _remember(self, resolved_path: Path) -> None:
        """Record a seen file, evicting the oldest 10% when at capacity.

        Evicted files still on disk will re-fire the callback on a later
        tick (documented tradeoff); callbacks must stay idempotent.
        """
        if resolved_path in self.seen_files:
            self.seen_files.move_to_end(resolved_path)
            return
        if len(self.seen_files) >= self.max_seen_files:
            for _ in range(max(1, self.max_seen_files // 10)):
                self.seen_files.popitem(last=False)
            logger.warning("PollingWatcher seen-set full; evicted oldest entries")
        self.seen_files[resolved_path] = None

    async def start(self, callback: Any) -> None:
        """Starts the polling loop."""
        self.running = True
        logger.info(f"Starting polling watcher on {self.folders} (interval: {self.interval}s)")

        # Initial scan to populate seen files, preventing callback triggers on existing files
        for folder in self.folders:
            if not folder.exists():
                continue
            for file_path in folder.rglob("*"):
                if file_path.is_file() and file_path.suffix.lower() in self.supported_extensions:
                    self._remember(file_path.resolve())

        while self.running:
            await asyncio.sleep(self.interval)

            for folder in self.folders:
                if not folder.exists():
                    continue

                for file_path in folder.rglob("*"):
                    if file_path.is_file() and file_path.suffix.lower() in self.supported_extensions:
                        resolved_path = file_path.resolve()
                        if resolved_path not in self.seen_files:
                            self._remember(resolved_path)
                            try:
                                await callback(file_path)
                            except Exception:
                                logger.exception("PollingWatcher callback failed for %s", file_path)

    def stop(self) -> None:
        self.running = False

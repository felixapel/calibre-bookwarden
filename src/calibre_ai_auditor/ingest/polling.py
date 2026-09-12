import asyncio
import logging
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
        self.seen_files: set[Path] = set()
        self.max_seen_files = max_seen_files

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
                    self.seen_files.add(file_path.resolve())

        while self.running:
            await asyncio.sleep(self.interval)

            for folder in self.folders:
                if not folder.exists():
                    continue

                for file_path in folder.rglob("*"):
                    if file_path.is_file() and file_path.suffix.lower() in self.supported_extensions:
                        resolved_path = file_path.resolve()
                        if resolved_path not in self.seen_files:
                            if len(self.seen_files) >= self.max_seen_files:
                                logger.warning("PollingWatcher seen-set full; dropping oldest entries")
                                self.seen_files.clear()
                            self.seen_files.add(resolved_path)
                            try:
                                await callback(file_path)
                            except Exception:
                                logger.exception("PollingWatcher callback failed for %s", file_path)

    def stop(self) -> None:
        self.running = False

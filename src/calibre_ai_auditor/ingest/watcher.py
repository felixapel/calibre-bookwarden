import logging
from collections.abc import Callable, Coroutine
from pathlib import Path
from typing import Any

from calibre_ai_auditor.ingest.polling import PollingWatcher

logger = logging.getLogger(__name__)


class IngestWatcher:
    """
    Filesystem event watcher with fallback polling.
    """

    def __init__(
        self,
        folders: list[Path],
        interval: int = 10,
        supported_extensions: list[str] | None = None,
    ):
        self.folders = folders
        self.interval = interval
        self.supported_extensions = supported_extensions or [".epub", ".pdf", ".mobi", ".azw3"]
        self._watcher = PollingWatcher(
            folders=folders,
            interval=interval,
            supported_extensions=supported_extensions,
        )

    async def start(self, callback: Callable[[Path], Coroutine[Any, Any, None]]) -> None:
        """
        Starts watching the directories.
        """
        try:
            import asyncio

            from watchdog.events import FileSystemEventHandler
            from watchdog.observers import Observer

            class IngestHandler(FileSystemEventHandler):
                def __init__(
                    self,
                    loop: asyncio.AbstractEventLoop,
                    cb: Callable[[Path], Coroutine[Any, Any, None]],
                    exts: list[str],
                ):
                    self.loop = loop
                    self.cb = cb
                    self.exts = exts

                def on_created(self, event: Any) -> None:
                    if event.is_directory:
                        return
                    path = Path(event.src_path)
                    if path.suffix.lower() in self.exts:
                        asyncio.run_coroutine_threadsafe(self.cb(path), self.loop)

            loop = asyncio.get_running_loop()
            handler = IngestHandler(loop, callback, self.supported_extensions)
            observer = Observer()
            for folder in self.folders:
                if folder.exists():
                    observer.schedule(handler, str(folder), recursive=True)
            observer.start()
            logger.info(f"Started watchdog directory observer on {self.folders}")

            while observer.is_alive():
                await asyncio.sleep(self.interval)
        except ImportError:
            logger.info("watchdog library not found. Falling back to PollingWatcher.")
            await self._watcher.start(callback)

    def stop(self) -> None:
        if self._watcher:
            self._watcher.stop()

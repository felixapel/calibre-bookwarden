import json
import logging
import subprocess
from pathlib import Path
from typing import Any, cast

logger = logging.getLogger(__name__)


class CalibreCLIError(Exception):
    """Raised when a Calibre CLI command fails."""

    pass


class CalibreCLI:
    def __init__(self, library_path: Path | None = None):
        self.library_path = library_path

    def _run_command(
        self, cmd: list[str], capture_output: bool = True, timeout: float = 30.0
    ) -> subprocess.CompletedProcess[str]:
        try:
            logger.debug(f"Running Calibre command: {' '.join(cmd)}")
            return subprocess.run(
                cmd, capture_output=capture_output, text=True, check=True, timeout=timeout
            )
        except subprocess.TimeoutExpired as e:
            logger.error(f"Calibre command timed out after {timeout}s: {' '.join(cmd)}")
            raise CalibreCLIError(f"Command '{' '.join(cmd)}' timed out.") from e
        except subprocess.CalledProcessError as e:
            logger.error(f"Calibre command failed: {e.stderr}")
            msg = f"Command '{' '.join(cmd)}' failed with exit code {e.returncode}: {e.stderr}"
            raise CalibreCLIError(msg) from e
        except FileNotFoundError as e:
            logger.error(f"Calibre tool not found: {cmd[0]}")
            msg = f"Calibre tool '{cmd[0]}' not found. Is Calibre installed?"
            raise CalibreCLIError(msg) from e

    def _extract_json(self, text: str) -> str:
        """Finds the first '[' or '{' and returns everything from there to the end."""
        start_idx = -1
        for i, char in enumerate(text):
            if char in ("[", "{"):
                start_idx = i
                break
        if start_idx == -1:
            return ""
        return text[start_idx:]

    def list_books(self, search: str | None = None) -> list[dict[str, Any]]:
        """Wraps 'calibredb list --for-machine'."""
        cmd = ["calibredb", "list", "--for-machine", "--fields", "all"]
        if self.library_path:
            cmd.extend(["--with-library", str(self.library_path)])
        if search:
            cmd.extend(["--search", search])

        result = self._run_command(cmd)
        json_part = self._extract_json(result.stdout)
        if not json_part:
             return []
        return cast(list[dict[str, Any]], json.loads(json_part))

    def show_metadata(self, book_id: int) -> dict[str, Any]:
        """Wraps 'calibredb show_metadata --as-opf'."""
        cmd = [
            "calibredb",
            "list",
            "--for-machine",
            "--fields",
            "all",
            "--search",
            f"id:{book_id}",
        ]
        if self.library_path:
            cmd.extend(["--with-library", str(self.library_path)])

        result = self._run_command(cmd)
        json_part = self._extract_json(result.stdout)
        if not json_part:
             raise CalibreCLIError(f"Book with ID {book_id} not found (no JSON output).")
        books = cast(list[dict[str, Any]], json.loads(json_part))
        if not books:
            raise CalibreCLIError(f"Book with ID {book_id} not found.")
        return books[0]

    def get_embedded_metadata(self, file_path: Path) -> str:
        """Wraps 'ebook-meta' to read metadata as OPF or similar."""
        cmd = ["ebook-meta", str(file_path)]
        result = self._run_command(cmd)
        return result.stdout

    def export_opf(self, book_id: int, output_path: Path) -> None:
        """Wraps 'calibredb export_metadata --as-opf'."""
        cmd = ["calibredb", "show_metadata", "--as-opf", str(book_id)]
        if self.library_path:
            cmd.extend(["--with-library", str(self.library_path)])

        result = self._run_command(cmd)
        with open(output_path, "w") as f:
            f.write(result.stdout)

    def set_metadata(self, book_id: int, opf_path: Path) -> None:
        """Wraps 'calibredb set_metadata --from-opf'."""
        cmd = ["calibredb", "set_metadata", str(book_id), str(opf_path)]
        if self.library_path:
            cmd.extend(["--with-library", str(self.library_path)])

        self._run_command(cmd)

    def extract_cover(self, file_path: Path, output_path: Path) -> None:
        """Wraps 'ebook-meta --get-cover'."""
        logger.info(f"Extracting cover from {file_path}...")
        cmd = ["ebook-meta", str(file_path), "--get-cover", str(output_path)]
        self._run_command(cmd, timeout=20.0)

    def fetch_metadata(
        self,
        title: str | None = None,
        authors: list[str] | None = None,
        isbn: str | None = None,
    ) -> str:
        """Wraps 'fetch-ebook-metadata'."""
        logger.info("Fetching metadata via Calibre fetch-ebook-metadata...")
        cmd = ["fetch-ebook-metadata", "--opf"]
        if title:
            cmd.extend(["--title", title])
        if authors:
            cmd.extend(["--author", ",".join(authors)])
        if isbn:
            cmd.extend(["--isbn", isbn])

        result = self._run_command(cmd, timeout=60.0)
        return result.stdout

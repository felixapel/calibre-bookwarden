import hashlib
import json
import logging
import re
import subprocess
import xml.etree.ElementTree as ET
from pathlib import Path
from typing import Any, cast

from calibre_ai_auditor.security.files import copy_file_beneath, write_bytes_beneath

logger = logging.getLogger(__name__)

DC = "http://purl.org/dc/elements/1.1/"
OPF = "http://www.idpf.org/2007/opf"


class CalibreCLIError(Exception):
    """Raised when a Calibre CLI command fails."""

    pass


class CalibreCLI:
    def __init__(self, library_path: Path | None = None):
        self.library_path = library_path

    def _run_command(
        self,
        cmd: list[str],
        capture_output: bool = True,
        timeout: float = 30.0,
        pass_fds: tuple[int, ...] = (),
    ) -> subprocess.CompletedProcess[str]:
        try:
            logger.debug(f"Running Calibre command: {' '.join(cmd)}")
            return subprocess.run(
                cmd,
                capture_output=capture_output,
                text=True,
                check=True,
                timeout=timeout,
                pass_fds=pass_fds,
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

    def export_opf(self, book_id: int, output_path: Path) -> str:
        """Wraps 'calibredb export_metadata --as-opf'."""
        cmd = ["calibredb", "show_metadata", "--as-opf", str(book_id)]
        if self.library_path:
            cmd.extend(["--with-library", str(self.library_path)])

        result = self._run_command(cmd)
        payload = result.stdout.encode()
        write_bytes_beneath(output_path.parent, output_path, payload)
        return hashlib.sha256(payload).hexdigest()

    def set_metadata(self, book_id: int, opf_path: Path) -> None:
        """Apply a full OPF snapshot, including clearing absent optional fields."""
        missing_fields = self._missing_optional_opf_fields(opf_path)
        if missing_fields:
            clear_cmd = ["calibredb", "set_metadata", str(book_id)]
            for field in missing_fields:
                clear_cmd.extend(["--field", f"{field}:"])
            if self.library_path:
                clear_cmd.extend(["--with-library", str(self.library_path)])
            self._run_command(clear_cmd)
        cmd = ["calibredb", "set_metadata", str(book_id), str(opf_path)]
        if self.library_path:
            cmd.extend(["--with-library", str(self.library_path)])

        self._run_command(cmd)

    def set_metadata_from_fd(self, book_id: int, descriptor: int) -> None:
        """Apply OPF bytes from an inherited, already-verified descriptor."""
        opf_path = Path(f"/proc/self/fd/{descriptor}")
        missing_fields = self._missing_optional_opf_fields(opf_path)
        if missing_fields:
            clear_cmd = ["calibredb", "set_metadata", str(book_id)]
            for field in missing_fields:
                clear_cmd.extend(["--field", f"{field}:"])
            if self.library_path:
                clear_cmd.extend(["--with-library", str(self.library_path)])
            self._run_command(clear_cmd)
        cmd = ["calibredb", "set_metadata", str(book_id), str(opf_path)]
        if self.library_path:
            cmd.extend(["--with-library", str(self.library_path)])
        self._run_command(cmd, pass_fds=(descriptor,))

    def list_metadata_fields(self) -> set[str]:
        """Return fields accepted by ``calibredb set_metadata --field``."""
        cmd = ["calibredb", "set_metadata", "--list-fields"]
        if self.library_path:
            cmd.extend(["--with-library", str(self.library_path)])
        result = self._run_command(cmd)
        return {
            token for line in result.stdout.splitlines() for token in re.findall(r"#?[A-Za-z_][A-Za-z0-9_-]*", line)
        }

    def custom_columns(self) -> set[str]:
        cmd = ["calibredb", "custom_columns"]
        if self.library_path:
            cmd.extend(["--with-library", str(self.library_path)])
        result = self._run_command(cmd)
        labels: set[str] = set()
        for line in result.stdout.splitlines():
            labels.update(match.lstrip("#") for match in re.findall(r"#?[A-Za-z_][A-Za-z0-9_-]*", line))
        return labels

    def set_custom(self, book_id: int, column: str, value: str) -> None:
        label = column.strip().lstrip("#")
        if not label or label not in self.custom_columns():
            raise CalibreCLIError(f"Required custom column #{label or '?'} is unavailable")
        cmd = ["calibredb", "set_custom", label, str(book_id), value]
        if self.library_path:
            cmd.extend(["--with-library", str(self.library_path)])
        self._run_command(cmd)

    def set_cover(self, book_id: int, cover_path: Path) -> None:
        if "cover" not in self.list_metadata_fields():
            raise CalibreCLIError("This Calibre version does not expose the cover metadata field")
        cmd = ["calibredb", "set_metadata", str(book_id), "--field", f"cover:{cover_path}"]
        if self.library_path:
            cmd.extend(["--with-library", str(self.library_path)])
        self._run_command(cmd)

    def set_cover_from_fd(self, book_id: int, descriptor: int) -> None:
        """Apply cover bytes from an inherited, already-verified descriptor."""
        if "cover" not in self.list_metadata_fields():
            raise CalibreCLIError("This Calibre version does not expose the cover metadata field")
        cover_path = f"/proc/self/fd/{descriptor}"
        cmd = ["calibredb", "set_metadata", str(book_id), "--field", f"cover:{cover_path}"]
        if self.library_path:
            cmd.extend(["--with-library", str(self.library_path)])
        self._run_command(cmd, pass_fds=(descriptor,))

    def export_cover(self, book_id: int, output_path: Path) -> str | None:
        """Copy the current library cover to a rollback artifact, if present."""
        metadata = self.show_metadata(book_id)
        raw_path = metadata.get("cover")
        if not isinstance(raw_path, str) or not raw_path.strip():
            return None
        source = Path(raw_path)
        source_root = self.library_path if self.library_path is not None else source.parent
        try:
            return copy_file_beneath(
                source_root,
                source,
                output_path,
                max_bytes=20 * 1024 * 1024,
                target_root=output_path.parent,
            )
        except (OSError, RuntimeError) as exc:
            raise CalibreCLIError("Calibre returned an unsafe cover path") from exc

    @staticmethod
    def _missing_optional_opf_fields(opf_path: Path) -> list[str]:
        metadata = ET.parse(opf_path).getroot().find(f"{{{OPF}}}metadata")
        if metadata is None:
            raise CalibreCLIError(f"OPF metadata element is missing: {opf_path}")
        missing: list[str] = []
        for calibre_field, dc_tag in (
            ("publisher", "publisher"),
            ("pubdate", "date"),
            ("languages", "language"),
            ("identifiers", "identifier"),
        ):
            if metadata.find(f"{{{DC}}}{dc_tag}") is None:
                missing.append(calibre_field)
        meta_names = {element.get("name") for element in metadata.findall(f"{{{OPF}}}meta")}
        if "calibre:series" not in meta_names:
            missing.append("series")
        return missing

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

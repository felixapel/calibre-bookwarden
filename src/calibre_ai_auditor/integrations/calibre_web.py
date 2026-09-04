"""Calibre-Web integration and thumbnail cache invalidator.

Purges /config/thumbnails/* and triggers graceful reload/restart so newly updated
metadata and covers immediately reflect in the Calibre-Web frontend without stale
caching or visual glitches.
"""

from __future__ import annotations

import logging
import os
import shutil
import subprocess
from pathlib import Path

logger = logging.getLogger(__name__)


class CalibreWebIntegration:
    def __init__(
        self,
        thumbnails_dir: Path | str | None = None,
        container_name: str = "calibre-web-automated",
        ssh_host: str | None = None,
        ssh_key: str | None = None,
    ):
        self.thumbnails_dir = Path(thumbnails_dir) if thumbnails_dir else None
        self.container_name = container_name
        self.ssh_host = ssh_host
        self.ssh_key = ssh_key

    def purge_local_thumbnails(self) -> int:
        """Purges local thumbnail directory if directly mounted."""
        if not self.thumbnails_dir or not self.thumbnails_dir.exists():
            return 0
        count = 0
        for item in self.thumbnails_dir.glob("*"):
            try:
                if item.is_file():
                    item.unlink()
                    count += 1
                elif item.is_dir():
                    shutil.rmtree(item, ignore_errors=True)
                    count += 1
            except Exception as e:
                logger.warning(f"Could not delete thumbnail {item}: {e}")
        logger.info(f"Purged {count} cached thumbnails locally.")
        return count

    def purge_and_reload_remote(self) -> bool:
        """Purges thumbnails inside container and restarts Calibre-Web via Docker or SSH."""
        cmd_str = (
            f"docker exec -i {self.container_name} sh -c 'rm -rf /config/thumbnails/*' "
            f"&& docker restart {self.container_name}"
        )
        if self.ssh_host:
            full_cmd = ["ssh"]
            if self.ssh_key:
                full_cmd.extend(["-i", self.ssh_key])
            full_cmd.extend([self.ssh_host, cmd_str])
        else:
            full_cmd = ["sh", "-c", cmd_str] if os.name != "nt" else ["cmd", "/c", cmd_str]

        try:
            logger.info(f"Triggering Calibre-Web cache purge and reload on {self.container_name}...")
            res = subprocess.run(full_cmd, capture_output=True, text=True, timeout=30)
            if res.returncode == 0:
                logger.info("Calibre-Web thumbnail cache purged and service restarted successfully.")
                return True
            logger.warning(f"Calibre-Web reload returned non-zero code {res.returncode}: {res.stderr}")
            return False
        except Exception as exc:
            logger.warning(f"Could not reload Calibre-Web: {exc}")
            return False

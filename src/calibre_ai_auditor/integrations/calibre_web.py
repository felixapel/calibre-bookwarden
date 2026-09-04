"""Calibre-Web integration and thumbnail cache invalidator.

Purges /config/thumbnails/* and triggers graceful reload/restart so newly updated
metadata and covers immediately reflect in the Calibre-Web frontend without stale
caching or visual glitches.
"""

from __future__ import annotations

import logging
import re
import shutil
import subprocess
from pathlib import Path

logger = logging.getLogger(__name__)

_CONTAINER_RE = re.compile(r"^[a-zA-Z0-9][a-zA-Z0-9_.-]{1,127}$")
_HOST_RE = re.compile(r"^[a-zA-Z0-9._-]+(@[a-zA-Z0-9._-]+)?$")


class CalibreWebIntegration:
    def __init__(
        self,
        thumbnails_dir: Path | str | None = None,
        container_name: str = "calibre-web-automated",
        ssh_host: str | None = None,
        ssh_key: str | None = None,
    ):
        if not _CONTAINER_RE.fullmatch(container_name):
            raise ValueError(f"Invalid container name: {container_name}")
        if ssh_host and not _HOST_RE.fullmatch(ssh_host):
            raise ValueError(f"Invalid SSH host format: {ssh_host}")

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
        """Purges thumbnails inside container and restarts Calibre-Web safely."""
        try:
            logger.info(f"Triggering Calibre-Web cache purge on {self.container_name}...")
            if self.ssh_host:
                ssh_args = ["ssh"]
                if self.ssh_key:
                    ssh_args.extend(["-i", self.ssh_key])
                ssh_args.extend(
                    [
                        self.ssh_host,
                        (
                            f"docker exec {self.container_name} rm -rf /config/thumbnails/* && "
                            f"docker restart {self.container_name}"
                        ),
                    ]
                )
                res = subprocess.run(ssh_args, capture_output=True, text=True, timeout=30)
                return res.returncode == 0

            # Safe local execution without host shell invocation
            purge_cmd = [
                "docker",
                "exec",
                self.container_name,
                "sh",
                "-c",
                "rm -rf /config/thumbnails/*",
            ]
            restart_cmd = ["docker", "restart", self.container_name]

            res_purge = subprocess.run(purge_cmd, capture_output=True, text=True, timeout=15)
            if res_purge.returncode != 0:
                logger.warning(
                    f"Purge thumbnails command returned non-zero code {res_purge.returncode}: {res_purge.stderr}"
                )

            res_restart = subprocess.run(restart_cmd, capture_output=True, text=True, timeout=15)
            if res_restart.returncode == 0:
                logger.info("Calibre-Web thumbnail cache purged and service restarted successfully.")
                return True
            logger.warning(f"Calibre-Web restart returned non-zero code {res_restart.returncode}: {res_restart.stderr}")
            return False
        except Exception as exc:
            logger.warning(f"Could not reload Calibre-Web: {exc}")
            return False

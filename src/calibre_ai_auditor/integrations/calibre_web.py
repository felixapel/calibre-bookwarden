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
import urllib.error
import urllib.request
from pathlib import Path
from typing import Any

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


class CalibreWebSyncManager(CalibreWebIntegration):
    """High-availability zero-downtime synchronization manager for Calibre-Web."""

    def __init__(
        self,
        calibre_web_url: str = "http://calibre-web:8083",
        thumbnails_dir: Path | str | None = None,
        container_name: str = "calibre-web-automated",
        ssh_host: str | None = None,
        ssh_key: str | None = None,
    ):
        super().__init__(
            thumbnails_dir=thumbnails_dir,
            container_name=container_name,
            ssh_host=ssh_host,
            ssh_key=ssh_key,
        )
        self.calibre_web_url = calibre_web_url.rstrip("/")

    def trigger_reconnect(self, timeout: float = 10.0) -> bool:
        """Instructs Calibre-Web to reload its SQLAlchemy database session via /reconnect."""
        url = f"{self.calibre_web_url}/reconnect"
        try:
            req = urllib.request.Request(url, headers={"User-Agent": "CalibreAIAuditor/2.0"})
            with urllib.request.urlopen(req, timeout=timeout) as resp:
                if resp.status in (200, 302):
                    logger.info(f"Calibre-Web /reconnect trigger succeeded (HTTP {resp.status})")
                    return True
        except Exception as exc:
            logger.debug(f"HTTP reconnect call failed: {exc}")
        return False

    def reload_workers_sighup(self) -> bool:
        """Sends SIGHUP to Gunicorn/Tornado workers for zero-downtime hot-reload."""
        try:
            hup_cmd = "pkill -HUP -f 'cps.py' || kill -HUP 1"
            if self.ssh_host:
                ssh_args = ["ssh"]
                if self.ssh_key:
                    ssh_args.extend(["-i", self.ssh_key])
                ssh_args.extend([self.ssh_host, f"docker exec {self.container_name} sh -c \"{hup_cmd}\""])
                res = subprocess.run(ssh_args, capture_output=True, text=True, timeout=15)
                return res.returncode == 0

            cmd = ["docker", "exec", self.container_name, "sh", "-c", hup_cmd]
            res = subprocess.run(cmd, capture_output=True, text=True, timeout=15)
            return res.returncode == 0
        except Exception as exc:
            logger.warning(f"Failed to send SIGHUP to {self.container_name}: {exc}")
            return False

    def invalidate_book_thumbnails(self, book_ids: list[int]) -> int:
        """Selectively removes thumbnail cache for given book IDs without wiping all cache."""
        if not book_ids:
            return 0
        count = 0
        if self.thumbnails_dir and self.thumbnails_dir.exists():
            for bid in book_ids:
                for pattern in [f"{bid}.*", f"book_{bid}.*"]:
                    for f in self.thumbnails_dir.glob(pattern):
                        try:
                            f.unlink()
                            count += 1
                        except Exception:
                            pass
            return count

        # Inside container
        patterns = " ".join([f"/config/thumbnails/{bid}.* /config/cache/{bid}.*" for bid in book_ids])
        rm_cmd = f"rm -f {patterns}"
        try:
            if self.ssh_host:
                ssh_args = ["ssh"]
                if self.ssh_key:
                    ssh_args.extend(["-i", self.ssh_key])
                ssh_args.extend([self.ssh_host, f"docker exec {self.container_name} sh -c \"{rm_cmd}\""])
                res = subprocess.run(ssh_args, capture_output=True, text=True, timeout=15)
                return len(book_ids) if res.returncode == 0 else 0

            cmd = ["docker", "exec", self.container_name, "sh", "-c", rm_cmd]
            res = subprocess.run(cmd, capture_output=True, text=True, timeout=15)
            return len(book_ids) if res.returncode == 0 else 0
        except Exception as exc:
            logger.warning(f"Failed to invalidate book thumbnails inside container: {exc}")
            return 0

    def sync_hot_reload(self, book_ids: list[int] | None = None) -> dict[str, Any]:
        """Attempts zero-downtime hot-reload; falls back to container restart if necessary."""
        if book_ids:
            self.invalidate_book_thumbnails(book_ids)
        else:
            self.purge_local_thumbnails()

        # 1. Try /reconnect HTTP trigger
        http_ok = self.trigger_reconnect()

        # 2. Try SIGHUP worker reload
        sighup_ok = self.reload_workers_sighup()

        if http_ok or sighup_ok:
            logger.info("Calibre-Web successfully refreshed via zero-downtime hot-reload.")
            return {"success": True, "method": "hot_reload", "http_reconnect": http_ok, "sighup": sighup_ok}

        # 3. Fallback to container restart
        logger.info("Hot-reload unavailable or failed; falling back to graceful container restart.")
        restart_ok = self.purge_and_reload_remote()
        return {"success": restart_ok, "method": "container_restart"}

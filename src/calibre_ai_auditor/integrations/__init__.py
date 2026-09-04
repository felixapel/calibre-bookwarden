"""External service integrations (Calibre-Web, headless workers, reverse proxies)."""

from calibre_ai_auditor.integrations.calibre_web import (
    CalibreWebIntegration,
    CalibreWebSyncManager,
)

__all__ = [
    "CalibreWebIntegration",
    "CalibreWebSyncManager",
]

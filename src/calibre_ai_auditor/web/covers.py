import logging
from pathlib import Path

logger = logging.getLogger(__name__)


def resolve_covers_dir(artifacts_dir: Path) -> Path:
    """Return a writable covers directory, falling back to `.artifacts/covers` locally."""
    preferred = artifacts_dir / "covers"
    try:
        preferred.mkdir(parents=True, exist_ok=True)
        return preferred
    except OSError as exc:
        fallback = Path(".artifacts/covers")
        logger.warning(
            "Cannot use %s for cover storage (%s); using %s instead",
            preferred,
            exc,
            fallback,
        )
        fallback.mkdir(parents=True, exist_ok=True)
        return fallback

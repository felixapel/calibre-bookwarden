import hmac
import os

from fastapi import HTTPException, Request

from calibre_ai_auditor.config.settings import Settings


def verify_paperless_webhook(request: Request, settings: Settings) -> None:
    """
    When PAPERLESS_WEBHOOK_SECRET (or configured env) is set, require matching header.
    Fail-closed in production: missing secret while webhooks are enabled is a 503.
    """
    env_name = settings.paperless.webhook_secret_env
    expected = os.getenv(env_name, "").strip()
    if not expected:
        if settings.profile == "production":
            raise HTTPException(status_code=503, detail="Webhook authentication is not configured")
        return

    provided = (
        request.headers.get("X-Webhook-Secret")
        or request.headers.get("X-Paperless-Webhook-Secret")
        or request.headers.get("Authorization", "").removeprefix("Bearer ").strip()
    )
    try:
        # compare_digest requires ASCII-only str; non-ASCII header bytes
        # (latin-1 decoded) must fail closed, not raise an unhandled 500.
        valid = bool(provided) and hmac.compare_digest(provided.encode("ascii"), expected.encode("ascii"))
    except (UnicodeEncodeError, TypeError):
        valid = False
    if not valid:
        raise HTTPException(status_code=401, detail="Invalid or missing webhook secret")

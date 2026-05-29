import os

from fastapi import HTTPException, Request

from calibre_ai_auditor.config.settings import Settings


def verify_paperless_webhook(request: Request, settings: Settings) -> None:
    """
    When PAPERLESS_WEBHOOK_SECRET (or configured env) is set, require matching header.
    """
    env_name = settings.paperless.webhook_secret_env
    expected = os.getenv(env_name, "").strip()
    if not expected:
        return

    provided = (
        request.headers.get("X-Webhook-Secret")
        or request.headers.get("X-Paperless-Webhook-Secret")
        or request.headers.get("Authorization", "").removeprefix("Bearer ").strip()
    )
    if provided != expected:
        raise HTTPException(status_code=401, detail="Invalid or missing webhook secret")

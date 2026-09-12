import logging
from pathlib import Path
from typing import Any

import httpx
from jinja2 import Environment, FileSystemLoader

logger = logging.getLogger(__name__)


class PreviewRenderer:
    def __init__(self, enabled: bool = False, url: str = "http://gotenberg:3000"):
        self.enabled = enabled
        self.url = url.rstrip("/")

        # Setup Jinja2 environment (autoescape ON: book metadata is untrusted)
        template_dir = Path(__file__).parent / "templates"
        self.jinja_env = Environment(loader=FileSystemLoader(template_dir), autoescape=True)

    async def render_html(self, template_name: str, context: dict[str, Any]) -> str:
        """Renders an HTML template using Jinja2."""
        template = self.jinja_env.get_template(template_name)
        return template.render(**context)

    async def render_pdf(self, html_content: str) -> bytes | None:
        """Calls Gotenberg to convert HTML to PDF."""
        if not self.enabled:
            return None

        url = f"{self.url}/forms/chromium/convert/html"
        files = {"index.html": ("index.html", html_content.encode("utf-8"), "text/html")}

        try:
            async with httpx.AsyncClient(timeout=30.0) as client:
                response = await client.post(url, files=files)
                response.raise_for_status()
                return response.content
        except Exception as e:
            logger.error(f"Failed to render PDF via Gotenberg: {e}")
            return None

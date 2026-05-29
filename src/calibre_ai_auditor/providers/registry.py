import logging

from calibre_ai_auditor.calibre.cli import CalibreCLI
from calibre_ai_auditor.providers.base import BaseProvider
from calibre_ai_auditor.providers.calibre_fetch import CalibreFetchProvider
from calibre_ai_auditor.providers.google_books import GoogleBooksProvider
from calibre_ai_auditor.providers.openlibrary import OpenLibraryProvider

logger = logging.getLogger(__name__)


class ProviderRegistry:
    def __init__(self, cli: CalibreCLI, order: list[str] | None = None):
        self.cli = cli
        self.order = order or ["calibre_fetch", "openlibrary", "google_books"]
        self.providers: dict[str, BaseProvider] = {
            "google_books": GoogleBooksProvider(),
            "openlibrary": OpenLibraryProvider(),
            "calibre_fetch": CalibreFetchProvider(cli),
        }

    def get_providers(self) -> list[BaseProvider]:
        """Returns the list of providers in the configured order."""
        return [self.providers[name] for name in self.order if name in self.providers]

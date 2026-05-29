from abc import ABC, abstractmethod
from typing import Any

from calibre_ai_auditor.storage.models import Candidate, Metadata


class BaseProvider(ABC):
    @property
    @abstractmethod
    def name(self) -> str:
        pass

    @abstractmethod
    async def fetch_candidates(
        self, title: str | None = None, authors: list[str] | None = None, isbn: str | None = None
    ) -> list[Candidate]:
        pass

    @abstractmethod
    def normalize_metadata(self, raw_data: Any) -> Metadata:
        """Helper to convert provider-specific data to our common Metadata model."""
        pass

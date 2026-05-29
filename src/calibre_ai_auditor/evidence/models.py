from datetime import datetime
from enum import StrEnum
from typing import Any

from pydantic import BaseModel, Field


class EvidenceSourceType(StrEnum):
    book_content = "book_content"
    cover = "cover"
    embedded_metadata = "embedded_metadata"
    current_calibre = "current_calibre"
    external_provider = "external_provider"
    llm_inference = "llm_inference"
    filename = "filename"
    file_system = "file_system"
    user_override = "user_override"


class EvidenceItem(BaseModel):
    id: str
    book_key: str
    field: str
    value: Any
    source_type: EvidenceSourceType
    source_name: str
    priority: int
    confidence: int
    quote_or_reason: str | None = None
    page_range: str | None = None
    provider_url: str | None = None
    raw: Any | None = None
    risk_flags: list[str] = Field(default_factory=list)
    created_at: datetime = Field(default_factory=datetime.utcnow)


class ResolvedField(BaseModel):
    field: str
    selected_value: Any
    selected_source: EvidenceItem | None = None
    confidence: int = 0
    alternatives: list[EvidenceItem] = Field(default_factory=list)
    reason: str | None = None
    requires_review: bool = False
    risk_flags: list[str] = Field(default_factory=list)


class MetadataResolution(BaseModel):
    book_key: str
    resolved_fields: dict[str, ResolvedField] = Field(default_factory=dict)
    proposed_patch: dict[str, Any] = Field(default_factory=dict)
    risk_flags: list[str] = Field(default_factory=list)
    overall_confidence: int = 0
    recommended_action: str = "no_change"  # no_change | suggest_fix | needs_review | defer
    created_at: datetime = Field(default_factory=datetime.utcnow)

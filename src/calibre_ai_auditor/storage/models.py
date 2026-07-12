from datetime import UTC, datetime
from typing import Any

from pydantic import BaseModel
from sqlmodel import JSON, Column, Field, SQLModel


def utc_now() -> datetime:
    return datetime.now(UTC)


class Metadata(BaseModel):
    title: str | None = None
    authors: list[str] = []
    publisher: str | None = None
    published_date: str | None = None
    language: str | None = None
    series: str | None = None
    series_index: float | None = None
    # Comic/Manga specific (v1.1 vision support)
    volume: int | None = None
    chapter: float | None = None  # decimal per Weebarr convention
    series_position: float | None = None
    identifiers: dict[str, str] = {}
    tags: list[str] = []


class BookFile(BaseModel):
    path: str
    format: str
    size_bytes: int | None = None
    sha256: str | None = None


class Run(SQLModel, table=True):
    id: int | None = Field(default=None, primary_key=True)
    run_id: str = Field(index=True, unique=True)
    created_at: datetime = Field(default_factory=utc_now)
    status: str = "started"  # started, completed, failed
    metadata_filter: str | None = None


class BookRecord(SQLModel, table=True):
    id: int | None = Field(default=None, primary_key=True)
    book_key: str = Field(index=True, unique=True)  # e.g., calibre:123 or path:/foo/bar
    run_id: str = Field(index=True)
    calibre_book_id: int | None = Field(default=None, index=True)
    paperless_document_id: int | None = Field(default=None, index=True)
    source: str = "calibre"  # calibre, direct_path
    discovered_at: datetime = Field(default_factory=utc_now)

    # We use Column(JSON) for complex nested data in SQLModel
    files: list[dict[str, Any]] = Field(default_factory=list, sa_column=Column(JSON))
    current_metadata: dict[str, Any] = Field(default_factory=dict, sa_column=Column(JSON))
    field_locks: dict[str, Any] = Field(default_factory=dict, sa_column=Column(JSON))
    status: str = "scanned"  # scanned, audited, needs_review, safe, error, applied, undone


class Candidate(BaseModel):
    candidate_id: str
    provider: str  # calibre_fetch, openlibrary, google_books, manual
    provider_url: str | None = None
    metadata: Metadata
    cover_url: str | None = None
    raw_score: float | None = None


class Snippet(BaseModel):
    source: str  # title_page, copyright_page, toc, first_pages, ocr
    text: str
    page_range: str | None = None


class EvidencePackage(SQLModel, table=True):
    id: int | None = Field(default=None, primary_key=True)
    evidence_id: str = Field(index=True, unique=True)
    book_key: str = Field(index=True)
    run_id: str = Field(index=True)
    created_at: datetime = Field(default_factory=utc_now)

    current: dict[str, Any] = Field(default_factory=dict, sa_column=Column(JSON))
    extracted: dict[str, Any] = Field(default_factory=dict, sa_column=Column(JSON))
    candidates: list[dict[str, Any]] = Field(default_factory=list, sa_column=Column(JSON))
    snippets: list[dict[str, Any]] = Field(default_factory=list, sa_column=Column(JSON))
    cover: dict[str, Any] | None = Field(default=None, sa_column=Column(JSON))
    risk_flags: list[str] = Field(default_factory=list, sa_column=Column(JSON))
    decision: dict[str, Any] | None = Field(default=None, sa_column=Column(JSON))


class Change(SQLModel, table=True):
    id: int | None = Field(default=None, primary_key=True)
    book_key: str = Field(index=True)
    run_id: str = Field(index=True)
    applied_at: datetime = Field(default_factory=utc_now)

    before_metadata: dict[str, Any] = Field(default_factory=dict, sa_column=Column(JSON))
    after_metadata: dict[str, Any] = Field(default_factory=dict, sa_column=Column(JSON))
    backup_opf_path: str
    # pending_apply is committed before the external write. Failure states keep
    # enough audit evidence to reconcile or restore after a process crash.
    status: str = "applied"  # pending_apply, applied, failed_rolled_back, failed_rollback_failed, undone


class CoverVisionCache(SQLModel, table=True):
    id: int | None = Field(default=None, primary_key=True)
    sha256: str | None = Field(default=None, index=True)
    phash: str | None = Field(default=None, index=True)
    response: dict[str, Any] = Field(default_factory=dict, sa_column=Column(JSON))
    created_at: datetime = Field(default_factory=utc_now)

from datetime import UTC, datetime
from typing import Any

from pydantic import BaseModel
from sqlalchemy import CheckConstraint, UniqueConstraint
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
    schema_version: int = 1

    current: dict[str, Any] = Field(default_factory=dict, sa_column=Column(JSON))
    extracted: dict[str, Any] = Field(default_factory=dict, sa_column=Column(JSON))
    candidates: list[dict[str, Any]] = Field(default_factory=list, sa_column=Column(JSON))
    snippets: list[dict[str, Any]] = Field(default_factory=list, sa_column=Column(JSON))
    cover: dict[str, Any] | None = Field(default=None, sa_column=Column(JSON))
    risk_flags: list[str] = Field(default_factory=list, sa_column=Column(JSON))
    decision: dict[str, Any] | None = Field(default=None, sa_column=Column(JSON))
    observations: dict[str, Any] | None = Field(default=None, sa_column=Column(JSON))


class Change(SQLModel, table=True):
    __table_args__ = (UniqueConstraint("operation_id", name="uq_change_operation_id"),)

    id: int | None = Field(default=None, primary_key=True)
    operation_id: str | None = Field(default=None, index=True)
    book_key: str = Field(index=True)
    run_id: str = Field(index=True)
    applied_at: datetime = Field(default_factory=utc_now)

    before_metadata: dict[str, Any] = Field(default_factory=dict, sa_column=Column(JSON))
    after_metadata: dict[str, Any] = Field(default_factory=dict, sa_column=Column(JSON))
    backup_opf_path: str
    backup_opf_sha256: str | None = None
    backup_cover_path: str | None = None
    backup_cover_sha256: str | None = None
    before_custom: dict[str, Any] = Field(default_factory=dict, sa_column=Column(JSON))
    # pending_apply is committed before the external write. Failure states keep
    # enough audit evidence to reconcile or restore after a process crash.
    status: str = "applied"  # pending_apply, applied, failed_rolled_back, failed_rollback_failed, undone


class OperationLedger(SQLModel, table=True):
    """Durable state machine for an externally visible metadata operation."""

    __table_args__ = (UniqueConstraint("idempotency_key", name="uq_operationledger_idempotency_key"),)

    id: int | None = Field(default=None, primary_key=True)
    operation_id: str = Field(index=True, unique=True)
    idempotency_key: str
    operation_type: str = Field(index=True)
    book_key: str = Field(index=True)
    run_id: str | None = Field(default=None, index=True)
    calibre_book_id: int | None = Field(default=None, index=True)
    state: str = Field(default="requested", index=True)
    requested_patch: dict[str, Any] = Field(default_factory=dict, sa_column=Column(JSON))
    expected_before_metadata: dict[str, Any] | None = Field(default=None, sa_column=Column(JSON))
    before_metadata: dict[str, Any] | None = Field(default=None, sa_column=Column(JSON))
    target_metadata: dict[str, Any] | None = Field(default=None, sa_column=Column(JSON))
    observed_metadata: dict[str, Any] | None = Field(default=None, sa_column=Column(JSON))
    authorization_id: str | None = Field(default=None, index=True)
    verdict_hash: str | None = None
    patch_hash: str | None = None
    field_locks_hash: str | None = None
    policy_version: str = "v1"
    change_id: int | None = Field(default=None, index=True)
    rollback_opf_path: str | None = None
    rollback_opf_sha256: str | None = None
    rollback_cover_path: str | None = None
    rollback_cover_sha256: str | None = None
    rollback_custom: dict[str, Any] = Field(default_factory=dict, sa_column=Column(JSON))
    evidence_id: str | None = Field(default=None, index=True)
    pilot_id: str | None = Field(default=None, index=True)
    lease_owner: str | None = Field(default=None, index=True)
    lease_expires_at: datetime | None = Field(default=None, index=True)
    error: str | None = None
    created_at: datetime = Field(default_factory=utc_now)
    updated_at: datetime = Field(default_factory=utc_now)
    completed_at: datetime | None = None


class OperationIncidentAcknowledgement(SQLModel, table=True):
    """Append-only operator evidence that one safe terminal V2 failure was reviewed."""

    __table_args__ = (
        CheckConstraint(
            "length(trim(actor)) BETWEEN 1 AND 128",
            name="ck_incident_ack_actor_length",
        ),
        CheckConstraint(
            "length(trim(reason)) BETWEEN 12 AND 1000",
            name="ck_incident_ack_reason_length",
        ),
    )

    operation_id: str = Field(
        primary_key=True,
        foreign_key="operationledger.operation_id",
    )
    actor: str
    reason: str
    created_at: datetime = Field(default_factory=utc_now)


class PilotSession(SQLModel, table=True):
    """Immutable runtime binding and monotonically consumed V2 canary budget."""

    __table_args__ = (
        CheckConstraint("max_operations BETWEEN 1 AND 5", name="ck_pilotsession_max_operations"),
        CheckConstraint(
            "reserved_operations BETWEEN 0 AND max_operations",
            name="ck_pilotsession_reserved_operations",
        ),
        CheckConstraint(
            "state IN ('open', 'stopped', 'completed')",
            name="ck_pilotsession_state",
        ),
    )

    pilot_id: str = Field(primary_key=True)
    library_root_sha256: str = Field(index=True)
    release_digest: str
    alembic_revision: str
    max_operations: int = 5
    reserved_operations: int = 0
    state: str = Field(default="open", index=True)
    created_at: datetime = Field(default_factory=utc_now)
    updated_at: datetime = Field(default_factory=utc_now)


class BookWriteLock(SQLModel, table=True):
    """One durable active writer lease per immutable Calibre book identity."""

    book_key: str = Field(primary_key=True)
    operation_id: str = Field(index=True, unique=True)
    lease_owner: str = Field(index=True)
    lease_expires_at: datetime = Field(index=True)


class OutboxEvent(SQLModel, table=True):
    """Transactionally persisted event awaiting delivery to a worker."""

    id: int | None = Field(default=None, primary_key=True)
    event_id: str = Field(index=True, unique=True)
    aggregate_id: str = Field(index=True)
    event_type: str = Field(index=True)
    payload: dict[str, Any] = Field(default_factory=dict, sa_column=Column(JSON))
    status: str = Field(default="pending", index=True)
    attempts: int = 0
    available_at: datetime = Field(default_factory=utc_now, index=True)
    created_at: datetime = Field(default_factory=utc_now)
    published_at: datetime | None = None
    last_error: str | None = None


class VerificationRun(SQLModel, table=True):
    """Durable progress and aggregate counts for a verification run."""

    id: int | None = Field(default=None, primary_key=True)
    run_id: str = Field(index=True, unique=True)
    status: str = Field(default="running", index=True)
    started_at: datetime = Field(default_factory=utc_now)
    finished_at: datetime | None = None
    total: int = 0
    completed: int = 0
    counts: dict[str, int] = Field(default_factory=dict, sa_column=Column(JSON))
    use_llm: bool = False
    pipeline_version: str = "v1"
    mode: str = "legacy"


class VerificationResult(SQLModel, table=True):
    """One immutable per-book verdict belonging to a verification run."""

    __table_args__ = (UniqueConstraint("run_id", "book_key", name="uq_verificationresult_run_book"),)

    id: int | None = Field(default=None, primary_key=True)
    result_id: str = Field(index=True, unique=True)
    run_id: str = Field(index=True)
    book_key: str = Field(index=True)
    verdict: dict[str, Any] = Field(default_factory=dict, sa_column=Column(JSON))
    evidence_id: str | None = Field(default=None, index=True)
    state: str = Field(default="pending", index=True)
    created_at: datetime = Field(default_factory=utc_now)


class ManualAuthorization(SQLModel, table=True):
    """Immutable operator approval bound to one exact verdict patch."""

    id: int | None = Field(default=None, primary_key=True)
    authorization_id: str = Field(index=True, unique=True)
    book_key: str = Field(index=True)
    run_id: str = Field(index=True)
    verdict_hash: str = Field(index=True)
    patch_hash: str = Field(index=True)
    actor: str
    reason: str
    created_at: datetime = Field(default_factory=utc_now)


class CoverVisionCache(SQLModel, table=True):
    id: int | None = Field(default=None, primary_key=True)
    sha256: str | None = Field(default=None, index=True)
    phash: str | None = Field(default=None, index=True)
    response: dict[str, Any] = Field(default_factory=dict, sa_column=Column(JSON))
    created_at: datetime = Field(default_factory=utc_now)

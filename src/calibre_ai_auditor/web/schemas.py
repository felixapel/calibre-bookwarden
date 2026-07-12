from typing import Any

from pydantic import BaseModel, Field


class InspectRequest(BaseModel):
    path: str
    no_providers: bool = False


class ReviewRequest(BaseModel):
    patch: dict[str, Any] | None = None
    force: bool = False


class UndoRequest(BaseModel):
    force: bool = False


class LockFieldRequest(BaseModel):
    field: str
    value: Any


class ApplyRequest(BaseModel):
    force: bool = False
    authorization_ids: dict[str, str] = Field(default_factory=dict)


class ManualAuthorizationRequest(BaseModel):
    actor: str
    reason: str


class RevertRequest(BaseModel):
    force: bool = False


class APIResponse(BaseModel):
    status: str
    data: Any | None = None

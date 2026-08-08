"""Dependency providers shared only by the Certificate A application."""

from __future__ import annotations

from collections.abc import Callable, Generator
from typing import Annotated, cast

from fastapi import Depends, Request
from sqlalchemy.engine import Engine
from sqlmodel import Session

from calibre_ai_auditor.config.settings import Settings


def get_production_settings(request: Request) -> Settings:
    provider = cast(Callable[[], Settings], request.app.state.settings_provider)
    return provider()


def get_production_engine(
    request: Request,
    settings: Annotated[Settings, Depends(get_production_settings)],
) -> Engine:
    provider = cast(Callable[[Settings], Engine], request.app.state.engine_provider)
    return provider(settings)


def get_production_session(
    engine: Annotated[Engine, Depends(get_production_engine)],
) -> Generator[Session, None, None]:
    with Session(engine) as session:
        yield session

import tempfile
from pathlib import Path
from typing import Any
from unittest.mock import AsyncMock, MagicMock, patch

import pytest
from sqlalchemy import inspect
from sqlmodel import Session, SQLModel, create_engine

import calibre_ai_auditor.storage.db as db_module
from calibre_ai_auditor.config.settings import Settings
from calibre_ai_auditor.extractors.cover_hashes import calculate_phash
from calibre_ai_auditor.llm.schemas import LLMResponse
from calibre_ai_auditor.ocr.vision import VisionVerifier, calculate_sha256
from calibre_ai_auditor.storage.models import CoverVisionCache


@pytest.mark.asyncio
async def test_vision_verifier_success(tmp_path: Any) -> None:
    db_module._engine = None

    settings = Settings()
    db_file = tmp_path / "test_vision_success.db"
    settings.storage.sqlite_path = db_file
    settings.vision_model = "test-vision-model"

    # Create dummy cover image
    with tempfile.TemporaryDirectory() as tmp_dir:
        cover_path = Path(tmp_dir) / "cover.jpg"
        cover_path.write_bytes(b"fake image data success")

        # Mock the provider and router
        mock_provider = MagicMock()
        mock_provider.supports_vision = True
        mock_provider.name = "mock-vision"

        mock_router_instance = MagicMock()
        mock_router_instance.get_provider_for_task.return_value = mock_provider

        # Mock execute_structured to return a successful parsing response
        mock_response = LLMResponse(
            content={
                "title": "Mocked Book Title",
                "authors": ["Author One"],
                "publisher": "Mock Publisher",
                "isbn": "9781234567890",
                "confidence": 0.95,
                "explanation": "Extracted from cover",
            }
        )
        mock_router_instance.execute_structured = AsyncMock(return_value=mock_response)

        with patch("calibre_ai_auditor.ocr.vision.LLMRouter", return_value=mock_router_instance):
            verifier = VisionVerifier(settings)
            result = await verifier.verify_cover(cover_path)

            assert result is not None
            assert result["title"] == "Mocked Book Title"
            assert result["authors"] == ["Author One"]
            assert result["isbn"] == "9781234567890"
            assert result["confidence"] == 0.95

    assert "covervisioncache" not in inspect(create_engine(f"sqlite:///{db_file}")).get_table_names()

    db_module._engine = None


@pytest.mark.asyncio
async def test_vision_verifier_unsupported(tmp_path: Any) -> None:
    db_module._engine = None

    settings = Settings()
    db_file = tmp_path / "test_vision_unsupported.db"
    settings.storage.sqlite_path = db_file

    with tempfile.TemporaryDirectory() as tmp_dir:
        cover_path = Path(tmp_dir) / "cover.jpg"
        cover_path.write_bytes(b"fake image data unsupported")

        mock_provider = MagicMock()
        mock_provider.supports_vision = False
        mock_provider.name = "mock-text-only"

        mock_router_instance = MagicMock()
        mock_router_instance.get_provider_for_task.return_value = mock_provider

        with patch("calibre_ai_auditor.ocr.vision.LLMRouter", return_value=mock_router_instance):
            verifier = VisionVerifier(settings)
            result = await verifier.verify_cover(cover_path)
            assert result is None

    db_module._engine = None


@pytest.mark.asyncio
async def test_vision_verifier_cache(tmp_path: Any) -> None:
    # Reset engine
    db_module._engine = None

    settings = Settings()
    db_file = tmp_path / "test_vision_cache.db"
    settings.storage.sqlite_path = db_file

    # Initialize temporary database
    engine = create_engine(f"sqlite:///{db_file}")
    SQLModel.metadata.create_all(engine)

    # Create dummy cover image
    cover_path = tmp_path / "cover.jpg"
    cover_path.write_bytes(b"fake cached cover image data")

    sha256 = calculate_sha256(cover_path)
    phash = calculate_phash(cover_path)

    cached_data = {
        "title": "Cached Book Title",
        "authors": ["Cached Author"],
        "publisher": "Cached Publisher",
        "isbn": "9789999999999",
        "confidence": 1.0,
        "explanation": "From Cache",
    }

    with Session(engine) as session:
        cache_entry = CoverVisionCache(sha256=sha256, phash=phash, response=cached_data)
        session.add(cache_entry)
        session.commit()

    verifier = VisionVerifier(settings)
    # This should hit the database cache and succeed without calling LLMRouter
    result = await verifier.verify_cover(cover_path)

    assert result is not None
    assert result["title"] == "Cached Book Title"
    assert result["authors"] == ["Cached Author"]
    assert result["isbn"] == "9789999999999"

    # Reset engine after test
    db_module._engine = None

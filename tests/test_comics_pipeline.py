from unittest.mock import AsyncMock, MagicMock, patch

import pytest

from calibre_ai_auditor.comics.pipeline import enrich_comic_observations
from calibre_ai_auditor.config.settings import MangaProviders, MangaSettings, Settings
from calibre_ai_auditor.storage.models import Candidate, Metadata


@pytest.mark.asyncio
async def test_comic_pipeline_reads_komf_candidate_metadata() -> None:
    settings = Settings()
    settings.manga_mode = MangaSettings(enabled=True, providers=MangaProviders(komf=True))
    candidate = Candidate(
        candidate_id="komf-series-42",
        provider="komf",
        metadata=Metadata(
            title="Exact Manga",
            series="Exact Series",
            volume=2,
            chapter=3.5,
            series_position=2.0,
        ),
    )
    provider = MagicMock()
    provider.fetch_candidates = AsyncMock(return_value=[candidate])
    registry = MagicMock()
    registry.providers = {"komf": provider}

    with patch("calibre_ai_auditor.providers.registry.ProviderRegistry", return_value=registry) as registry_cls:
        declared, observed = await enrich_comic_observations(
            settings,
            book_path=None,
            cover_path=None,
            declared={"title": "Exact Manga", "authors": ["Exact Author"]},
            observed={},
        )

    expected = {
        "series": "Exact Series",
        "volume": 2,
        "chapter": 3.5,
        "series_position": 2.0,
    }
    assert {field: declared[field] for field in expected} == expected
    assert {field: observed[field] for field in expected} == expected
    assert registry_cls.call_args.kwargs == {"enable_komf": True}

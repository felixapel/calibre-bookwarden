from unittest.mock import AsyncMock, MagicMock, patch

import pytest

from calibre_ai_auditor.providers.registry import KomfProvider, ProviderRegistry


def _async_client(response: MagicMock) -> MagicMock:
    client = MagicMock()
    client.post = AsyncMock(return_value=response)
    client.__aenter__ = AsyncMock(return_value=client)
    client.__aexit__ = AsyncMock(return_value=None)
    return client


def test_registry_omits_komf_by_default() -> None:
    registry = ProviderRegistry(MagicMock())

    assert "komf" not in registry.providers
    assert "komf" not in [provider.name for provider in registry.get_providers()]


def test_registry_includes_komf_only_when_explicitly_enabled() -> None:
    registry = ProviderRegistry(MagicMock(), enable_komf=True)

    assert registry.providers["komf"].name == "komf"
    assert registry.get_providers()[-1].name == "komf"


@pytest.mark.asyncio
async def test_komf_returns_no_candidates_when_all_endpoints_fail() -> None:
    response = MagicMock(status_code=503)

    with patch("httpx.AsyncClient", return_value=_async_client(response)):
        candidates = await KomfProvider().fetch_candidates(title="Exact Manga")

    assert candidates == []


@pytest.mark.parametrize(
    "payload",
    [
        {"candidates": [None, "invalid", {"id": "empty"}]},
        {"candidates": "invalid", "title": "must not be accepted"},
    ],
)
@pytest.mark.asyncio
async def test_komf_ignores_malformed_success_payloads(payload: object) -> None:
    response = MagicMock(status_code=200)
    response.json.return_value = payload

    with patch("httpx.AsyncClient", return_value=_async_client(response)):
        candidates = await KomfProvider().fetch_candidates(title="Exact Manga")

    assert candidates == []


@pytest.mark.asyncio
async def test_komf_normalizes_valid_candidates() -> None:
    response = MagicMock(status_code=200)
    response.json.return_value = {
        "candidates": [
            {
                "id": "series-42",
                "url": "http://komf.local/series/42",
                "title": "Exact Manga",
                "authors": ["Exact Author"],
                "series": "Exact Series",
                "volume": 2,
                "chapter": "3.5",
                "series_position": 2.0,
                "identifiers": {"anilist": "42"},
            }
        ]
    }

    with patch("httpx.AsyncClient", return_value=_async_client(response)):
        candidates = await KomfProvider(base_url="http://komf.local").fetch_candidates(title="Exact Manga")

    assert len(candidates) == 1
    candidate = candidates[0]
    assert candidate.candidate_id == "komf-series-42"
    assert candidate.provider_url == "http://komf.local/series/42"
    assert candidate.metadata.series == "Exact Series"
    assert candidate.metadata.volume == 2
    assert candidate.metadata.chapter == 3.5
    assert candidate.metadata.series_position == 2.0
    assert candidate.metadata.identifiers == {"anilist": "42"}

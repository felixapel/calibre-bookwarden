from typing import Any
from unittest.mock import AsyncMock

import pytest

from calibre_ai_auditor.config.settings import load_settings
from calibre_ai_auditor.judge.engine import MetadataJudge
from calibre_ai_auditor.llm.schemas import LLMResponse
from calibre_ai_auditor.storage.models import EvidencePackage


@pytest.mark.asyncio
async def test_judge_basic(monkeypatch: Any) -> None:
    settings = load_settings()
    judge = MetadataJudge(settings)

    # Mock LLMRouter.execute_structured
    verdict = {
        "recommended_action": "suggest_fix",
        "confidence": 90,
        "reasons": ["test"],
        "proposed_patch": {"title": "Fixed Title"},
        "same_work_confidence": 100,
        "same_edition_confidence": 100,
        "selected_candidate_ids": [],
        "risk_flags": [],
    }

    mock_response = LLMResponse(content=verdict)
    mock_execute = AsyncMock(return_value=mock_response)
    monkeypatch.setattr(
        "calibre_ai_auditor.judge.engine.LLMRouter.execute_structured", mock_execute
    )

    package = EvidencePackage(
        evidence_id="test_ev",
        book_key="test_book",
        run_id="test_run",
        current={"title": "Old Title"},
        extracted={},
        candidates=[],
        snippets=[],
    )

    result = await judge.judge(package)

    assert result["recommended_action"] == "suggest_fix"
    assert result["proposed_patch"]["title"] == "Fixed Title"
    mock_execute.assert_called_once()

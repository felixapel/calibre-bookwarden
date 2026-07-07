"""Pre-rendered JSON fixtures for the v1.0 WebUI E2E suite.

These are the exact payloads the WebUI receives from the FastAPI backend.
The Playwright tests use them via api-mock.ts to avoid needing a real backend.

The shapes match src/calibre_ai_auditor/verification/verdict.py exactly.
"""

from __future__ import annotations

import json
from pathlib import Path

# This file is the Python companion to webui/e2e/helpers/fixtures.ts.
# When the Python-side BookVerdict model changes, regenerate this file:

OUT_PATH = Path(__file__).parent.parent.parent / "webui" / "e2e" / "fixtures" / "verdicts.json"


def build_fixtures() -> dict:
    """Build the canonical v1.0 verdict fixtures for WebUI E2E."""
    return {
        "confirmed": {
            "book_key": "calibre:1",
            "run_id": "test_run",
            "field_verdicts": {
                "title": {
                    "field": "title",
                    "declared_value": "The Great Gatsby",
                    "observed_value": "The Great Gatsby",
                    "verdict": "confirmed",
                    "confidence": 99,
                    "evidence": [
                        {"source": "title_page", "text": "The Great Gatsby", "page_range": "1", "confidence": 99}
                    ],
                    "requires_review": False,
                    "risk_flags": [],
                    "reason": "Titles match exactly.",
                    "is_deterministic": True,
                    "created_at": "2026-07-05T10:00:00Z",
                }
            },
            "overall_confidence": 99,
            "risk_flags": [],
            "action": "no_change",
            "auto_apply_eligible": False,
            "proposed_patch": {},
            "reasons": ["[title] Titles match exactly."],
            "created_at": "2026-07-05T10:00:00Z",
        },
        "mismatch": {
            "book_key": "calibre:2",
            "run_id": "test_run",
            "field_verdicts": {
                "title": {
                    "field": "title",
                    "declared_value": "WRONG TITLE",
                    "observed_value": "Some Real Book",
                    "verdict": "mismatch",
                    "confidence": 95,
                    "evidence": [
                        {"source": "title_page", "text": "Some Real Book", "page_range": "1", "confidence": 95}
                    ],
                    "requires_review": False,
                    "risk_flags": [],
                    "reason": "Titles differ significantly.",
                    "is_deterministic": True,
                    "created_at": "2026-07-05T10:00:00Z",
                }
            },
            "overall_confidence": 95,
            "risk_flags": [],
            "action": "suggest_fix",
            "auto_apply_eligible": True,
            "proposed_patch": {"title": "Some Real Book"},
            "reasons": ["[title] Titles differ significantly."],
            "created_at": "2026-07-05T10:00:00Z",
        },
        "author_swap": {
            "book_key": "calibre:3",
            "run_id": "test_run",
            "field_verdicts": {
                "authors": {
                    "field": "authors",
                    "declared_value": ["Wrong Author"],
                    "observed_value": ["Right Author"],
                    "verdict": "mismatch",
                    "confidence": 92,
                    "evidence": [
                        {"source": "title_page", "text": "By Right Author", "page_range": "1", "confidence": 92}
                    ],
                    "requires_review": True,
                    "risk_flags": ["author_swap"],
                    "reason": "Author swap detected.",
                    "is_deterministic": True,
                    "created_at": "2026-07-05T10:00:00Z",
                }
            },
            "overall_confidence": 92,
            "risk_flags": ["author_swap"],
            "action": "needs_review",
            "auto_apply_eligible": False,
            "proposed_patch": {"authors": ["Right Author"]},
            "reasons": ["[authors] Author swap detected."],
            "created_at": "2026-07-05T10:00:00Z",
        },
        "ambiguous": {
            "book_key": "calibre:4",
            "run_id": "test_run",
            "field_verdicts": {
                "title": {
                    "field": "title",
                    "declared_value": "The Novel",
                    "observed_value": "The Novels",
                    "verdict": "ambiguous",
                    "confidence": 60,
                    "evidence": [],
                    "requires_review": True,
                    "risk_flags": [],
                    "reason": "Title similarity 0.62 is ambiguous; needs LLM adjudication.",
                    "is_deterministic": True,
                    "created_at": "2026-07-05T10:00:00Z",
                }
            },
            "overall_confidence": 60,
            "risk_flags": [],
            "action": "needs_review",
            "auto_apply_eligible": False,
            "proposed_patch": {},
            "reasons": ["[title] Ambiguous match."],
            "created_at": "2026-07-05T10:00:00Z",
        },
    }


def main() -> int:
    OUT_PATH.parent.mkdir(parents=True, exist_ok=True)
    OUT_PATH.write_text(json.dumps(build_fixtures(), indent=2))
    print(f"Wrote {OUT_PATH}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())

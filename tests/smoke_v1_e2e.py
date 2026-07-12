"""Smoke test: run the v1.0 ContentVerificationEngine end-to-end on a real EPUB.

Skips Calibre CLI (this environment doesn't have it) — exercises:
  - Extract text from a real EPUB
  - Build Observations
  - Run ContentVerificationEngine
  - Print the BookVerdict

Run with:
    source .venv/bin/activate
    python tests/smoke_v1_e2e.py /path/to/real/book.epub
"""

from __future__ import annotations

import json
import sys
from pathlib import Path

from calibre_ai_auditor.extractors.heuristics import extract_heuristics
from calibre_ai_auditor.extractors.text import extract_snippets
from calibre_ai_auditor.verification.engine import (
    ContentVerificationEngine,
    DeclaredMetadata,
    ObservationSet,
)


def main() -> int:
    if len(sys.argv) < 2:
        print("usage: smoke_v1_e2e.py <path-to-epub-or-pdf>")
        return 2
    book_path = Path(sys.argv[1])
    if not book_path.exists():
        print(f"file not found: {book_path}")
        return 2

    print(f"== smoke test on {book_path.name} ==")

    # 1. Extract snippets from real file
    snippets = extract_snippets(book_path)
    snippet_dicts = [{"text": s.text, "source": s.source} for s in snippets]
    print(f"extracted {len(snippets)} snippet(s); total chars: {sum(len(s.text) for s in snippets)}")

    # 2. Heuristics from snippets
    heuristics = extract_heuristics(snippet_dicts)
    print(f"heuristics: {heuristics}")

    # 3. For demonstration: pretend Calibre declares WRONG metadata and watch the
    # engine catch it.  Real declared metadata would come from calibredb.
    declared = DeclaredMetadata(
        title="WRONG TITLE FOR TEST",
        authors=["Wrong Author"],
        isbn="0000000000000",
        publisher="Made-up Publisher",
        language="eng",
    )

    # 4. Build observations from real extraction
    observed = ObservationSet(
        title_page_text=snippets[0].text if snippets else None,
        copyright_page_text=snippets[0].text if snippets else None,
        body_sample=snippets[0].text if snippets else None,
        isbn_extracted=(heuristics.get("identifiers") or {}).get("isbn"),
        title_extracted=heuristics.get("title"),
        authors_extracted=heuristics.get("authors") or [],
        evidence_quality="high" if snippets and len(snippets[0].text) > 1000 else "low",
    )

    # 5. Run engine
    engine = ContentVerificationEngine()
    verdict = engine.verify(
        book_key=f"smoke:{book_path.name}",
        run_id="smoke_run",
        declared=declared,
        observed=observed,
    )

    print()
    print(f"action:           {verdict.action.value}")
    print(f"overall_conf:     {verdict.overall_confidence}")
    print(f"risk_flags:       {verdict.risk_flags}")
    print(f"auto_apply:       {verdict.auto_apply_eligible}")
    print(f"proposed_patch:   {json.dumps(verdict.proposed_patch, indent=2)}")
    print()
    print("per-field verdicts:")
    for fname, fv in verdict.field_verdicts.items():
        print(
            f"  {fname:18s} {fv.verdict.value:10s} "
            f"conf={fv.confidence:3d}  "
            f"declared={fv.declared_value!r:35s} "
            f"observed={fv.observed_value!r}"
        )
    print()
    print("reasons:")
    for r in verdict.reasons:
        print(f"  - {r}")
    return 0


if __name__ == "__main__":
    sys.exit(main())

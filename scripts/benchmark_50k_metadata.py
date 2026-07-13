"""Reproducible metadata-only 50k scale gate (no OCR, providers, or LLM)."""

import json
import platform
import resource
import time

from calibre_ai_auditor.verification.engine import ContentVerificationEngine, DeclaredMetadata, ObservationSet


def main() -> None:
    engine = ContentVerificationEngine()
    declared = DeclaredMetadata(
        title="A Deterministic Book",
        authors=["Example Author"],
        publisher="Example Press",
        published_date="2020-01-01",
        language="en",
        isbn="9780306406157",
    )
    observed = ObservationSet(
        title_page_text="A Deterministic Book by Example Author",
        body_sample="deterministic metadata-only sample " * 20,
        title_extracted="A Deterministic Book",
        authors_extracted=["Example Author"],
        isbn_extracted="9780306406157",
        publisher_extracted="Example Press",
        date_extracted="2020-01-01",
        language_detected="en",
        evidence_quality="high",
    )
    total = 50_000
    started = time.perf_counter()
    for index in range(total):
        engine.verify(
            book_key=f"benchmark:{index}",
            run_id="metadata-50k",
            declared=declared,
            observed=observed,
        )
    elapsed = time.perf_counter() - started
    rss_kib = resource.getrusage(resource.RUSAGE_SELF).ru_maxrss
    result = {
        "scope": "verification-engine metadata only; excludes scan IO, OCR, providers, LLM, and persistence",
        "books": total,
        "elapsed_seconds": round(elapsed, 3),
        "books_per_second": round(total / elapsed, 2),
        "max_rss_mib": round(rss_kib / 1024, 2),
        "python": platform.python_version(),
        "platform": platform.platform(),
    }
    print(json.dumps(result, indent=2, sort_keys=True))
    if elapsed >= 1800 or rss_kib >= 2 * 1024 * 1024:
        raise SystemExit("50k metadata-only scale gate failed")


if __name__ == "__main__":
    main()

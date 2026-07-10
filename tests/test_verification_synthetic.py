"""Regression backbone: every synthetic fixture must produce the gold-truth verdict."""

from __future__ import annotations

import pytest
from pathlib import Path

from calibre_ai_auditor.verification.engine import (
    ContentVerificationEngine,
    DeclaredMetadata,
    ObservationSet,
)
from calibre_ai_auditor.verification.rules import (
    verify_chapter,
    verify_series_position,
    verify_volume,
)
from calibre_ai_auditor.verification.verdict import VerdictKind
from tests.fixtures.gold_truth import VERIFICATION_CONTRACT
from tests.fixtures.synthetic_library.gold_truth import SYNTHETIC_FIXTURES


def _build_declared(field_entry: dict) -> DeclaredMetadata:
    """Build DeclaredMetadata from a fixture field entry's declared value."""
    fields = {}
    for entry in field_entry["fields"]:
        fields[entry["field"]] = entry["declared"]
    return DeclaredMetadata(
        title=fields.get("title"),
        authors=fields.get("authors") or [],
        publisher=fields.get("publisher"),
        published_date=fields.get("published_date"),
        language=fields.get("language"),
        series=fields.get("series"),
        series_index=fields.get("series_index"),
        # Comic fields (C1/C2) — .get for backward compat with existing fixtures
        volume=fields.get("volume"),
        chapter=fields.get("chapter"),
        series_position=fields.get("series_position"),
        isbn=fields.get("isbn"),
    )


def _build_observed(field_entry: dict) -> ObservationSet:
    """Build ObservationSet from a fixture field entry's observed value."""
    fields = {}
    for entry in field_entry["fields"]:
        fields[entry["field"]] = entry["observed"]

    # Headers / copyright / body — synthetic fixtures may not include raw text,
    # so we leave them None unless the fixture sets has_text_layer=False.
    has_text = field_entry.get("has_text_layer", True)

    # OCR quality defaults to "high" unless fixture sets "low" via ocr_quality field.
    evidence_quality = field_entry.get("ocr_quality", "high")
    if evidence_quality == "low":
        quality_str = "low"
    elif has_text:
        quality_str = "high"
    else:
        # Scanned PDF with no signal about OCR quality → assume medium unless overridden
        quality_str = field_entry.get("ocr_quality", "medium")

    return ObservationSet(
        title_page_text=("[placeholder title page text]" if has_text else None),
        copyright_page_text=("[placeholder copyright page]" if has_text else None),
        header_running_text=("[running header sample]" if field_entry.get("header_sampled") else None),
        body_sample=("[placeholder body sample]" if has_text else None),
        isbn_extracted=fields.get("isbn"),
        publisher_extracted=fields.get("publisher"),
        date_extracted=fields.get("published_date"),
        language_detected=fields.get("language"),
        title_extracted=fields.get("title"),
        authors_extracted=fields.get("authors"),
        series_extracted=fields.get("series"),
        series_index_extracted=fields.get("series_index"),
        # Comic fields (C1/C2)
        volume_extracted=fields.get("volume"),
        chapter_extracted=fields.get("chapter"),
        series_position_extracted=fields.get("series_position"),
        cover_phash_distance=field_entry.get("cover_phash_distance"),
        evidence_quality=quality_str,
    )


@pytest.fixture
def engine() -> ContentVerificationEngine:
    return ContentVerificationEngine()


@pytest.mark.parametrize(
    "fixture",
    SYNTHETIC_FIXTURES,
    ids=lambda f: f["id"],
)
def test_synthetic_fixture_matches_contract(fixture: dict, engine: ContentVerificationEngine) -> None:
    declared = _build_declared(fixture)
    observed = _build_observed(fixture)
    verdict = engine.verify(
        book_key=fixture["id"],
        run_id="test_run",
        declared=declared,
        observed=observed,
    )
    contract = VERIFICATION_CONTRACT[fixture["id"]]

    # 1. Overall action must match
    assert verdict.action.value == contract["overall_action"], (
        f"{fixture['id']}: expected action {contract['overall_action']}, "
        f"got {verdict.action.value}. Reasons: {verdict.reasons}"
    )

    # 2. Auto-apply eligibility must match
    assert verdict.auto_apply_eligible == contract["auto_apply"], (
        f"{fixture['id']}: auto_apply mismatch. "
        f"expected {contract['auto_apply']}, got {verdict.auto_apply_eligible}. "
        f"Risk flags: {verdict.risk_flags}, conf: {verdict.overall_confidence}"
    )

    # 3. Per-field verdicts must match (within contract tolerance)
    for field_name, field_contract in contract["per_field"].items():
        fv = verdict.field_verdicts.get(field_name)
        assert fv is not None, f"{fixture['id']}: missing field verdict for {field_name}"
        assert fv.verdict.value == field_contract["verdict"], (
            f"{fixture['id']}.{field_name}: expected verdict "
            f"{field_contract['verdict']}, got {fv.verdict.value}. "
            f"Reason: {fv.reason}"
        )
        assert fv.confidence >= field_contract["min_conf"], (
            f"{fixture['id']}.{field_name}: confidence "
            f"{fv.confidence} below contract minimum {field_contract['min_conf']}"
        )

    # 4. Required risk flags must be present
    if "risk_flags_present" in contract:
        for required_flag in contract["risk_flags_present"]:
            assert required_flag in verdict.risk_flags, (
                f"{fixture['id']}: required risk flag {required_flag!r} missing. Have: {verdict.risk_flags}"
            )


def test_fixture_count_matches_contract() -> None:
    """Sanity check: contract covers every fixture."""
    assert len(VERIFICATION_CONTRACT) == len(SYNTHETIC_FIXTURES)
    for fx in SYNTHETIC_FIXTURES:
        assert fx["id"] in VERIFICATION_CONTRACT, f"missing contract for {fx['id']}"


def test_comic_fields_in_declared_observed_no_breakage() -> None:
    """C2 coverage: comic rules (verify_volume/chapter/series_position) + decimal chapter + no breakage on engine."""
    declared = DeclaredMetadata(
        title="Some Manga",
        authors=["Author"],
        series="Some Series",
        series_index=1.0,
        volume=2,
        chapter=3.5,  # decimal per Weeb
        series_position=2.5,
        isbn="9780451524935",
    )
    observed = ObservationSet(
        title_extracted="Some Manga",
        authors_extracted=["Author"],
        series_extracted="Some Series",
        series_index_extracted=1.0,
        volume_extracted=2,
        chapter_extracted=3.5,
        series_position_extracted=2.5,
        isbn_extracted="9780451524935",
        evidence_quality="high",
    )
    # Attrs present and typed as expected
    assert declared.volume == 2
    assert declared.chapter == 3.5
    assert declared.series_position == 2.5
    assert observed.volume_extracted == 2
    assert observed.chapter_extracted == 3.5
    assert observed.series_position_extracted == 2.5
    assert observed.cover_vision is None

    engine = ContentVerificationEngine()
    verdict = engine.verify(
        book_key="comic-test:1",
        run_id="c2-test",
        declared=declared,
        observed=observed,
    )
    # C2 rules now active: comic numeric fields processed with tolerant decimal rules
    assert "title" in verdict.field_verdicts
    assert "series_index" in verdict.field_verdicts
    assert "volume" in verdict.field_verdicts
    assert "chapter" in verdict.field_verdicts
    assert "series_position" in verdict.field_verdicts
    # Matching decimals -> confirmed (and full match -> no_change)
    assert verdict.field_verdicts["volume"].verdict.value == "confirmed"
    assert verdict.field_verdicts["chapter"].verdict.value == "confirmed"
    assert verdict.field_verdicts["series_position"].verdict.value == "confirmed"
    assert verdict.action == "no_change"
    # No breakage


# ---------------------------------------------------------------------------
# C2 comic rules: explicit table-driven (parametrized) tests
# tolerant decimal chapter, None cases, match/mismatch, coercion
# ---------------------------------------------------------------------------


@pytest.mark.parametrize(
    "rule_func,declared,observed,expected_verdict,expected_conf,reason_sub",
    [
        # volume cases (int tolerant)
        (verify_volume, None, None, VerdictKind.confirmed, 90, "No volume"),
        (verify_volume, None, 2, VerdictKind.missing, 85, "Volume found"),
        (verify_volume, 3, None, VerdictKind.ambiguous, 50, "Volume declared"),
        (verify_volume, 1, 1, VerdictKind.confirmed, 95, "Volume matches"),
        (verify_volume, 1, 1.0, VerdictKind.confirmed, 95, "Volume matches"),
        (verify_volume, "2", 2, VerdictKind.confirmed, 95, "Volume matches"),
        (verify_volume, 2, 3, VerdictKind.mismatch, 90, "Volume mismatch"),
        # chapter decimal cases (tolerant)
        (verify_chapter, None, None, VerdictKind.confirmed, 90, "No chapter"),
        (verify_chapter, None, 12.5, VerdictKind.missing, 85, "Chapter found"),
        (verify_chapter, 5.0, None, VerdictKind.ambiguous, 50, "Chapter declared"),
        (verify_chapter, 1.5, 1.5, VerdictKind.confirmed, 95, "Chapter matches"),
        (verify_chapter, 1.5, 1.50, VerdictKind.confirmed, 95, "Chapter matches"),
        (verify_chapter, "3.25", 3.25, VerdictKind.confirmed, 95, "Chapter matches"),
        (verify_chapter, 10, 10.0, VerdictKind.confirmed, 95, "Chapter matches"),
        (verify_chapter, 1.0, 1.1, VerdictKind.mismatch, 90, "Chapter mismatch"),
        # series_position
        (verify_series_position, None, None, VerdictKind.confirmed, 90, "No series_position"),
        (verify_series_position, None, 1.0, VerdictKind.missing, 85, "Series position found"),
        (verify_series_position, 4.5, None, VerdictKind.ambiguous, 50, "Series position declared"),
        (verify_series_position, 2.5, 2.5, VerdictKind.confirmed, 95, "Series position matches"),
        (verify_series_position, 2.5, 2.51, VerdictKind.confirmed, 95, "Series position matches"),
        (verify_series_position, 0, 0.0, VerdictKind.confirmed, 95, "Series position matches"),
        (verify_series_position, 1, 2, VerdictKind.mismatch, 90, "Series position mismatch"),
    ],
    ids=None,
)
def test_comic_rules_table_driven(rule_func, declared, observed, expected_verdict, expected_conf, reason_sub) -> None:
    """Table-driven tests for C2 comic verify_* rules. decimal chapter, tolerant."""
    fv = rule_func(declared, observed)
    assert fv.verdict == expected_verdict
    assert fv.confidence == expected_conf
    assert reason_sub in (fv.reason or "")
    # field name correct
    assert fv.field in ("volume", "chapter", "series_position")
    # original values preserved in verdict
    assert fv.declared_value == declared
    # observed may be coerced in report but input kept


async def test_enrich_comic_pipeline_komf_and_vision_paths(tmp_path: Path) -> None:
    """Assert komf path and vision path are exercised in enrich_comic_observations (synthetic)."""
    from unittest.mock import AsyncMock, MagicMock, patch

    import calibre_ai_auditor.ocr.vision  # ensure submodule for patch
    from calibre_ai_auditor.comics.pipeline import enrich_comic_observations
    from calibre_ai_auditor.config.settings import MangaProviders, MangaSettings, Settings

    settings = Settings()
    settings.manga_mode = MangaSettings(enabled=True, providers=MangaProviders(komf=True))

    # prepare a fake cover_path to hit vision branch (direct, no book needed for this test)
    cover_path = tmp_path / "fake_cover.jpg"
    cover_path.write_bytes(b"\x00" * 10)  # exists

    base_decl = {"title": "Pipeline Test Manga", "authors": ["Test Author"], "series": None}
    base_obs: dict = {}

    # mocks
    mock_vision_res = {"volume": 2, "chapter": 5.5, "series": "Vision Series", "series_position": 2.0}
    mock_komf_cand = MagicMock()
    mock_komf_cand.volume = 99
    mock_komf_cand.chapter = 9.9
    mock_komf_cand.series_position = 99
    mock_komf_cand.series = "Komf Series"

    mock_komf = MagicMock()
    mock_komf.fetch_candidates = AsyncMock(return_value=[mock_komf_cand])

    with patch("calibre_ai_auditor.ocr.vision.verify_comic_cover", new_callable=AsyncMock) as mock_verify, \
         patch("calibre_ai_auditor.calibre.cli.CalibreCLI") as mock_cli, \
         patch("calibre_ai_auditor.providers.registry.ProviderRegistry") as mock_reg_cls:
        mock_verify.return_value = mock_vision_res
        mock_cli.return_value = MagicMock()
        reg = MagicMock()
        reg.providers = {"komf": mock_komf}
        mock_reg_cls.return_value = reg

        en_decl, en_obs = await enrich_comic_observations(settings, None, cover_path, base_decl, base_obs)

    # path assertions
    assert mock_verify.called, "vision path was not taken"
    assert mock_komf.fetch_candidates.called, "komf path was not taken"

    # results: vision provides (before komf), decimal chapter
    assert en_obs.get("cover_vision") == mock_vision_res
    assert en_decl.get("volume") == 2
    assert en_decl.get("chapter") == 5.5
    assert en_obs.get("series") == "Vision Series" or en_decl.get("series") == "Vision Series"

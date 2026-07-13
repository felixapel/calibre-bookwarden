# Testing Guide

`calibre-ai-auditor` v1.2.1 uses a 4-layer testing strategy: unit,
integration, E2E, and benchmark. The release-candidate gate currently covers
218 selected backend/integration tests plus the separate Komf integration gate,
and 42 desktop/mobile browser scenarios.

```
tests/
├── (root)             # 23 unit + integration tests
├── test_comics.py     # ComicInfo.xml parsing
├── fixtures/
│   ├── synthetic_library/   # 30 hand-crafted bad-metadata fixtures (gold truth)
│   └── gold_truth/          # Per-field contract for each fixture
├── benchmarks/        # 32 pytest-benchmark tests (with baseline.json)
├── calibration/       # 4 smoke tests for the calibration pipeline
├── web/               # 23 FastAPI test-client tests
└── cassettes/         # Recorded LLM responses for the witness tests
```

## 1. Unit tests (pytest)

Pure-function tests covering the v1.0 deterministic rules + helpers.

- **Coverage**: 8 verification rules (title / authors / isbn / publisher /
  date / language / series / series_index), heuristics extraction, config
  parsing, REST API endpoints.
- **Run**:
  ```bash
  source .venv/bin/activate
  pytest -m "not benchmark and not ocr_live and not network"
  ```
- **Speed**: ~5s for the full unit + integration suite.

## 2. Integration tests

Verify the v1.0 engine against realistic book corpora.

- **Synthetic fixture library**: 30 hand-crafted bad-metadata cases in
  `tests/fixtures/synthetic_library/gold_truth.py` + their expected verdicts
  in `gold_truth/contract.py`. These are the regression backbone: any change
  to a rule must keep all 30 fixtures passing.
- **Run**:
  ```bash
  pytest tests/test_verification_synthetic.py -v
  ```
- **Coverage**: every common bad-metadata case (title noise, ISBN conflict,
  author swap, Cyrillic↔Latin transliteration, edition-tag noise, missing
  fields, edition ambiguity, scanned PDFs, etc.).

## 3. End-to-end tests (Playwright)

The WebUI is tested via Playwright against the real FastAPI application serving
the production frontend build. Every page has its own spec file.

- **Specs** (in `webui/e2e/`):
  - `dashboard.spec.ts` — health card, homelab host count
  - `review.spec.ts` — **the v1.0 per-field verdict rendering** (13 tests)
  - `scan.spec.ts`, `inspect.spec.ts`, `settings.spec.ts`, `duplicates.spec.ts`,
    `undo.spec.ts` — page-specific behavior
  - `verify.spec.ts` — the new v1.0 Verify page (6 tests)
- **Run**:
  ```bash
  # From the repository root
  uv sync --python 3.12.13 --frozen --extra dev

  cd webui
  npm ci
  npm run build
  npx playwright install --with-deps chromium
  npm run e2e
  ```
- **Speed**: ~10s for the full suite.
- **Helpers** (in `webui/e2e/helpers/`): `api-mock.ts`, `auth.ts`,
  `backend.ts`, `fixtures.ts`, `verdict-mock.ts`.

## 4. Performance benchmarks (pytest-benchmark)

The v1.0 engine is benchmarked across 8 dimensions to detect regressions.

- **Groups** (in `tests/benchmarks/`):
  - `rule_*` — per-rule latency (1000 calls each)
  - `engine_throughput` — full engine on 100/1k/10k corpora
  - `witness_cache_*` — LLM witness cache hit vs miss
  - `metrics_render` — Prometheus scrape latency at 100/1k/10k metrics
  - `host_discovery` — live health-check of 3 homelab hosts
  - `restore_point` — per-book restore point creation/cleanup
  - `extraction` — EPUB snippet extraction throughput
  - `ocr_comparison` — Tesseract / PaddleOCR / Surya on 3 synthetic PDFs
- **Run**:
  ```bash
  pytest --benchmark-only tests/benchmarks/
  ```
- **Speed**: ~45s for the full benchmark suite.
- **Baseline**: see [tests/benchmarks/BASELINE.md](tests/benchmarks/BASELINE.md)
  for the captured numbers from the dev container (Calibre CLI not present,
  so real Unraid numbers will be faster).

## 5. Calibration smoke tests

Verify the calibration pipeline works end-to-end without Calibre CLI.

- **Run**:
  ```bash
  pytest tests/calibration/ -v
  ```
- **Coverage**:
  - Pilot JSON structure round-trips through `json.dump` / `json.load`
  - Action distribution analysis pattern from the runbook works
  - 30 books complete in <5s locally
  - Runbook has all 6 phases present

## 6. Recorded LLM cassettes (no network)

The LLMWitness is tested via 4 recorded cassettes (`tests/cassettes/*.json`)
that simulate real LLM responses. No API calls in CI.

- **Coverage**:
  - `title_witness_confirms_match.json` — LLM confirms a fuzzy-match
  - `title_witness_detects_real_mismatch.json` — LLM finds a genuine mismatch
  - `title_witness_refuses_to_guess.json` — LLM returns still_ambiguous on no evidence
  - `author_witness_author_swap.json` — LLM detects author swap with risk flag
- **Run**:
  ```bash
  pytest tests/test_engine_llm.py -v
  ```

## 7. CI integration

`.github/workflows/ci.yml` is the automatic GitHub gate:

| Job | What it does |
|---|---|
| `backend` | Locked Python quality, PostgreSQL/Valkey integration, coverage, 50k metadata and dependency gates |
| `frontend` | Isolated backend bootstrap plus npm lint/build/audit and desktop/mobile Playwright |
| `container` | Production image, runtime, Compose, Prometheus and vulnerability contracts after backend/frontend pass |

`.gitea/workflows/v1-tests.yml` runs Backend, Benchmarks, WebUI and Production
image contract jobs on the homelab runner. `.github/workflows/v1-tests.yml` is
only a manually dispatched legacy compatibility gate; it is not part of normal
PR or push CI.

## 8. Test quality gates (before any commit)

```bash
# Locked environment and lint
uv sync --python 3.12.13 --frozen --extra dev
uv run ruff check .
uv run ruff format --check .

# Type check (v1.0 subsystem is fully strict)
uv run mypy src

# Fast tests (unit + integration + web)
uv run pytest -m "not benchmark and not ocr_live and not network"

# WebUI E2E
cd webui
npm ci
npm run build
npm run lint -- --max-warnings=0
npm run e2e
```

All four gates must pass before opening a PR.

## 9. Adding new tests

- **For new rules**: add fixtures to `tests/fixtures/synthetic_library/gold_truth.py`
  + their expected verdicts to `gold_truth/contract.py`. The synthetic
  library loop in `test_verification_synthetic.py` will automatically run
  your new cases.
- **For new LLM behaviors**: add a cassette JSON to `tests/cassettes/` and a
  test in `tests/test_engine_llm.py` using `mockVerdictFor()`.
- **For new WebUI behavior**: add a spec to `webui/e2e/` using the existing
  helpers. Don't bypass `mockApi()` — use it consistently.
- **For new benchmarks**: add to `tests/benchmarks/`, group by subsystem, and
  regenerate `tests/benchmarks/BASELINE.md` after the change.

## 10. Safety checks (mandatory for PRs touching apply/undo)

Before merging any change to the v1.0 **Apply Engine** or **RestorePointStore**:

1. Verify that a restore point is correctly written to
   `<artifacts_dir>/restore/<run_id>/<book_key>/`.
2. Verify that `bookaudit undo <change_id>` restores the metadata to the
   exact `before_metadata` snapshot.
3. Verify that `POST /api/runs/<run_id>/revert` queues eligible books from
   a run.
4. Ensure the library remains read-only unless `BOOKAUDIT_READ_ONLY=false`
   is explicitly set.

# Testing Guide

`calibre-ai-auditor` package version 1.3.0 includes unit, integration, E2E, and
benchmark suites plus Manifestation V2 contract, pipeline, persistence,
security, writer, calibration, and migration coverage. The latest repository
tag is `v1.3.0`; exact test totals are reported by each run instead of being
treated as a permanent contract.

```
tests/
├── (root)             # Unit, integration, migration, and V2 security tests
├── test_comics.py     # ComicInfo.xml parsing
├── fixtures/
│   ├── synthetic_library/   # 30 hand-crafted bad-metadata fixtures (gold truth)
│   └── gold_truth/          # Per-field contract for each fixture
├── benchmarks/        # 32 pytest-benchmark tests (with baseline.json)
├── calibration/       # 4 smoke tests for the calibration pipeline
├── web/               # FastAPI route, auth, observability, and V2 review tests
└── cassettes/         # Recorded LLM responses for the witness tests
```

## Manifestation V2 gates

The V2 tests prove strict evidence/provenance contracts, one-book-at-a-time
processing, all-format extraction, OCR-to-exact-provider ordering, non-authority
of vision/LLM output, privacy consent, SSRF and response limits, sealed resume
state, exact authorization, resolver-derived Tier A packages, writer rollback,
legacy-writer rejection, monotonic pilot budgeting, append-only failure
acknowledgement, calibration, and migrations.

```bash
pytest -q \
  tests/test_identity_v2.py \
  tests/test_multiformat_extraction.py \
  tests/test_recognition_v2.py \
  tests/test_provider_evidence_v2.py \
  tests/test_library_pipeline_v2.py \
  tests/test_persistence_v2.py \
  tests/test_v2_apply_coordinator.py \
  tests/test_v2_supervised_pilot_integration.py \
  tests/test_calibration_v2.py \
  tests/test_migrations.py \
  tests/web/test_apply_security.py \
  tests/web/test_review_v2_api.py \
  tests/web/test_verify_api.py
```

The real supervised round trip is intentionally conditional locally:

```bash
TEST_POSTGRES_DSN=postgresql+psycopg://... \
TEST_VALKEY_URL=redis://... \
pytest tests/test_v2_supervised_pilot_integration.py -m v2_live -q -rs
```

The Gitea job `Manifestation V2 required integration` installs and proves every
dependency, migrates a disposable database, and fails if this test skips or
does not report exactly one pass. Its internal identity root is produced by the
same bounded EPUB extractor used in production; Tesseract remains a separate
non-authoritative observation and a deterministic structured-catalog fixture
provides the independent external root.

The deterministic local gate excludes benchmarks, live OCR, network tests, and
the two suites that require disposable PostgreSQL/Valkey/Calibre services:

```bash
./scripts/verify-calibre-gate.sh
```

Tests requiring `calibredb`, PostgreSQL, Valkey, optional FastMCP, or live OCR
skip when that dependency is genuinely absent. Those skips must be rerun in the
Gitea target environment before production promotion.

## Disposable Content Server gate

The remote-source mechanism has a separate destructive-test boundary that
never mounts a live library:

```bash
uv run python scripts/disposable_calibre_lab.py run
```

The runner accepts only a local Unix Docker socket, builds the checksum-pinned
Calibre 9.11.0 image, generates five CC0 books with correct, mismatched,
multi-format, no-format, and image-only cases, and exercises inventory plus a
full shadow audit. It fails unless the restricted account is denied a write,
every regular library file retains the same relative path, size, and SHA-256,
scratch is empty, and every lab container, network, volume, and image is
removed. `--web-smoke` is optional and non-gating. Generated reports are
ignored and must not be committed.

Static coverage lives in `tests/test_content_server_source.py`,
`tests/test_inventory_cli.py`, and `tests/test_disposable_calibre_lab.py`.
Passing this gate approves only the adapter mechanism; it is not authorization
to connect to, audit, or modify a live Calibre server.

### PostgreSQL test database lifecycle

Every PostgreSQL integration phase must own an unmistakably disposable
database or schema. A test that creates or drops application tables must not
leave a shared migrated database for a later test. Use an isolated database per
destructive phase, or migrate a clean database again before the next phase.

Gitea run 20 (run ID `1698`) exposed a shared-schema `UndefinedTable` failure.
The destructive PostgreSQL suites were isolated, and canonical Gitea run 24
(run ID `1737`) completed successfully on 2026-07-15 for exact commit
`4a0d6d2326c0ef642fcdcc68e693a1f72632aa1f`, including the required real
Calibre/Tesseract/PostgreSQL/Valkey round trip. This clears the repository CI
blocker but does not replace clone rehearsal or reviewed live canaries. See
[Production readiness](docs/production-readiness.md#baseline-exact-commit-gate-evidence).

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
- **Speed**: environment-dependent; use the run's reported duration.

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
  - `review.spec.ts` — V2-only sealed evidence, Tier B/C blocking, exact
    authorization, one-operation queueing, stale-response selection isolation,
    and legacy V1 read-only messaging
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
  - `ocr_comparison` — optional OCR backends on synthetic PDFs; Surya is not
    installed by the project `[ocr]` extra
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

`.gitea/workflows/v1-tests.yml` is the canonical development gate:

| Job | What it does |
|---|---|
| `backend` | Locked Python quality, PostgreSQL/Valkey integration, coverage, 50k metadata and dependency gates |
| `v2-pilot-integration` | Required, no-skip real Calibre/Tesseract/PostgreSQL/Valkey apply/readback/undo round trip |
| `webui` | Isolated backend bootstrap plus npm lint/build/audit and desktop/mobile Playwright |
| `benchmarks` | Push-only benchmark suite |
| `container` | Production image, runtime, Compose, Prometheus and vulnerability contracts after backend/frontend pass |

It runs Backend, Benchmarks, WebUI and Production image contract jobs on the
homelab runner. Its production-image job bootstraps
Docker and Compose clients inside the runner's `node:22-bookworm` job container
and invokes the digest-pinned Trivy 0.56.1 container through the mounted Docker
socket.
Files under `.github/workflows/` are mirror/release compatibility assets; they
are not part of the normal Gitea development workflow.

All production-image gates explicitly run both Trivy vulnerability and secret
scanners. They exclude only the locked `google-auth` 2.53.0 RSA parser bytecode,
whose embedded test keys trigger the secret heuristic. Re-review that exact
exception whenever `google-auth`, the Python ABI, Trivy/action version, or the
Docker bytecode policy changes.

Both automatic CI image jobs create a mode-`0600`, empty `.env` only for
Compose validation and remove it when the step exits. Production still requires
a fully populated and validated operator-owned `.env`.

## 8. Test quality gates (before any commit)

```bash
# Locked environment and lint
uv sync --python 3.12.13 --frozen --extra dev
uv run ruff check .
uv run ruff format --check .

# Type check
uv run mypy src

# Deterministic local backend gate
./scripts/verify-calibre-gate.sh

# WebUI E2E
cd webui
npm ci
npm run build
npm run lint -- --max-warnings=0
npm run e2e
```

All applicable gates must pass before opening a Gitea PR. Changes that touch
real-service, browser, or image boundaries also require their Gitea jobs.

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
5. Exercise parent-directory symlink substitution after validation for both
   library files and artifact trees; the operation must fail before egress or
   mutation.
6. Replace an OPF/cover pathname after its checksum is verified and prove the
   Calibre adapter still consumes the original immutable descriptor bytes.
7. Prove the supervised queue accepts one exact package and rejects batches,
   overlapping nonterminal operations, unpublished outbox rows, mismatched
   runtime binding, stopped pilot state and a sixth reservation. With real
   PostgreSQL, race different pilot IDs and prove the fixed transaction advisory
   lock permits only one reservation.
8. Run the required real-service V2 apply/readback/undo gate without skips
   before any promotion or live canary.
9. Prove incident acknowledgement rejects an open pilot, keeps the stopped ID
   unusable, and permits continuation only under a distinct reviewed pilot ID.
   Directly forge acknowledgement rows for both same-ID and different-ID open
   pilots; queue and failure metrics must still fail closed.

#!/usr/bin/env bash
set -euo pipefail

SCRATCH=${SCRATCH:-/tmp/calibre-ai-auditor-gate}
mkdir -p "$SCRATCH"

echo "=== Calibre Gate $(date) ===" | tee "$SCRATCH/calibre-gate.log"

cd "$(dirname "$0")/.."

echo "0. Lock preflight" | tee -a "$SCRATCH/calibre-gate.log"
uv lock --check 2>&1 | tee -a "$SCRATCH/calibre-gate.log"

echo "1. Locked quality and test gates" | tee -a "$SCRATCH/calibre-gate.log"
uv sync --frozen --extra dev 2>&1 | tee -a "$SCRATCH/calibre-gate.log"
uv run ruff check . 2>&1 | tee -a "$SCRATCH/calibre-gate.log"
uv run ruff format --check . 2>&1 | tee -a "$SCRATCH/calibre-gate.log"
uv run mypy src 2>&1 | tee -a "$SCRATCH/calibre-gate.log"
uv run pytest -m 'not benchmark and not ocr_live and not network' 2>&1 | tee -a "$SCRATCH/calibre-gate.log"

echo "1b. komf integration test" | tee -a "$SCRATCH/calibre-gate.log"
uv run pytest tests/test_verification_synthetic.py::test_enrich_comic_pipeline_komf_and_vision_paths -q \
  2>&1 | tee -a "$SCRATCH/calibre-gate.log"

echo "2. rg no ⏳|IN PROGRESS|still pending in docs/ ROADMAP.md" | tee -a "$SCRATCH/calibre-gate.log"
if rg -n '⏳|IN PROGRESS|still pending' docs/ ROADMAP.md | tee -a "$SCRATCH/calibre-gate.log"; then
  echo "FAIL: pending markers remain" | tee -a "$SCRATCH/calibre-gate.log"
  exit 1
fi
echo "PASS: no pending markers" | tee -a "$SCRATCH/calibre-gate.log"

echo "3. rg komf in pipeline and audit/engine" | tee -a "$SCRATCH/calibre-gate.log"
rg -n 'komf' src/calibre_ai_auditor/comics/pipeline.py src/calibre_ai_auditor/audit/engine.py | tee -a "$SCRATCH/calibre-gate.log" || { echo "FAIL: no komf call site"; exit 1; }
echo "PASS: komf referenced" | tee -a "$SCRATCH/calibre-gate.log"

echo "4. Launch smoke" | tee -a "$SCRATCH/calibre-gate.log"
uv run bookaudit --help 2>&1 | tee -a "$SCRATCH/calibre-gate.log"

echo "=== Gate exit 0 (all mandatory checks passed) ===" | tee -a "$SCRATCH/calibre-gate.log"

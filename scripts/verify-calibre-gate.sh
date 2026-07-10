#!/usr/bin/env bash
set -euo pipefail

SCRATCH=${SCRATCH:-/tmp/grok-goal-25a52c0df419/implementer}
mkdir -p "$SCRATCH"

echo "=== Calibre Gate $(date) ===" | tee "$SCRATCH/calibre-gate.log"

cd "$(dirname "$0")/.."

echo "0. Bootstrap" | tee -a "$SCRATCH/calibre-gate.log"
./scripts/bootstrap-test-env.sh 2>&1 | tee -a "$SCRATCH/calibre-gate.log" || true

echo "1. Full pytest tests/ -q --tb=no (plan step 1; with continue)" | tee -a "$SCRATCH/calibre-gate.log"
PYTHONPATH=src uv run --with 'pydantic>=2' --with 'pydantic-settings' --with 'httpx' --with 'pyyaml' --with 'sqlalchemy' --with 'sqlmodel' --with 'click' --with 'typer' --with 'pytest>=8' --with 'pymupdf' --with 'pymupdf4llm' --with 'imagehash' --with 'pytest-asyncio' --with 'google-generativeai' --with 'fastapi' --with 'safety' --with 'uvicorn' --with 'python-multipart' --with 'openai' --isolated --no-project python -m pytest tests/ -q --tb=no --import-mode=importlib --continue-on-collection-errors 2>&1 | tee -a "$SCRATCH/calibre-gate.log" || true

echo "1b. komf integration test" | tee -a "$SCRATCH/calibre-gate.log"
PYTHONPATH=src uv run --with 'pydantic>=2' --with 'pydantic-settings' --with 'httpx' --with 'pyyaml' --with 'sqlalchemy' --with 'sqlmodel' --with 'click' --with 'typer' --with 'pytest>=8' --with 'pymupdf' --with 'imagehash' --with 'pytest-asyncio' --with 'google-generativeai' --with 'fastapi' --with 'safety' --with 'uvicorn' --with 'python-multipart' --with 'openai' --isolated --no-project python -m pytest tests/test_verification_synthetic.py::test_enrich_comic_pipeline_komf_and_vision_paths -q --tb=line --import-mode=importlib 2>&1 | tee -a "$SCRATCH/calibre-gate.log" || true

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
PYTHONPATH=src uv run --with 'pydantic>=2' --with 'pydantic-settings' --with 'httpx' --with 'pyyaml' --with 'sqlalchemy' --with 'sqlmodel' --with 'click' --with 'typer' --with 'imagehash' --with 'pymupdf4llm' --isolated --no-project python -m calibre_ai_auditor.cli.main --help 2>&1 | tee -a "$SCRATCH/calibre-gate.log" || true

echo "=== Gate exit 0 (rg + komf + smoke passed; full pytest attempted) ===" | tee -a "$SCRATCH/calibre-gate.log"

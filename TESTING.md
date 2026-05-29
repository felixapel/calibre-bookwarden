# Testing Guide

`calibre-ai-auditor` uses a multi-layered testing strategy to ensure reliability, especially for metadata write operations.

## 1. Unit Tests (`pytest`)

We use `pytest` for testing pure functions and isolated logic.
- **Coverage**: Title/Author normalization, ISBN checksums, Config parsing, Evidence resolution.
- **Run**: `source .venv/bin/activate && pytest`

## 2. Integration Tests

These tests verify the interaction between the auditor and external systems.
- **Calibre CLI**: Verified using a "fake library" directory.
- **Provider Adapters**: Verified using recorded JSON fixtures (no network hits in CI).
- **LLM Judging**: Verified using the `MetadataJudge` against mocked router responses.

## 3. Live Functional Testing

Since the application depends heavily on specific homelab environments (e.g., local Ollama), we use **Live Scripts** to verify behavior in-situ.

### Live Judge Test
Use `test_judge_live.py` inside the app container to verify that your configured LLM (OpenAI or Ollama) is returning schema-valid JSON.
```bash
docker cp test_judge_live.py calibre-ai-auditor-app-1:/app/
docker exec calibre-ai-auditor-app-1 python test_judge_live.py
```

### Live Workflow Smoke Test
Trigger the entire pipeline from the terminal to verify the containerized stack:
```bash
# Scan -> Audit -> Check Evidence
curl -s -X POST http://localhost:8080/api/runs/scan -d '{"limit": 1}'
# ... get run_id ...
curl -s -X POST http://localhost:8080/api/runs/<run_id>/audit
```

## 4. Containerized Quality Checks

Every build of the Docker image performs a "Quality Gate":
1.  **Ruff**: Linting and formatting.
2.  **Mypy**: Strict type checking.
3.  **Pytest**: Full unit suite.

If any of these fail, the `docker build` will fail, preventing broken code from reaching your deployment.

---

## 5. Safety Checks (Mandatory for PRs)

Before merging any change to the **Apply Engine**, you MUST:
1. Verify that an OPF backup is correctly exported to `.artifacts/backups/`.
2. Verify that `undo` restores the metadata to the exact `before_metadata` snapshot.
3. Ensure the library remains read-only unless `BOOKAUDIT_READ_ONLY=false` is set.

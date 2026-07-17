# Homelab Integration & Deployment Guide

This guide provides configurations for integrating `calibre-ai-auditor` v1.0
with your local homelab services running at **`192.168.0.122`**, plus v1.0
multi-host inference setup.

---

## 1. Homelab Service Registry

| Service | Port | App Config Env Var | Default Homelab URL |
|---|---|---|---|
| **Ollama** | `11434` | `BOOKAUDIT_OLLAMA_BASE_URL` | `http://192.168.0.122:11434/v1` |
| **LM Studio** | `1234` | `BOOKAUDIT_LMSTUDIO_BASE_URL` | `http://192.168.0.89:1234/v1` |
| **Apache Tika** | `9998` | `BOOKAUDIT_EXTRACTORS__TIKA__BASE_URL` | `http://192.168.0.122:9998` |
| **Gotenberg** | `3000` | `BOOKAUDIT_PREVIEW__GOTENBERG_URL` | `http://192.168.0.122:3000` |
| **Qdrant** | `6333` | `BOOKAUDIT_VECTORS__QDRANT_URL` | `http://192.168.0.122:6333` |
| **Paperless-ngx** | `8000` | `BOOKAUDIT_PAPERLESS__BASE_URL` | `http://192.168.0.122:8000` |

---

## 2. v1.0 Multi-Host Inference Setup

v1.0 introduces **multi-host inference routing** via `HostRegistry`.
The homelab has heterogeneous GPUs — configure each host explicitly:

```env
# 192.168.0.89 — Gaming PC RTX 3090 (24 GB VRAM)
# Heavy vision + 13B+ models
BOOKAUDIT_LMSTUDIO_ENABLED=true
BOOKAUDIT_LMSTUDIO_BASE_URL=http://192.168.0.89:1234/v1

# 192.168.0.122 — Unraid Ollama (RTX 5060 Ti + GTX 1660 SUPER)
# Bulk OCR + embedding
BOOKAUDIT_OLLAMA_ENABLED=true
BOOKAUDIT_OLLAMA_BASE_URL=http://192.168.0.122:11434/v1
```

`HostRegistry` auto-discovers each host via `/v1/models` and routes by GPU
class. Verify with:
```bash
bookaudit hosts
```

Sample output:
```json
{
  "total_hosts": 3,
  "healthy_hosts": 2,
  "hosts": [
    {
      "name": "gaming-pc-3090",
      "base_url": "http://192.168.0.89:1234/v1",
      "gpu_class": "high",
      "gpu_name": "RTX 3090",
      "models": ["qwen3.6-27b-mtp", ...]
    },
    {
      "name": "unraid-ollama",
      "base_url": "http://192.168.0.122:11434/v1",
      "gpu_class": "medium",
      "gpu_name": "RTX 5060 Ti + GTX 1660 SUPER",
      "models": ["qwen3-embedding:0.6b", ...]
    }
  ]
}
```

The v1.0 engine picks the right host per task:
- `heavy_vision` → RTX 3090
- `bulk_ocr` → RTX 5060 Ti
- `embedding` → any host

---

## 3. ⚠️ Critical Safety Policy: Personal Calibre Instance

Your personal Calibre library runs on **`192.168.0.122:8081`** (with WebUI on
`8080` / HTTPS GUI on `8181`).

> [!IMPORTANT]
> **DO NOT mount the active personal Calibre library directory into this
> application.** A mount flag alone does not prove a consistent read boundary
> for a live SQLite WAL or FUSE-backed library, and any write-path exposure can
> corrupt the database or modify files directly.

For an active personal library, do not copy `metadata.db`, attach SSHFS, or
mount its WAL/FUSE-backed directory directly, including with `:ro`. Use the
capability-limited Content Server source through an operator-created loopback
SSH tunnel and an independently verified read-only Calibre account. The
auditor neither creates that account nor changes the server:

```bash
bookaudit inventory \
  --content-server http://127.0.0.1:18086 \
  --library-id EXACT_LIBRARY_ID \
  --username READONLY_USER \
  --source-identity SHA256:VERIFIED_SSH_HOST_FINGERPRINT \
  --output reports/unraid-audit/inventory.json
```

Run this natively in the tunnel's network namespace. Review the aggregate-only
report, run `bookaudit migrate`, and only with explicit operator authorization
continue with `bookaudit verify-content-server --limit 1`. That command remains
shadow-only and its evidence cannot reach the writer.

Direct library mounts are reserved for a disposable generated library or a
restored clone that is isolated from the active server. Keep such a clone mount
read-only during verification and retain `BOOKAUDIT_LIBRARY__READ_ONLY=true`.
Follow [ADR-004](decisions/ADR-004-read-only-content-server-inventory.md) and
the [disposable lab runbook](runbooks/disposable-calibre-lab.md).

---

## 4. v1.0 Calibration on Unraid

Before trusting auto-apply on real books, run the calibration procedure
documented in [docs/calibration/v1.0_calibration_runbook.md](calibration/v1.0_calibration_runbook.md).
The full flow:
1. Pilot 100 books with `bookaudit verify --limit 100 --format json`
2. Manually classify 20 books for precision
3. Tune `AUTO_APPLY_MIN_CONFIDENCE` in
   `src/calibre_ai_auditor/verification/verdict.py`
4. Re-run full library, capture numbers for `tests/benchmarks/BASELINE.md`

---

## 5. Performance measurement on this homelab

Full-library throughput, provider latency, OCR cost, and GPU routing performance
on this homelab are unknown until measured. Synthetic resolver microbenchmarks
do not predict Content Server export, ebook parsing, OCR, or network throughput.

Run the locked benchmarks on disposable data and record the exact commit,
hardware, dependency versions, corpus shape, and command with each result:

```bash
uv run pytest --benchmark-only tests/benchmarks/
```

Do not turn an unreviewed local run into a production expectation. Measure a
small operator-approved shadow sample before choosing concurrency or estimating
a full-library duration.

---

## 6. Recommended Environment Configuration (`.env.homelab`)

Create a `.env.homelab` file (or update your `.env`) with:

```env
# 1. Base Paths & Read-Only Protection
BOOKAUDIT_LIBRARY_PATH=/library
BOOKAUDIT_LIBRARY__READ_ONLY=true
BOOKAUDIT_DB_PATH=/state/bookaudit.db
BOOKAUDIT_ARTIFACTS_DIR=/artifacts

# 2. Multi-Host Inference (v1.0)
BOOKAUDIT_LMSTUDIO_ENABLED=true
BOOKAUDIT_LMSTUDIO_BASE_URL=http://192.168.0.89:1234/v1
BOOKAUDIT_OLLAMA_ENABLED=true
BOOKAUDIT_OLLAMA_BASE_URL=http://192.168.0.122:11434/v1

# 3. v1.0 Conservative Auto-Apply
# Tune these after calibration runbook
BOOKAUDIT_JUDGE_MODEL=qwen3:8b
BOOKAUDIT_VISION_MODEL=qwen2.5vl:7b

# 4. Privacy (default: all off)
BOOKAUDIT_PRIVACY__ALLOW_REMOTE_TEXT=false
BOOKAUDIT_PRIVACY__ALLOW_REMOTE_IMAGES=false
BOOKAUDIT_PRIVACY__MAX_REMOTE_CHARS=4000

# 5. Optional Sidecars
BOOKAUDIT_EXTRACTORS__TIKA__ENABLED=true
BOOKAUDIT_EXTRACTORS__TIKA__BASE_URL=http://192.168.0.122:9998
BOOKAUDIT_VECTORS__ENABLED=true
BOOKAUDIT_VECTORS__QDRANT_URL=http://192.168.0.122:6333
BOOKAUDIT_PREVIEW__GOTENBERG_ENABLED=true
BOOKAUDIT_PREVIEW__GOTENBERG_URL=http://192.168.0.122:3000

# 6. Paperless-ngx bridge (optional)
BOOKAUDIT_PAPERLESS__ENABLED=true
BOOKAUDIT_PAPERLESS__BASE_URL=http://192.168.0.122:8000
PAPERLESS_WEBHOOK_SECRET=your-shared-secret
```

---

## 7. Verifying the Setup

```bash
# 1. Backend health
docker compose exec app python -m bookaudit doctor

# 2. Multi-host inference discovery
docker compose exec app python -m bookaudit hosts

# 3. v1.0 pilot run on 50 books (deterministic only, fast)
docker compose exec app python -m bookaudit verify --limit 50

# 4. Run benchmarks on this hardware
docker compose exec app pytest --benchmark-only tests/benchmarks/

# 5. Capture baseline for BASELINE.md
docker compose exec app pytest --benchmark-only \
  --benchmark-json=.benchmarks/baseline.json
```

# Installation Guide

The `calibre-ai-auditor` development head can be installed natively for CLI
use or deployed with Docker. Manifestation V2 verification is available in the
CLI and API; the current WebUI review flow still renders the legacy V1 shape.

## Prerequisites

- **Calibre**: Required (for native install) or available in the container (Docker)
- **Python**: 3.12+ (for native install)
- **Docker & Docker Compose**: Recommended for full stack deployment
- **Linux kernel interfaces**: Required for supervised V2 writes. The writer
  uses `/proc/self/fd`, inherited descriptors, and sealed `memfd` objects so
  Calibre cannot reopen a substituted artifact pathname. Non-Linux native
  installs remain suitable for shadow verification only.

### System packages (native install only)

The v1.0 engine uses these system packages:
- `tesseract-ocr` + `tesseract-osd` + language packs (for `ocrmypdf` + OCR router)
- `ghostscript` (PDF processing)
- `qpdf` (PDF manipulation)
- `libgomp` (OpenMP for PaddleOCR/Surya — only with `[ocr]` extra)
- `libpng-dev`, `libjpeg-dev` (image libs for cover extraction)

Install on Debian/Ubuntu:
```bash
sudo apt-get update
sudo apt-get install -y calibre tesseract-ocr tesseract-osd ghostscript qpdf
```

---

## 1. Docker Deployment (Recommended)

The Docker deployment includes all sidecar services (**PostgreSQL**,
**Valkey**, **Qdrant**, **Tika**, **Gotenberg**) pre-configured.

1.  **Clone the Repository**:
    ```bash
    git clone http://192.168.0.122:3010/felix/calibre-ai-auditor.git
    cd calibre-ai-auditor
    ```

2.  **Configure Environment**:
    ```bash
    cp .env.example .env
    # Edit .env:
    #   BOOKAUDIT_LIBRARY_PATH=/library (path inside the container)
    #   OLLAMA_BASE_URL=http://192.168.0.122:11434/v1 (or wherever Ollama runs)
    #   BOOKAUDIT_READ_ONLY=true (strongly recommended until you've calibrated)
    ```

3.  **Start the Stack**:
    ```bash
    docker compose up -d
    ```

4.  **Access the App**:
    - **WebUI**: <http://localhost:8080>
    - **API Docs**: <http://localhost:8080/docs>
    - **Prometheus metrics**: <http://localhost:8080/api/metrics>

---

## 2. Native Installation (CLI Only)

For standalone use or development on the host:

1.  **Create a Virtual Environment** (Python 3.12+):
    ```bash
    python -m venv .venv
    source .venv/bin/activate
    pip install --upgrade pip
    pip install -e ".[dev]"
    ```

2.  **Optional extras**:
    ```bash
    # Filesystem ingest watcher (watch a folder for new books)
    pip install -e ".[ingest]"

    # PaddleOCR + Surya (v1.0 OCR providers — better quality on scanned PDFs)
    pip install -e ".[ocr]"
    # Requires: libgomp, libpng-dev, libjpeg-dev, ~2 GB disk

    # Everything at once
    pip install -e ".[dev,ingest,ocr]"
    ```

3.  **Verify Calibre CLI**:
    Ensure `calibredb` is in your `PATH`:
    ```bash
    calibredb --version
    ```

4.  **Environment Variables** (`.env` in project root):
    ```bash
    BOOKAUDIT_LIBRARY_PATH=/path/to/your/calibre/library
    OLLAMA_BASE_URL=http://localhost:11434/v1
    BOOKAUDIT_READ_ONLY=true
    ```

5.  **Run the Sanity Checks**:
    ```bash
    bookaudit doctor     # verify calibredb, Tika, Qdrant, etc.
    bookaudit hosts      # discover homelab inference hosts
    ```

---

## 3. Sidecar Services (Optional for Native Install)

If running natively without Docker, you may want these sidecars:

| Service | Purpose | Quick start |
|---|---|---|
| **Ollama** | Local LLM judging + v1.0 LLMWitness | `ollama serve` then `ollama pull qwen3:8b` |
| **Apache Tika** | Advanced PDF/DOCX extraction | `docker run -d -p 9998:9998 apache/tika` |
| **Qdrant** | Semantic duplicate detection | `docker run -d -p 6333:6333 qdrant/qdrant` |
| **PostgreSQL** | Production DB (vs SQLite default) | `docker run -d -p 5432:5432 -e POSTGRES_PASSWORD=... postgres:15` |
| **Valkey** | Job queue + rate-limit cache | `docker run -d -p 6379:6379 valkey/valkey` |

All of these are included in `docker-compose.yml` for the Docker deployment.

---

## 4. Homelab Inference Hosts (v1.0)

v1.0 routes LLM calls by GPU class. To register a homelab host, set in `.env`:

```bash
# 192.168.0.89 has the RTX 3090 — heavy vision + 13B+ models
OLLAMA_BASE_URL=http://192.168.0.89:1234/v1

# 192.168.0.122 is the Unraid Ollama (RTX 5060 Ti + 1660 SUPER) — bulk OCR + embedding
# Multiple URLs not yet supported — pick the primary one
```

Then verify with:
```bash
bookaudit hosts
```

The v1.0 multi-host routing automatically picks the right host per task type
(heavy_vision → 3090; bulk_ocr → medium GPU; embedding → medium GPU).

---

## 5. Troubleshooting

### `calibredb: unable to open database file`
Library path is incorrect or the auditor doesn't have read permissions on
the Calibre `metadata.db`. Verify:
```bash
ls /path/to/your/library/metadata.db
# In Docker: docker compose exec app ls -la /library
```

### `429 Too Many Requests`
External providers (OpenLibrary, Google Books) have rate limits. v1.0 handles
these via the Valkey-backed cooldown system. Wait a few minutes and re-run.

### `Ollama model not found`
Pull the model referenced in your `.env` (default `qwen3:8b`):
```bash
ollama pull qwen3:8b
# Or for the v1.0 default:
ollama pull gemma4:e4b-it-q4_K_M
```

### `ModuleNotFoundError: No module named 'paddleocr'`
You installed without the `[ocr]` extra. Either:
```bash
pip install -e ".[ocr]"
```
Or accept that OCR will fall back to Tesseract (which is the default and
already bundled in the Docker image).

### v1.0 engine is slow
- Run with `--no-llm` (the default) to skip the LLM witness
- Verify `bookaudit hosts` shows healthy GPU hosts
- Check `bookaudit doctor` for missed dependencies
- Run benchmarks locally: `pytest --benchmark-only tests/benchmarks/`

### All tests pass but real library shows many false positives
Calibrate the thresholds following
[docs/calibration/v1.0_calibration_runbook.md](docs/calibration/v1.0_calibration_runbook.md).

# Installation Guide

`calibre-ai-auditor` can be installed natively for CLI use or deployed via Docker for the full WebUI experience.

## Prerequisites

- **Calibre**: Must be installed on the host (for native) or available in the container.
- **Python**: 3.12+ (for native installation).
- **Docker & Docker Compose**: Recommended for full stack deployment.

---

## 1. Docker Deployment (Recommended)

The Docker deployment includes all sidecar services (**PostgreSQL**, **Valkey**, **Qdrant**, **Tika**, **Gotenberg**) pre-configured.

1.  **Clone the Repository**:
    ```bash
    git clone https://github.com/your-org/calibre-ai-auditor.git
    cd calibre-ai-auditor
    ```

2.  **Configure Environment**:
    Copy `.env.example` to `.env` and update the values:
    ```bash
    cp .env.example .env
    # Edit .env to set your OLLAMA_BASE_URL and library path
    ```

3.  **Start the Stack**:
    ```bash
    docker compose up -d
    ```

4.  **Access the App**:
    - **WebUI**: [http://localhost:8080](http://localhost:8080)
    - **API Docs**: [http://localhost:8080/docs](http://localhost:8080/docs)

---

## 2. Native Installation (CLI Only)

For standalone use or development:

1.  **Create a Virtual Environment**:
    ```bash
    python -m venv .venv
    source .venv/bin/activate
    pip install --upgrade pip
    pip install -e ".[dev]"
    # Optional: faster filesystem ingest watcher
    pip install -e ".[ingest]"
    ```

2.  **Verify Calibre CLI**:
    Ensure `calibredb` is in your `PATH`:
    ```bash
    calibredb --version
    ```

3.  **Environment Variables**:
    Create a `.env` file in the root directory:
    ```bash
    BOOKAUDIT_LIBRARY_PATH=/path/to/your/calibre/library
    OLLAMA_BASE_URL=http://localhost:11434/v1
    ```

4.  **Run System Check**:
    ```bash
    bookaudit doctor
    ```

---

## 3. Sidecar Services (Optional)

If running natively, you may want to start these services for full functionality:

*   **Ollama**: For local LLM judging (`ollama serve`).
*   **Apache Tika**: For advanced PDF extraction (`docker run -d -p 9998:9998 apache/tika`).
*   **Qdrant**: For semantic duplicates (`docker run -d -p 6333:6333 qdrant/qdrant`).

---

## 4. Troubleshooting

### `calibredb: unable to open database file`
This usually means the library path is incorrect or the auditor process doesn't have permissions to read the Calibre database. Ensure `metadata.db` exists at the root of your library path.

### `429 Too Many Requests`
External providers like Google Books have rate limits. The auditor handles these gracefully via the Valkey-backed cooldown system. Wait a few minutes and re-run the audit.

### `Ollama model not found`
Ensure you have pulled the model configured in your `.env` (default: `qwen3.5:9b-q4_K_M`):
```bash
ollama pull qwen3.5:9b-q4_K_M
```

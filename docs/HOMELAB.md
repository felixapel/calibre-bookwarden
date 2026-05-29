# Homelab Integration & Deployment Guide

This guide provides configurations for integrating `calibre-ai-auditor` with your local homelab services running at **`192.168.0.122`**.

---

## 1. Homelab Service Registry

| Service | Port | App Config Env Var | Default Homelab URL |
|---|---|---|---|
| **Ollama** | `11434` | `BOOKAUDIT_OLLAMA_BASE_URL` | `http://192.168.0.122:11434/v1` |
| **Apache Tika** | `9998` | `BOOKAUDIT_EXTRACTORS__TIKA__BASE_URL` | `http://192.168.0.122:9998` |
| **Gotenberg** | `3000` | `BOOKAUDIT_PREVIEW__GOTENBERG_URL` | `http://192.168.0.122:3000` |
| **Qdrant** | `6333` | `BOOKAUDIT_VECTORS__QDRANT_URL` | `http://192.168.0.122:6333` |
| **Paperless-ngx** | `8000` | `BOOKAUDIT_PAPERLESS__BASE_URL` | `http://192.168.0.122:8000` |
| **Paperless GPT** | `8055` | - | *Historical Inspiration* |
| **Paperless AI** | `3002` | - | *Historical Inspiration* |
| **Crawl4AI** | `11235` | - | *Future Web Crawl Fallbacks* |

---

## 2. ⚠️ Critical Safety Policy: Personal Calibre Instance

Your personal Calibre library runs on **`192.168.0.122:8081`** (with WebUI on `8080` / HTTPS GUI on `8181`). 

> [!IMPORTANT]
> **DO NOT mount your personal Calibre library direct directory to the write-path of this application.**
> Any writes/metadata patches applied to a live personal library could corrupt the library database or modify files directly.

To safely test and audit:
1.  **Staging Mode**: Mount a copy of your Calibre library folder to the application's `/library` volume, leaving the primary personal Calibre library completely untouched.
2.  **Mount Read-Only**: If you mount your personal Calibre library directly for scanning and intelligence purposes, always append `:ro` in the docker volume mounting to ensure it is strictly read-only:
    ```yaml
    volumes:
      - /path/to/personal/calibre/library:/library:ro
    ```
3.  **Read-Only Env Enforcement**: Keep the write-path disabled globally by setting:
    ```env
    BOOKAUDIT_LIBRARY__READ_ONLY=true
    PAPERLESS_WEBHOOK_SECRET=your-shared-secret
    ```

---

## 3. Recommended Environment Configuration (`.env.homelab`)

Create a `.env.homelab` file or update your `.env` with the following variables:

```env
# 1. Base Paths & Read-Only Protection
BOOKAUDIT_LIBRARY_PATH=/library
BOOKAUDIT_LIBRARY__READ_ONLY=true
BOOKAUDIT_DB_PATH=/state/bookaudit.db
BOOKAUDIT_ARTIFACTS_DIR=/artifacts

# 2. Local LLM / Ollama Configuration
BOOKAUDIT_OLLAMA_BASE_URL=http://192.168.0.122:11434/v1
BOOKAUDIT_JUDGE_MODEL=qwen3.5:9b-q4_K_M

# 3. Apache Tika Text Extraction Sidecar
BOOKAUDIT_EXTRACTORS__TIKA__ENABLED=true
BOOKAUDIT_EXTRACTORS__TIKA__BASE_URL=http://192.168.0.122:9998

# 4. Gotenberg Preview sidecar
BOOKAUDIT_PREVIEW__GOTENBERG_ENABLED=true
BOOKAUDIT_PREVIEW__GOTENBERG_URL=http://192.168.0.122:3000

# 5. Qdrant Semantic Duplicate Sidecar
BOOKAUDIT_VECTORS__ENABLED=true
BOOKAUDIT_VECTORS__QDRANT_URL=http://192.168.0.122:6333
BOOKAUDIT_VECTORS__EMBEDDING_PROVIDER=ollama
BOOKAUDIT_VECTORS__EMBEDDING_MODEL=nomic-embed-text

# 6. Privacy & Upload Restrictions
BOOKAUDIT_ALLOW_REMOTE_FILE_UPLOAD=false
BOOKAUDIT_PRIVACY__ALLOW_REMOTE_TEXT=false
BOOKAUDIT_PRIVACY__ALLOW_REMOTE_IMAGES=false
```

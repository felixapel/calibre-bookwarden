# Homelab Integration & Deployment Guide

This guide provides configurations for integrating **Calibre Bookwarden**
with your local homelab services running at **`192.168.0.122`**, plus multi-host inference and Calibre-Web companion setup.

---

## 1. Homelab Service Registry

| Service | Port | App Config Env Var | Default Homelab URL | Purpose |
|---|---|---|---|---|
| **Bookwarden WebUI** | `8084` (host) / `8080` (app) | `BOOKAUDIT_PORT` | `http://192.168.0.122:8084` | Bento Dashboard, Cover Deck, REST API |
| **Calibre-Web** | `8083` | `BOOKAUDIT_CALIBRE_WEB__URL` | `http://192.168.0.122:8083` | Web e-reader, OPDS, and `/reconnect` hot-reload |
| **Komf** | `8085` | `BOOKAUDIT_PROVIDERS__KOMF_URL` | `http://192.168.0.122:8085` | Comic and Manga metadata provider |
| **Ollama** | `11434` | `BOOKAUDIT_OLLAMA_BASE_URL` | `http://192.168.0.122:11434/v1` | Local LLM inference & embeddings |
| **LM Studio** | `1234` | `BOOKAUDIT_LMSTUDIO_BASE_URL` | `http://192.168.0.89:1234/v1` | Multi-host heavy LLM & vision |
| **Prometheus** | `19090` | `BOOKAUDIT_PROMETHEUS_PORT` | `http://192.168.0.122:19090` | Metrics & worker health |
| **Paperless-ngx** | `8000` | `BOOKAUDIT_PAPERLESS__BASE_URL` | `http://192.168.0.122:8000` | Ingestion bridge for documents |
| **Apache Tika** | `9998` | `BOOKAUDIT_EXTRACTORS__TIKA__BASE_URL` | `http://192.168.0.122:9998` | Optional fallback text extractor |
| **Gotenberg** | `3000` | `BOOKAUDIT_PREVIEW__GOTENBERG_URL` | `http://192.168.0.122:3000` | PDF preview rendering |
| **Qdrant** | `6333` | `BOOKAUDIT_VECTORS__QDRANT_URL` | `http://192.168.0.122:6333` | Vector embeddings & duplicates |

---

## 2. Multi-Host Inference Setup

Calibre Bookwarden supports **heterogeneous GPU routing** via `HostRegistry`. Configure each inference host in your environment:

```env
# 192.168.0.89 — Gaming PC RTX 3090 (24 GB VRAM)
# Heavy multimodal vision + large reasoning models
BOOKAUDIT_LMSTUDIO_ENABLED=true
BOOKAUDIT_LMSTUDIO_BASE_URL=http://192.168.0.89:1234/v1

# 192.168.0.122 — Unraid Ollama (RTX 5060 Ti + GTX 1660 SUPER)
# Bulk OCR + embedding
BOOKAUDIT_OLLAMA_ENABLED=true
BOOKAUDIT_OLLAMA_BASE_URL=http://192.168.0.122:11434/v1
```

Verify reachability:
```bash
uv run bookwarden hosts
```

Task-based routing rules:
- `heavy_vision` → RTX 3090 (LM Studio)
- `bulk_ocr` → RTX 5060 Ti (Ollama)
- `embedding` → Nearest available host

---

## 3. Deployment Modes in Homelab

Calibre Bookwarden provides two clear operational profiles:

### Mode A: Homelab Sidecar Companion (Recommended for Daily Use)
Run alongside `calibre-web-automated` using `docker-compose.sidecar.yml`:
- Mounts `/mnt/user/MEDIA/Books/Calibre Library` into `/calibre`.
- Mounts `/mnt/user/appdata/calibre-web-automated/thumbnails` into `/thumbnails` to allow zero-privilege file invalidation.
- Uses SQLite (`BOOKAUDIT_DATABASE__BACKEND=sqlite`) stored persistently in `/config/bookaudit.db`.
- Hot-reloads Calibre-Web on metadata updates via `GET http://calibre-web:8083/reconnect`.

### Mode B: Cold Forensic & Certificate A Auditing (Enterprise Isolation)
For absolute zero-risk forensic audits:
- The personal Calibre instance and Content Server must be fully stopped.
- Run using `docker-compose.yml` (App + Verifier + PostgreSQL + Valkey).
- Mounts library as `:ro` into the verifier container only. The Web app has zero library mount.

---

## 4. Calibre-Web Hot-Reload & Thumbnail Cache Purging

When Bookwarden updates book metadata, cover art, or author sorts, Calibre-Web must reflect the changes without requiring a container restart:

1. **HTTP Database Reconnection:** Bookwarden issues an HTTP `GET /reconnect` request to Calibre-Web, prompting SQLAlchemy to refresh its session from `metadata.db`.
2. **Direct Thumbnail Invalidation:** If `/thumbnails` is mounted, Bookwarden removes cached images for the modified book ID (`/thumbnails/<book_id>.*`), forcing Calibre-Web to regenerate the crisp HD cover on the next request.
3. **Fallback Worker Reload:** If configured with Docker access, Bookwarden sends a graceful `SIGHUP` to Calibre-Web worker processes (`pkill -HUP -f 'cps.py'`).

---

## 5. Recommended Environment Configuration (`.env.homelab`)

Create `.env.homelab` (or configure UnRAID/Docker Compose) with:

```env
# 1. Base Paths & Persistence
BOOKAUDIT_LIBRARY_PATH=/calibre
BOOKAUDIT_READ_ONLY=false
BOOKAUDIT_DATABASE__BACKEND=sqlite
BOOKAUDIT_DB_PATH=/config/bookaudit.db
BOOKAUDIT_ARTIFACTS_DIR=/config/artifacts
BOOKAUDIT_QUEUE__BACKEND=memory
BOOKAUDIT_RATE_LIMITS__BACKEND=memory

# 2. Calibre-Web Companion Settings
BOOKAUDIT_CALIBRE_WEB__URL=http://192.168.0.122:8083
BOOKAUDIT_CALIBRE_WEB__THUMBNAILS_DIR=/thumbnails
CALIBRE_WEB_CONTAINER=calibre-web-automated

# 3. Multi-Host Inference
BOOKAUDIT_LMSTUDIO_ENABLED=true
BOOKAUDIT_LMSTUDIO_BASE_URL=http://192.168.0.89:1234/v1
BOOKAUDIT_OLLAMA_ENABLED=true
BOOKAUDIT_OLLAMA_BASE_URL=http://192.168.0.122:11434/v1

# 4. Multimodal AI API
GEMINI_API_KEY=your-gemini-api-key

# 5. Paperless-ngx Bridge (Optional)
BOOKAUDIT_PAPERLESS__ENABLED=true
BOOKAUDIT_PAPERLESS__BASE_URL=http://192.168.0.122:8000
```

---

## 6. Verifying the Setup

```bash
# 1. System doctor check
uv run bookwarden doctor

# 2. Multi-host inference discovery
uv run bookwarden hosts

# 3. Run a 360° read-only audit
uv run bookwarden audit-360

# 4. Run automated test suite
uv run pytest -m "not v2_live and not benchmark"
```

# Calibre AI Auditor

<p align="center">
  <strong>The Content-Grounded Metadata & Cover Integrity Engine for Calibre Libraries.</strong><br>
  <em>"The book file is the ground truth. LLMs and OCR are witnesses."</em>
</p>

<p align="center">
  <a href="https://github.com/felix/calibre-ai-auditor/actions"><img src="https://img.shields.io/badge/CI-Passing-brightgreen.svg" alt="CI Status"></a>
  <a href="https://github.com/felix/calibre-ai-auditor"><img src="https://img.shields.io/badge/Python-3.12%20%7C%203.13-blue.svg" alt="Python Versions"></a>
  <a href="https://github.com/felix/calibre-ai-auditor"><img src="https://img.shields.io/badge/Docker-Multi--Arch-blue.svg" alt="Docker Multi-Arch"></a>
  <a href="https://github.com/felix/calibre-ai-auditor"><img src="https://img.shields.io/badge/unRAID-CA%20Template-orange.svg" alt="unRAID Support"></a>
  <a href="LICENSE"><img src="https://img.shields.io/badge/License-GPL--3.0-blue.svg" alt="License"></a>
</p>

---

## 🎯 What is Calibre AI Auditor?

Music has **Beets**, documents have **Paperless-ngx**, media has **Radarr/Plex**. Ebooks have spent 15 years trapped in desktop PyQt menus or suffering from corrupted SQLite locks and hallucinated LLM plugins.

**Calibre AI Auditor** fills this void: a headless, high-performance forensic curation engine for Calibre libraries. It verifies metadata and covers directly against the authentic container contents (EPUB OCF, PDF XMP, CBZ/CBR) with zero-risk read-only audits, deterministic authority sort rules, multimodal vision scoring, and surgical atomic rollbacks.

### ✨ Key Capabilities

- 🔍 **360° Forensic Audit Engine**: Directly inspects `metadata.db` for SQLite integrity, foreign key orphans, author sort desyncs, missing covers, and decompression bomb pixel sizes.
- 🖼️ **Cover Quality Score (CQS 0-100)**: Evaluates covers mathematically on resolution, aspect ratio (1:1.5 standard), Laplacian sharpness, contrast, and flags fake covers (interior body page scans, Calibre default brown templates, Z-Library watermarks).
- ⚡ **O(1) Streaming SQLite Engine**: Keyset pagination iterator streams 100,000+ books with <25 MB RAM consumption and zero table contention.
- 🏛️ **Canonical Authority Rules**: Standardizes sort keys and display names for Grecolatin philosophers, Patristic saints, Popes, Nobel laureates, and periodicals (*The Economist*, *Financial Times*).
- 🛡️ **Zero-Hallucination & Fail-Safe Architecture**: The book file is the absolute source of truth. Remote LLMs (Gemini 3.8 Flash, GPT-4o-mini) and OCR backends act strictly as witnesses.
- 🐳 **Self-Hosted Homelab Ready**: Ready-to-run Docker Compose, unRAID Community Applications template (`deploy/unraid/`), TrueNAS SCALE catalog chart, and instant Calibre-Web thumbnail cache purge.

---

## 🚀 Quickstart

### Option A: Local CLI via `uv` or `pipx` (Instant)

```bash
# Run a 360° forensic audit without modifying any files (Read-Only)
uvx --from git+ssh://git@192.168.0.122:2222/felix/calibre-ai-auditor.git bookaudit audit-360 --library "/path/to/Calibre Library"

# Synchronize author sort keys to canonical bibliographic standards
uvx --from git+ssh://git@192.168.0.122:2222/felix/calibre-ai-auditor.git bookaudit sync-library --library "/path/to/Calibre Library"
```

### Option B: Docker Compose (Self-Hosted Web UI)

```yaml
services:
  calibre-ai-auditor:
    image: ghcr.io/felix/calibre-ai-auditor:latest
    container_name: calibre-ai-auditor
    ports:
      - "8000:8000"
    volumes:
      - /mnt/user/data/media/books:/calibre:ro
      - /mnt/user/appdata/calibre-ai-auditor:/config
    environment:
      - BOOKAUDIT_READ_ONLY=true
      - GEMINI_API_KEY=your_gemini_key_here
```

---

## Release status

The repository implements the **Certificate A production candidate** for
version 1.2.1. Promotion requires an automatically triggered, green canonical
Gitea run for the exact commit and immutable image digest being deployed.

- Production mode is shadow/read-only only.
- The default image contains no Calibre executable, LLM SDK, vector client,
  MCP server, filesystem watcher, upload handler, or legacy WebUI.
- Only the verifier mounts the offline library, and the mount is read-only.
- PostgreSQL is the authoritative request, lease, fence, progress, and evidence
  store. Valkey is limited to rate limiting and verifier health.
- Tesseract is the only production OCR backend and is bounded by page and
  wall-clock limits.
- The app never receives a library mount and its database role cannot write
  evidence or worker progress.
- The backend listens on loopback. Caddy provides same-host TLS and whole-site
  authentication for private home/VPN access.

Certificate B—supervised metadata writes—remains quarantined. Its separately
built image and explicit Compose profiles are retained for future disposable
rehearsals, but the Certificate A validator rejects an enabled writer pilot.

## Production architecture

```text
private browser / VPN
        |
  TLS + whole-site auth
        |
  same-host Caddy
        |
  127.0.0.1:8080
        |
  Certificate A app -------- PostgreSQL (bookaudit_app ACL)
        |                         ^
        | request only            | atomic claim, fence, evidence
        v                         |
  PostgreSQL queue <------- read-only verifier
                                  |
                    /library:ro + private tmpfs scratch

Valkey: API rate limits + release/schema/library-bound verifier heartbeat
External providers: exact checksum-valid ISBN only; no book text or images
```

The app and verifier are separate processes and database roles. A verifier
claim increments a monotonic fence; every durable worker write must still own
that fence and a live lease. Recovery can reclaim an expired run, while a stale
worker is rejected from all subsequent writes.

## Certificate A workflow

1. Stop the Calibre desktop application, Content Server, and every other
   process that can touch `metadata.db` or book files.
2. Open the WebUI and explicitly confirm that Calibre is stopped.
3. Start a bounded audit. The app only inserts an immutable request.
4. The verifier snapshots `metadata.db`, freezes selected membership and format
   hashes, and audits from private scratch space.
5. Review run progress and sealed evidence. Cancel if the source was restarted
   or changed.
6. Treat `source_changed`, `blocked_recovery`, or stale-heartbeat states as hard
   operator stops. Never work around them by changing the database.

Supported production screens are Overview, Verify, and Evidence. Supported API
routes are documented in [docs/API.md](docs/API.md).

## First production deployment

Prerequisites are Docker Engine with Compose v2, a private DNS/VPN name for the
host, Caddy on that same host, and an existing Calibre library that can be
stopped during every audit.

```bash
cp .env.example .env
chmod 600 .env
# Replace every placeholder. Use distinct database passwords and a digest-pinned image.
# Keep COMPOSE_PROJECT_NAME=bookaudit-certificate-a; never reuse a legacy stack name.
./scripts/prepare-production.sh

./scripts/certificate-a-compose.sh up -d --wait postgres valkey
./scripts/certificate-a-compose.sh --profile maintenance run --rm provision-roles
./scripts/certificate-a-compose.sh --profile maintenance run --rm migrate
./scripts/certificate-a-compose.sh up -d --wait verifier app
./scripts/certificate-a-compose.sh --profile monitoring up -d --no-deps --wait app
./scripts/certificate-a-compose.sh --profile monitoring up -d --no-deps --wait prometheus
```

The idempotent role-provisioning step is required for both new and existing
PostgreSQL volumes; initdb hooks do not rerun on an existing volume. Runtime
services never provision roles or migrate the database. Run the migration
profile exactly once per reviewed upgrade.

Start the reviewed private edge only after the loopback readiness check passes:

```bash
./scripts/certificate-a-compose.sh --profile edge up -d --no-deps --wait caddy
```

`BOOKAUDIT_BASIC_AUTH_HASH` must be a Caddy-supported password hash, not the
plaintext password. After Caddy login, the WebUI asks for the separate internal
API key and holds it only in page memory; a reload requires it again. The app
port remains bound to `127.0.0.1`; the edge is published only on the configured
Tailscale IP. See the complete
[production operations runbook](docs/runbooks/production-operations.md) before
an upgrade, backup, restore, or incident.

## Development

Python 3.12.13 and Node 20.19.4 are the tested toolchain.

```bash
uv sync --frozen
uv run ruff check .
uv run ruff format --check .
uv run mypy src
uv run pytest \
  -m 'not benchmark and not ocr_live and not network' \
  --ignore=tests/test_retention_postgres_valkey.py \
  --ignore=tests/test_v2_supervised_pilot_integration.py

cd webui
npm ci
npm audit --audit-level=high
npm run lint -- --max-warnings=0
npm run build
npx playwright test --project=chromium --project=mobile-chromium
```

The canonical Gitea pipeline adds disposable PostgreSQL ACL and concurrency
abuse tests, real Calibre/Tesseract boundary tests, Python and npm dependency
audits, both browser viewports, the 50k metadata microbenchmark, separated image
builds, exact Compose-graph validation, and vulnerability/secret scans.

The convenience command below runs the deterministic local backend gate. It
does not replace the Gitea live-service or container gates.

```bash
./scripts/verify-calibre-gate.sh
```

## Safety invariants

- Never test against a running or live-mounted library.
- Never use a library write as a readiness or integration probe.
- Do not mount the library into the app, PostgreSQL, or Valkey containers.
- Do not enable `writer` or `writer-maintenance` profiles for Certificate A.
- Do not reuse a legacy/shadow Compose project. Preflight requires the dedicated
  `bookaudit-certificate-a` project and rejects unexpected services in it.
- Do not enable auto-apply or the supervised pilot; preflight rejects both.
- Do not add cloud OCR, LLM, vision, upload, watcher, MCP, Paperless, Qdrant,
  Gotenberg, Tika, manga, or Content Server paths to the production closure.
- Do not retry or manually edit a `source_changed` or `blocked_recovery` run.
- Do not treat an old green CI run as evidence for a new commit or image.

The isolated legacy CLI and future writer work remain available to developers
through the explicit `writer` image, but they are not part of the Certificate A
support or threat model.

## Documentation

- [Production readiness](docs/production-readiness.md)
- [Production operations](docs/runbooks/production-operations.md)
- [Certificate A API](docs/API.md)
- [Architecture](docs/ARCHITECTURE.md)
- [Safety model](docs/SAFETY.md)
- [ADR-005: Certificate A production boundary](docs/decisions/ADR-005-certificate-a-production-boundary.md)
- [Testing](TESTING.md)

Historical V1, remote Content Server, and supervised writer documents remain
for research and future Certificate B work. They do not override ADR-005 or the
Certificate A runbook.

## License

GPL-3.0-or-later. See [LICENSE](LICENSE) for the canonical text and
[LICENSE.md](LICENSE.md) for the short notice.

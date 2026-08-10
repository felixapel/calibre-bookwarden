# Installation

## Production

Certificate A production is container-only. Required host components:

- Linux with Docker Engine and Compose v2;
- a private DNS/VPN hostname and same-host Caddy;
- an existing Calibre library that can be fully stopped during every audit;
- x86_64 or another architecture for which you build and scan the pinned base
  images yourself.

The Certificate A image contains Python 3.12.13, Tesseract, Ghostscript, qpdf,
the production WebUI, and the minimal Python closure. It deliberately does not
contain Calibre.

```bash
cp .env.example .env
chmod 600 .env
# Replace every placeholder; retain COMPOSE_PROJECT_NAME=bookaudit-certificate-a.
./scripts/prepare-production.sh
./scripts/certificate-a-compose.sh up -d --wait postgres valkey
./scripts/certificate-a-compose.sh --profile maintenance run --rm provision-roles
./scripts/certificate-a-compose.sh --profile maintenance run --rm migrate
./scripts/certificate-a-compose.sh up -d --wait verifier app
```

The dedicated Compose project name isolates Certificate A volumes, networks,
and containers from legacy/shadow deployments. Preflight rejects the old
`calibre-ai-auditor` project name and any unexpected service already present in
the Certificate A project. Use `scripts/certificate-a-compose.sh` for every
Certificate A operation; it pins the project, Compose file, environment file,
and allowed profiles even if the host shell exports conflicting Compose values.

Always run the idempotent `provision-roles` maintenance task before Alembic.
PostgreSQL does not rerun initdb hooks for an existing data volume.

Continue with the [production runbook](docs/runbooks/production-operations.md)
before exposing the Caddy hostname.

## Development

Use Python 3.12.13, `uv` 0.11.15, and Node 20.19.4.

On Debian/Ubuntu, real OCR and integration tests need:

```bash
sudo apt-get update
sudo apt-get install -y \
  calibre fonts-dejavu-core ghostscript postgresql-client qpdf \
  redis-tools tesseract-ocr tesseract-ocr-eng
```

Install the locked Python environment:

```bash
uv python install 3.12.13
uv sync --python 3.12.13 --frozen
uv run bookaudit-certificate-a --help
```

Install the locked WebUI separately:

```bash
cd webui
npm ci
npm run lint -- --max-warnings=0
npm run build
```

The default uv development group includes test, type, lint, and quarantined
legacy imports so the complete repository test suite remains runnable. Optional
extras are not part of the Certificate A image:

- `legacy`: LLM/vector/template/upload compatibility code;
- `mcp`: FastMCP research surface;
- `ingest`: filesystem watcher research surface;
- `ocr`: PaddleOCR development comparison.

Do not point native development commands at a live library. Use synthetic
fixtures and disposable PostgreSQL/Valkey services. The canonical real-boundary
tests create their own Calibre library and raster PDF.

## Verification

```bash
./scripts/verify-calibre-gate.sh

cd webui
npm audit --audit-level=high
npx playwright test --project=chromium --project=mobile-chromium
```

The local gate excludes live OCR and disposable-service suites. The canonical
Gitea pipeline must pass those gates without skips for a release commit.

## Common problems

- **Preflight rejects `.env`:** use mode 0600, replace every placeholder, use
  five distinct 24+ character PostgreSQL passwords, and make every DSN password
  match its role variable.
- **Image/release mismatch:** `BOOKAUDIT_IMAGE` must end in `@sha256:<64 hex>`;
  `BOOKAUDIT_RELEASE_DIGEST` must be that exact digest.
- **Verifier refuses the library:** stop Calibre and Content Server, confirm the
  path and read permission, and investigate rather than delete any SQLite
  sidecar. Certificate A supports reviewed Calibre schema versions 25-27 and
  deliberately rejects unknown or marker-mismatched databases.
- **Readiness says verifier unavailable:** require the same release digest,
  Alembic head, and library root in app and verifier; then inspect the verifier
  logs and Valkey connectivity.
- **Browser cannot connect:** keep the app on loopback and fix same-host Caddy,
  DNS/VPN routing, trusted hosts, certificate, or authentication configuration.
  Do not expose the backend port as a shortcut.

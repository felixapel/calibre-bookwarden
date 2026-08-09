# Calibre AI Auditor

Calibre AI Auditor is a local, content-grounded metadata auditor for Calibre
libraries. Its production contract is deliberately narrow: it reads a stopped
Calibre library, gathers bounded evidence, checks checksum-valid ISBNs against
Google Books and Open Library, and presents sealed findings for human review.
It does not modify Calibre metadata.

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
./scripts/prepare-production.sh

docker compose up -d --wait postgres valkey
docker compose --profile maintenance run --rm provision-roles
docker compose --profile maintenance run --rm migrate
docker compose up -d --wait verifier app
```

The idempotent role-provisioning step is required for both new and existing
PostgreSQL volumes; initdb hooks do not rerun on an existing volume. Runtime
services never provision roles or migrate the database. Run the migration
profile exactly once per reviewed upgrade.

Install the same-host edge only after the loopback readiness check passes:

```bash
sudo install -m 0644 deploy/caddy/Caddyfile.example /etc/caddy/Caddyfile
caddy hash-password
# Store BOOKAUDIT_DOMAIN, BOOKAUDIT_BASIC_AUTH_USER,
# BOOKAUDIT_BASIC_AUTH_HASH, BOOKAUDIT_PORT, and BOOKAUDIT_ACME_EMAIL in the
# root-readable environment used by the Caddy service.
sudo caddy validate --config /etc/caddy/Caddyfile
sudo systemctl reload caddy
```

`BOOKAUDIT_BASIC_AUTH_HASH` must be a Caddy-supported password hash, not the
plaintext password. After Caddy login, the WebUI asks for the separate internal
API key and holds it only in page memory; a reload requires it again. The app
port remains bound to `127.0.0.1`; do not publish it to the LAN. See the complete
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

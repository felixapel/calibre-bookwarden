# Usage Guide: Calibre Bookwarden

Certificate A is a stopped-library, read-only auditor. The development application contains additional research tools and does not establish production readiness.

## 1. Read-only diagnostics

```sh
uv run bookwarden audit-360 --library "/path/to/stopped-or-restored-library"
```

This reports database integrity, missing formats and cover/metadata diagnostics. Treat classifications as review proposals. Keep reports local because they may contain private library metadata.

The development Cover Deck is an inspection view. It has no supported apply or undo action. The production SPA remains the supported verification and sealed-evidence review interface.

Legacy `optimize-covers`, `sync-library`, `curate-periodicals` and `full-audit-run` commands are retired and reject before writing. No flag, including `--yes` or disabling read-only configuration, bypasses this boundary. There is no automatic duplicate merge or cache/reconnect operation.

A database snapshot is not a full-library backup. Before any separately authorized supervised pilot, verify restoration of the complete library and exclude other writers. See [the remediation plan](docs/REMEDIATION_PLAN.md) and [production operations](docs/runbooks/production-operations.md).

## 2. Manifestation V2 Verification (Exact-Edition Evidence)

Manifestation V2 treats each physical format as immutable ground truth, resolves edition identity using exact identifiers, and records sealed Tier A/B/C packages.

### 2.1 Running a Shadow Pilot
Run an exact-manifestation audit across your library without modifying files:

```bash
# 100 books pilot with native format extraction + local Tesseract OCR
bookwarden verify --pipeline v2 --limit 100 --use-ocr

# Full library audit exported to JSON
bookwarden verify --limit 0 --format json > v2-library-audit.json
```

**Decision Tiers:**
- **Tier A (Exact Match)**: Internal container evidence and external records completely agree. Ready for review.
- **Tier B (Incomplete)**: Missing identifiers or single unconfirmed OCR candidate. Requires human review.
- **Tier C (Conflict)**: Discrepancy between attached formats or contradictory provider data. Never applied automatically.

---

### 2.2 Supervised Correction & Rollback
To apply a verified Tier A patch:
1. Inspect the evidence package in the WebUI or via `GET /api/review/v2/{evidence_id}`.
2. Authorize the patch: `POST /api/review/v2/{evidence_id}/authorize`.
3. Submit the change to the writer queue: `POST /api/apply/v2`.

If a change needs to be reverted:
```bash
bookwarden undo <change_id>
```

---

## 3. Remote Content Server Auditing (Zero-Filesystem Access)

For libraries stored on remote servers or where direct filesystem access is restricted, audit through an SSH tunnel:

```bash
# 1. Collect aggregate inventory counts only
bookwarden inventory \
  --content-server http://127.0.0.1:18086 \
  --library-id "Main_Library" \
  --username "readonly_user" \
  --source-identity SHA256:VERIFIED_SSH_HOST_FINGERPRINT \
  --output reports/remote_inventory.json

# 2. Verify books remotely into a scratch directory
bookwarden verify-content-server \
  --content-server http://127.0.0.1:18086 \
  --library-id "Main_Library" \
  --username "readonly_user" \
  --source-identity SHA256:VERIFIED_SSH_HOST_FINGERPRINT \
  --scratch-root reports/scratch \
  --limit 10 --use-ocr
```

---

## 4. Ingestion from Paperless-ngx

Calibre Bookwarden includes a native bridge to import ebooks, whitepapers, and academic papers managed in Paperless-ngx:

```bash
# Test connection and import up to 50 candidate documents
bookwarden ingest-paperless --limit 50
```

---

## 5. Homelab Inference & AI Witnesses

Discover and check the health of configured local inference hosts (RTX 3090, Ollama, LM Studio):

```bash
bookwarden hosts
```

---

## 6. System Diagnostics (`doctor` & `config`)

```bash
# Verify system binaries and environment paths
bookwarden doctor

# Inspect effective configuration with redacted secrets
bookwarden config
```

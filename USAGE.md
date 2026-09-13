# Usage Guide: Calibre Bookwarden

**Calibre Bookwarden** (invoked via `bookwarden` or the backward-compatible `bookaudit` alias) provides a comprehensive suite of tools spanning high-speed 360° forensic auditing, interactive visual cover triage, deterministic bibliographic authority synchronization, and Manifestation V2 exact-edition verification.

---

## 1. 360° Forensic Auditing & Homelab Curation (v1.3.0)

For homelabs and power users, the v1.3.0 forensic engine operates directly against the Calibre SQLite database (`DirectCalibreEngine`) at speeds exceeding **6,000 books per second** without running heavy LLM calls or writing unverified metadata.

### 1.1 Deep Read-Only Forensic Health Check
Run a non-destructive audit of the entire library:

```bash
# Terminal formatted visual summary
bookwarden audit-360 --library "/mnt/user/MEDIA/Books/Calibre Library"

# Or output full JSON data for automated monitoring
bookwarden audit-360 --library "/mnt/user/MEDIA/Books/Calibre Library" --json > audit_360_report.json
```

**Key issues diagnosed:**
- SQLite database corruption or index desync (`PRAGMA integrity_check`).
- Broken foreign keys in junction tables (`books_authors_link`, `books_tags_link`, `data`).
- Ebook files registered in Calibre but missing from disk (`FILE_MISSING_ON_DISK`).
- Empty book records with no formats attached.
- Oversized cover decompression bombs (>30MP, >10MB) or tiny thumbnail stubs (<200px).
- Inverted or non-standard author sort strings.

---

### 1.2 Cover Optimization & "Cover Deck" Visual Triage

#### A. Command-Line Cover Optimization:
Neutralize oversized covers and compress them into optimized, high-resolution JPEGs:

```bash
bookwarden optimize-covers --library "/mnt/user/MEDIA/Books/Calibre Library"
```

#### B. Interactive Cover Deck Triage (Web UI):
For fast, keyboard-driven cover review, launch the Web UI:

```bash
bookwarden web --port 8080
```

1. Open your browser at `http://localhost:8080/api/covers/ui/deck` (or via the **Cover Studio** in the WebUI).
2. The interface presents a swipeable card stack of books with low CQS scores (Tier C/D) or spurious covers.
3. Use keyboard shortcuts:
   - **Left Arrow (`←`)**: Skip / Keep current cover.
   - **Right Arrow (`→`)**: Accept high-res candidate fetched from OpenLibrary or Hardcover.
   - **Spacebar**: Preview cover at full resolution.

---

### 1.3 Bibliographic Authority & Author Sort Synchronization
Enforce canonical "Last, First" conventions for authors, handling complex nobility particles (`von Goethe`, `de Saint-Exupéry`, `de Beauvoir`), titles (`Thomas Aquinas, Saint`), and corporate publishers:

```bash
# Synchronize authors and clean orphan junction rows
bookwarden sync-library --library "/mnt/user/MEDIA/Books/Calibre Library"

# Also purge empty book records that have no physical files
bookwarden sync-library --library "/mnt/user/MEDIA/Books/Calibre Library" --purge-empty
```

---

### 1.4 Curate Automated News & Periodicals
Clean up Calibre automated recipe downloads (*Financial Times*, *The Economist*, *Der Spiegel*), assigning canonical publishers, tags, and 5-star ratings:

```bash
bookwarden curate-periodicals --library "/mnt/user/MEDIA/Books/Calibre Library"
```

---

### 1.5 The Master Unattended Maintenance Pipeline (`full-audit-run`)
Execute an end-to-end maintenance run safely. This command creates an atomic pre-flight snapshot (`metadata.db.bak_<timestamp>`), audits the library, optimizes oversized covers, synchronizes author sorts, cleans foreign keys, and notifies `calibre-web-automated` to reload:

```bash
bookwarden full-audit-run --library "/mnt/user/MEDIA/Books/Calibre Library" --purge-web --yes
```

---

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

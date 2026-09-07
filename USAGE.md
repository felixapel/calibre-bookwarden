# Usage Guide

**Calibre Bookwarden** (invoked via `bookwarden` or the backward-compatible `bookaudit` alias) provides one unified Manifestation V2 workflow across CLI, API, and WebUI, while retaining historical V1 inspection compatibility:

1. **CLI/API/WebUI (V2 default)** — exact-edition, all-format, sealed audits and
   supervised per-package correction
2. **Legacy V1 data** — historical/read-only in the V2 Review surface; the
   legacy apply route is permanently retired

This guide walks through the canonical workflows for each.

---

## 1. Manifestation V2 workflow (recommended)

Run a shadow pilot; this never modifies Calibre:

```bash
bookaudit verify --pipeline v2 --limit 100 --use-ocr --format json > v2-pilot.json
```

For each book, V2 snapshots the current Calibre metadata, hashes and inspects
every attached format, optionally OCRs PDF front matter, queries Google Books
and Open Library by one exact checksum-valid ISBN, and persists a checksummed Tier
A/B/C package. One book failure is recorded and the next book continues.

- **Tier A**: exact internal and structured external manifestation evidence,
  complete core-field agreement; review the canonical patch.
- **Tier B**: missing/incomplete evidence, a single OCR/vision candidate, or an
  unreadable format; human research is required.
- **Tier C**: conflicting formats or exact provider records; do not apply.

LLM and cover vision are recognition aids, not authorities. Use remote consent
flags only after enabling the corresponding global privacy setting. All allowed
egress produces a receipt in the sealed package.

To correct a Tier A book, authorize its exact evidence ID through
`POST /api/review/v2/{evidence_id}/authorize`, then queue it with
`POST /api/apply/v2` and `force=true`. The sole writer verifies that Calibre
metadata, attached-format membership, paths, and ebook hashes have not changed,
creates rollback artifacts, applies canonical fields, and reads the result back.
See [docs/API.md](docs/API.md) for request bodies.

### Remote Content Server inventory and shadow verification

When direct filesystem access would increase risk, create an SSH tunnel outside
the auditor and point the capability-limited source at its loopback endpoint.
First collect aggregate counts only:

```bash
bookaudit inventory \
  --content-server http://127.0.0.1:18086 \
  --library-id EXACT_LIBRARY_ID \
  --username READONLY_USER \
  --source-identity SHA256:VERIFIED_SSH_HOST_FINGERPRINT \
  --output reports/remote-audit/inventory.json
```

`inventory` does not open the auditor database or initialize providers, OCR,
vision, or LLMs. Its optional output must remain beneath the current working
directory and is created mode `0600`. It contains aggregate counts and format
statistics, never titles, authors, identifiers, filenames, or paths.

After reviewing that result, migrate the local auditor database and verify one
book in shadow mode:

```bash
bookaudit migrate
bookaudit verify-content-server \
  --content-server http://127.0.0.1:18086 \
  --library-id EXACT_LIBRARY_ID \
  --username READONLY_USER \
  --source-identity SHA256:VERIFIED_SSH_HOST_FINGERPRINT \
  --scratch-root reports/remote-audit/scratch \
  --limit 1 --use-ocr
```

The remote command requires an already-current schema. It defaults to one book,
local OCR enabled, public providers disabled, and all LLM, vision, remote-text,
and remote-image egress rejected. If the remote record or format set changes
during inspection, the result is `source_changed`. Remote evidence remains
ineligible for writer authorization. See the
[disposable lab runbook](docs/runbooks/disposable-calibre-lab.md) before using a
real server.

## 2. WebUI Manifestation V2 workflow

The WebUI starts shadow V2 audits and reviews their sealed evidence. It never
turns a Tier B/C result or a historical V1 flag into a write.

### Dashboard

Start at the **Dashboard** to see:
- System health (Calibre CLI, Tika, Qdrant)
- Homelab inference host discovery (3090 + Unraid Ollama + remote)
- Historical V1 aggregate counters, clearly separate from V2 review
- Review queue size, duplicates count, applied fixes

### Verify (V2) page

The **Verify** page explicitly submits `pipeline: "v2"` with local bounded OCR,
vision and remote egress disabled, and the optional LLM witness off by default.

1. Navigate to **Verify (V2)** in the sidebar.
2. Set **Limit** (e.g. `50` books for a pilot run, `0` for the full library).
3. Toggle the local non-authoritative LLM transcription witness only if needed.
4. Click **Run Manifestation V2**. The progress bar updates every 1.5s.
5. When the run completes, click the row in **Recent verify runs** to drill
   into per-book evidence summaries.

**Terminal-state semantics**:

- `shadowed` — Tier A exact identity was sealed; any canonical patch still
  requires manual review and pilot authorization
- `review` — Tier B incomplete evidence; no write control
- `deferred` — Tier C conflict; no write control
- `failed` / `blocked_recovery` — inspect and resolve before proceeding

### Review page — sealed exact-manifestation evidence

The Review page lists validated V2 packages and lets the operator inspect:

- exact evidence/package ID and SHA-256 seal
- every attached format, path and hash
- internal/external source provenance and locators
- Tier/risk reasons and current-versus-canonical-patch values
- exact authorization and sanitized operation state

Only a Tier A package with a nonempty patch can be authorized. Queueing is a
separate second action and submits exactly that evidence ID plus its returned
authorization ID. Tier B/C controls are disabled. V1 results show a historical
read-only notice.

### Other pages

- **Scan Library** — Triggers a Calibre DB scan (legacy ingestion path; V2 uses
  Verify for exact-manifestation auditing)
- **Inspect File** — Single-file inspection; useful before adding to library
- **Duplicates** — Qdrant-backed semantic duplicate detection
- **Changes & Undo** — v1.0 restore points + API-queued run revert
- **Settings** — Provider config, privacy filters, local API key

---

## 3. Additional CLI workflows

The CLI is ideal for batch jobs, CI, and offline single-file inspection.

### Sanity checks

```bash
# Verify all dependencies are reachable
bookaudit doctor

# Discover homelab inference hosts (3090 + Unraid Ollama)
bookaudit hosts
```

### Manifestation V2 verification

```bash
# Pilot: 100 books, local extraction/OCR, text output
bookaudit verify --pipeline v2 --limit 100 --use-ocr

# Add bounded LLM transcription evidence (still non-authoritative)
bookaudit verify --limit 100 --use-llm

# Full library as JSON; resume with the emitted run_id if interrupted
bookaudit verify --limit 0 --format json > manifestation-v2-audit.json
```

`bookaudit apply` is retired and exits nonzero; `POST /api/apply` returns HTTP
410. Use the V2 authorization and queue endpoints. `bookaudit undo` accepts one numeric
`change_id`, not a run ID.

To close one persisted pilot after first stopping app intake and the writer:

```bash
bookaudit pilot-stop pilot-YYYYMMDD --yes
```

If a completed V2 operation is exactly `failed`, stop intake/writer, close its
exact pilot with `pilot-stop`, and first prove from live Calibre plus hashed
recovery evidence that no unresolved write remains. Then append the incident
record (never for `unknown` or `restore_failed`). The stopped ID remains closed;
continuation requires a new reviewed pilot:

```bash
bookaudit incident-ack OPERATION_ID --actor OPERATOR \
  --reason "Verified Calibre matches before_metadata and recovery hashes" --yes
```

### Single-file inspection (no Calibre library required)

```bash
# Extract metadata from one file
bookaudit inspect --path "/path/to/book.epub"

# Skip provider fetching (deterministic only)
bookaudit inspect --path "/path/to/book.epub" --no-providers
```

### Library management (legacy v0.9 paths, still work)

```bash
# Scan a Calibre library into the auditor DB
bookaudit scan --limit 100

# Run the v0.9 audit engine (one-shot LLM call per book)
bookaudit audit --run latest

# Roll back via OPF backup
bookaudit undo <change_id>
```

---

## 4. Real-world calibration

Use calibration to measure the pipeline against your actual library. It does
not enable automatic writes. The full procedure lives in
[docs/calibration/manifestation-v2-runbook.md](docs/calibration/manifestation-v2-runbook.md).

Summary:
1. Run a shadow pilot and review exact manifestations, not just works.
2. Label at least 100 packages, including at least 50 Tier A decisions.
3. Generate an integrity-checksummed advisory report with `bookaudit calibrate-v2`.
4. Require zero false hypothetical automatic writes and the configured
   false-positive rate before considering any future authenticated gate.
5. Repeat whenever policy, extractors, providers, models, or the representative
   library distribution changes.

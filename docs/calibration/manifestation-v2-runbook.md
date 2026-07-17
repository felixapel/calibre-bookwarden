# Manifestation V2 calibration and supervised rollout

## Objective

Measure exact-edition precision on the real library before considering any
unattended metadata write. Verification is shadow-only by default. Creating a
calibration report does not modify Calibre and does not enable an automatic
writer by itself.

## 1. Prerequisites

- Back up the Calibre library, database, and auditor artifacts.
- Keep the library mount read-only during calibration.
- Apply migrations with `bookaudit migrate`.
- Verify `calibredb`, `ebook-meta`, `ebook-convert`, and `ocrmypdf` with
  `bookaudit doctor`.
- If edition statements may be written later, create the Calibre text custom
  column `#edition` before any supervised apply.

Recommended configuration:

```yaml
privacy:
  allow_remote_text: false
  allow_remote_images: false
  max_remote_chars: 4000
  max_remote_images: 1

recognition_v2:
  ocr:
    enabled: true
    backends: [tesseract]
    max_pages: 6
    language: en
  vision:
    enabled: false

manifestation_v2:
  auto_apply:
    enabled: false
    calibration_report: null
    min_sample_size: 100
    min_tier_a_decisions: 50
    max_false_positive_rate: 0.0
    max_report_age_days: 30
  supervised_pilot:
    enabled: false
    pilot_id: null
    release_digest: null
    max_operations: 5
```

One OCR backend is intentionally non-authoritative. Configure two distinct
installed backends, for example `[tesseract, paddleocr]`, only if their runtime
dependencies are available. Agreement then produces `ocr_consensus`; it does
not create a second independent content root.

## 2. Shadow pilot

Start with 100 books:

```bash
bookaudit verify --pipeline v2 --limit 100 --use-ocr --no-vision \
  --no-llm --format json > /tmp/manifestation-v2-pilot.json
```

The JSON envelope remains:

```json
{
  "run_id": "verify_...",
  "elapsed_seconds": 12.3,
  "verdicts": [
    {
      "schema_version": 2,
      "evidence_id": "evidence_...",
      "book_key": "calibre:1",
      "state": "shadowed",
      "identity": {"tier": "A", "risk_flags": [], "auto_patch": {}}
    }
  ]
}
```

Review the persisted full package through `GET /api/verify/{run_id}`. Check all
attached formats, evidence locators, exact ISBN, core-field agreement, provider
records, privacy receipts, and proposed patch. Do not label from the Calibre
record alone; compare the physical/digital title and copyright pages and an
independent authoritative catalog where possible.

The package also seals the absolute Calibre library root. A writer configured
for any other root rejects the package. Symlinks in that root, ebook parent
directories, or the artifacts tree are hard failures rather than aliases.

For an active remote library, do not substitute a direct mount for the command
above. First run aggregate `bookaudit inventory` through an operator-created
loopback SSH tunnel, migrate the local auditor database, and then start with
`bookaudit verify-content-server --limit 1`. Remote packages use source-bound
logical references, return `source_changed` if membership changes, and can
populate the reviewed corpus, but they are permanently ineligible for the
writer. Any apply/readback/undo rehearsal must use a restored isolated clone.
See [ADR-004](../decisions/ADR-004-read-only-content-server-inventory.md).

## 3. Optional LLM and vision experiments

Local LLM text:

```bash
bookaudit verify --pipeline v2 --limit 20 --use-ocr --use-llm
```

Remote text requires both `privacy.allow_remote_text: true` and
`--allow-remote-text`. Remote vision additionally requires
`recognition_v2.vision.enabled: true`, `privacy.allow_remote_images: true`,
`--use-vision`, and `--allow-remote-images`.

LLM and vision evidence must remain `authoritative: false`. If either changes a
Tier B/C package into Tier A by itself, stop: that violates ADR-002.

## 4. Build the reviewed corpus

Create JSON containing unique labels for persisted evidence packages. Include
both clean and difficult books, every supported format, translations, revised
editions, boxed/omnibus volumes, scans, comics, missing ISBNs, provider
conflicts, and intentionally wrong Calibre records.

```json
{
  "schema_version": 1,
  "policy_version": "manifestation-v2",
  "observations": [
    {
      "evidence_id": "evidence_abc",
      "tier": "A",
      "would_auto_apply": true,
      "identity_correct": true,
      "patch_correct": true
    },
    {
      "evidence_id": "evidence_def",
      "tier": "B",
      "would_auto_apply": false,
      "identity_correct": false,
      "patch_correct": false
    }
  ]
}
```

`identity_correct` means the exact manifestation is correct, not merely the
work. `patch_correct` means every field and value in the proposed canonical
patch is correct. `would_auto_apply` labels the decision under the exact policy
being calibrated.

## 5. Derive and seal the report

```bash
bookaudit calibrate-v2 \
  --corpus /secure/reviewed-manifestations.json \
  --output /secure/manifestation-v2-calibration.json \
  --valid-days 30
```

The command derives counts from the labels, hashes the corpus, rejects duplicate
evidence IDs, and atomically writes a mode-0600 integrity-checksummed report. The
checksum is not a signature and the labels are not authenticated. The advisory policy
requires at least 100 reviewed books, at least 50 Tier A decisions, zero false
auto-applies, zero Tier A false positives, and a report no older than 30 days.

Test the report parser with `manifestation_v2.auto_apply.enabled: false`. A
changed report, expired report, symlink, wrong policy version, insufficient
sample, or nonzero false write fails validation. Even a valid report cannot
enable automatic writes; `tier_a_auto` is rejected unconditionally.

## 6. Required rehearsal before a live pilot

Do not enable the pilot merely because shadow results look plausible. For the
exact commit and image digest, require a green Gitea job named `Manifestation V2
required integration`; that job fails if its real Calibre/Tesseract/PostgreSQL/
Valkey apply-readback-undo test skips. Then:

1. Run the same flow on a disposable Calibre library.
2. Restore the paired database/artifact backup and a copy of the real library
   into an isolated clone path.
3. Create new evidence against that clone; evidence sealed for another absolute
   library root is correctly rejected.
4. Apply one reviewed Tier A patch, verify Calibre readback and every rollback
   artifact, queue its undo, and verify the original values return.
5. Confirm there are no `unknown`, `restore_failed`, or nonterminal ledger rows
   and no unpublished outbox rows.

Any missing gate leaves the live deployment shadow-only.

## 7. Start a serial max-five pilot

Stop intake and the writer, take the paired backup, and edit the operator-owned
`.env`. The image digest and pilot digest must be identical:

```dotenv
BOOKAUDIT_IMAGE=registry.example/bookaudit@sha256:<64-hex-digest>
BOOKAUDIT_REQUIRE_WRITER_READY=true
BOOKAUDIT_MANIFESTATION_V2__AUTO_APPLY__ENABLED=false
BOOKAUDIT_MANIFESTATION_V2__SUPERVISED_PILOT__ENABLED=true
BOOKAUDIT_MANIFESTATION_V2__SUPERVISED_PILOT__PILOT_ID=pilot-YYYYMMDD
BOOKAUDIT_MANIFESTATION_V2__SUPERVISED_PILOT__RELEASE_DIGEST=sha256:<64-hex-digest>
BOOKAUDIT_MANIFESTATION_V2__SUPERVISED_PILOT__MAX_OPERATIONS=5
```

```bash
docker compose stop app writer
chmod 600 .env
./scripts/prepare-production.sh
docker compose --profile maintenance run --rm migrate
docker compose up -d writer app
```

Authenticated `/api/health/ready` must be ready, and the metrics
`bookaudit_v2_pilot_enabled` and `bookaudit_v2_writer_binding_ok` must both be
`1`. Readiness checks the same pilot ID, maximum, release digest, Alembic head,
and canonical library-root hash carried by the writer heartbeat. Do not
authorize or queue while a V2 alert is firing.

Use the WebUI Review page or the authenticated API. First authorize one exact
package after inspecting every format/hash, internal locator, exact provider
record, current value and proposed value:

```bash
curl -X POST "$BOOKAUDIT_URL/api/review/v2/$EVIDENCE_ID/authorize" \
  -H "X-API-Key: $BOOKAUDIT_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{"reason":"Checked title page, copyright page, ISBN and provider record"}'
```

Then queue exactly that evidence ID and returned authorization ID:

```bash
curl -X POST "$BOOKAUDIT_URL/api/apply/v2" \
  -H "X-API-Key: $BOOKAUDIT_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "force": true,
    "evidence_id": "'"$EVIDENCE_ID"'",
    "authorization_id": "'"$AUTHORIZATION_ID"'"
  }'
```

The request queues only one operation. The next request is rejected until every
nonterminal ledger row is terminal and every outbox event is published, except
for a separately reconciled terminal `failed` row with an append-only incident
acknowledgement. Inspect
`GET /api/operations/{operation_id}`, the Calibre record, attached ebook hashes,
and rollback artifacts before considering the next canary. Never exceed the
persisted budget, even after a failure or undo; reservations are intentionally
not refunded.

## 8. Stop and reconcile the pilot

At the first stop condition, do not queue another operation:

```bash
docker compose stop app writer
# Edit .env and set SUPERVISED_PILOT__ENABLED=false before continuing.
docker compose run --rm app pilot-stop "$PILOT_ID" --yes
```

The command closes the exact persisted row under the same lock used by queue
reservation. It makes queued V2 work fail writer revalidation, but cannot
interrupt a Calibre subprocess that had already begun; stopping the writer
first is mandatory. Preserve the database, library and writer artifacts, then
reconcile all nonterminal/failed states under the production operations
runbook. Only a new reviewed pilot ID and a new explicit approval may resume
writes. Re-enabling a stopped ID is unsupported.

If and only if the operation ended in `failed` (never `unknown` or
`restore_failed`), its outbox is failed, no writer lease remains, and live
Calibre plus hashed recovery evidence prove no unresolved mutation, follow the
production runbook's `bookaudit incident-ack` procedure. The acknowledgement
preserves the original ledger/outbox and consumes no less budget; it merely
allows a new reviewed pilot to pass that historical failed-outbox gate.

## Stop conditions

- Any exact-ID conflict, format disagreement, missing rollback artifact, live
  metadata drift, package seal mismatch, or privacy receipt discrepancy.
- Any library-root mismatch, symlinked path component, unavailable Linux memfd
  sealing, or artifact that cannot be passed to Calibre by descriptor.
- Any Tier A false positive or false automatic patch in the labeled corpus.
- Any remote payload without both configured permission and per-run consent.
- Any apply that cannot restore OPF, `#edition`, and the previous cover.
- A stale/mismatched writer binding, any V2 operation stalled for ten minutes,
  any failed/unknown/restore-failed operation, or exhausted pilot budget.
- Any attempt to batch evidence IDs, overlap operations, reuse a stopped pilot
  ID, or run beyond five reserved operations.

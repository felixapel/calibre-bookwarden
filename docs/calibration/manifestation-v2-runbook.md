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

## 6. Supervised V2 apply and undo

Use the authenticated API. First authorize one exact package:

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
    "evidence_ids": ["'"$EVIDENCE_ID"'"],
    "authorization_ids": {"'"$EVIDENCE_ID"'":"'"$AUTHORIZATION_ID"'"}
  }'
```

The request only queues work. The sole privileged writer performs the Calibre
mutation after revalidation. Confirm the resulting Calibre metadata and test
`POST /api/undo/{change_id}` with `{"force": true}` on a disposable library
before using the production library.

## Stop conditions

- Any exact-ID conflict, format disagreement, missing rollback artifact, live
  metadata drift, package seal mismatch, or privacy receipt discrepancy.
- Any library-root mismatch, symlinked path component, unavailable Linux memfd
  sealing, or artifact that cannot be passed to Calibre by descriptor.
- Any Tier A false positive or false automatic patch in the labeled corpus.
- Any remote payload without both configured permission and per-run consent.
- Any apply that cannot restore OPF, `#edition`, and the previous cover.

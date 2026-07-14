# Safety First

This project adheres to a strict "read-only by default" philosophy to protect
Calibre libraries and metadata files. Manifestation V2 verifies in shadow mode
by default and adds sealed evidence, exact authorization, a sole privileged
writer, and reversible per-book artifacts. Nothing changes the library unless
the operator explicitly opts in.

## Production Trust Boundary

The authenticated API is the trusted control plane. Its database role may
create evidence, authorization, ledger, and outbox records so approved work can
reach the writer. Database ACL separation prevents accidental cross-role DML
and keeps the writer from minting approvals, but it is not containment against
arbitrary code execution in the API: an API compromise can forge a consistent
approval set. Deploy the API behind the documented loopback/TLS boundary,
protect its key, and treat API-host compromise as authority compromise.

The first rollout is supervised and requires the PostgreSQL plus writer-artifact
backup procedure. Automated tests cover state reconciliation and a real Calibre
apply/undo round trip; they do not yet claim exhaustive SIGKILL, ENOSPC, or
filesystem-tamper fault injection across every external-write window.

## What is Read-Only?

By default, the application operates in a completely read-only mode:

- **Scan**: Reads library database to discover books.
- **Inspect**: Reads file content (PDF, EPUB) and cover images.
- **Verify** (V2 default): Hashes and inspects every attached format, optionally
  runs bounded OCR/vision/LLM recognition, and persists sealed evidence.
- **Verify** (legacy v1.0): Reads book content and runs deterministic rules plus
  the optional LLM witness.
- **Audit** (legacy v0.9): Fetches candidate metadata from external providers.

**The default Docker configuration mounts the `library` directory as read-only (`:ro`).**
**The default configuration has `BOOKAUDIT_READ_ONLY=true`.**

## What Can Write?

Write operations only occur when explicitly instructed and confirmed by
the user. The primary write actions are:

- `apply` — Modifies Calibre metadata via `calibredb set_metadata`.
- `undo` — Restores metadata from a per-book restore point.

**WebUI and API endpoints that modify data require `{"force": true}` in the
request body after explicit user confirmation.**

Affected write endpoints: `POST /api/apply/v2`,
`POST /api/undo/{change_id}`, `POST /api/runs/{run_id}/revert`.
`POST /api/apply` is retired and always returns HTTP 410.

## Manifestation V2 write boundary

V2 verification is shadow-only through the current CLI and verify API. A V2
package cannot enter the legacy apply path because it is stored as sealed
`observations` with no V1 `decision`.

A supervised V2 correction requires all of the following:

1. Tier A exact-manifestation identity with a non-empty canonical patch.
2. A valid package checksum whose run ID, book key, snapshot, evidence ID, and patch
   still match durable storage.
3. `POST /api/review/v2/{evidence_id}/authorize` with a non-empty reason. The
   authorization records the server principal and hashes the exact package and
   field-lock-filtered patch.
4. `POST /api/apply/v2` with `force=true`, the explicit evidence ID, and its
   exact authorization ID.
5. Revalidation by the sole privileged writer, including unchanged live Calibre
   values, the exact library root sealed into the package, attached-format
   membership/order, and the SHA-256 of every ebook before and after write.
   Every path component is opened relative to a descriptor-anchored root with
   no symlink following; checking only the final component is insufficient.
6. A hashed OPF backup and restore point before the metadata command. `#edition`
   changes additionally preserve the previous custom-column value; cover
   changes preserve and hash the previous cover. Artifact directories are also
   created with descriptor-relative no-follow operations. Before Calibre reads
   an OPF or cover, the verified bytes are copied into an immutable sealed
   descriptor and passed to the child process; a pathname replacement after
   validation cannot alter the consumed bytes.
7. Read-back verification. A partial write is restored; missing or inconsistent
   recovery evidence becomes an error/unknown state instead of being guessed.

Allowed V2 fields are `title`, `authors`, namespaced `identifiers`, `languages`,
`publisher`, `pubdate`, `series`, `series_index`, `edition_statement` (Calibre
`#edition`), and a local sealed `cover` artifact. Legacy aliases and unknown
fields are rejected before backup or mutation.

Tier A automatic mode is disabled unconditionally. Calibration reports remain
useful advisory measurements, but their checksum is not an authenticated
attestation and cannot unlock a writer. `bookaudit apply` and the legacy
`POST /api/apply` route are disabled in every profile.

---

## Legacy v1.0: Conservative Auto-Apply Gate

v1.0 introduced an **auto-apply gate** that decides which books are safe
to apply without human approval. A book is `auto_apply_eligible` iff:

1. **Every declared field has a deterministic verdict** (no `ambiguous` left)
2. **Overall confidence ≥ 80**
3. **No high-risk flag** present (e.g. `author_swap`, `isbn_conflict`,
   `wrong_book`, `series_mismatch`, `publisher_mismatch`)
4. **Per-field confidence ≥ 75**

The threshold constants live in `src/calibre_ai_auditor/verification/verdict.py`:

```python
AUTO_APPLY_MIN_CONFIDENCE: int = 80
AUTO_APPLY_MIN_FIELD_CONFIDENCE: int = 75
```

Tune these based on real-world calibration. See
[docs/calibration/v1.0_calibration_runbook.md](calibration/v1.0_calibration_runbook.md).

---

## v1.0: Per-Book Restore Points

Every apply creates a **restore point** at
`<artifacts_dir>/restore/<run_id>/<book_key>/` containing:

- `original.opf` — original OPF (calibredb export)
- `original.<ext>` — descriptor-anchored streamed copy of the original book
  file when that optional backup is requested
- `original.cover.<ext>` — original cover image (if cover was modified)
- `before.json` — full metadata snapshot before the apply
- `after.json` — full metadata snapshot after the apply
- `restore.json` — metadata for bulk-undo by `run_id`

**Retention target: 30 days** (configurable). The store exposes bounded cleanup,
but production does not schedule it automatically. Production cleanup requires
a checksum-verified paired PostgreSQL/artifact backup and the same advisory lock
used by the sole writer. It refuses fresh writer heartbeats, non-terminal ledger
operations, unsafe backup paths, and changed restore manifests before mutation.
Monitor artifact-disk usage and use only the approved procedure in the
[production operations runbook](runbooks/production-operations.md).

---

## v1.0: Bulk Undo by Run

The API supports queueing undo for all changes in a run at once:

```bash
# API
curl -X POST http://localhost:8080/api/runs/<run_id>/revert \
     -H "X-API-Key: $BOOKAUDIT_API_KEY" \
     -H "Content-Type: application/json" \
     -d '{"force": true}'
```

The request queues each eligible change for the sole writer. CLI undo remains
single-change only: `bookaudit undo <change_id>`.

---

## How Backups Work (v0.9 legacy, still works)

Before any write operation, the system automatically exports the current
metadata state as an OPF file via `calibredb export_metadata` and saves it
to `.artifacts/backups/<book_id>/`. The v1.0 restore point system supersedes
this with a richer per-book snapshot.

## How Undo Works (legacy)

The legacy `undo` command reverses an applied change by using the backup OPF
file saved prior to the `apply` operation. It restores the exact state of
the book's metadata before the change.

---

## Safe Testing Practices

1. **Never test `apply` on your primary, real Calibre library first.**
2. Use a disposable or isolated test library when testing write capabilities.
3. Keep the Docker volume mount for the library as `ro` unless you explicitly
   intend to test writing.
4. Ensure `.state` and `.artifacts` directories are writable to store logs,
   evidence packages, restore points, and backups safely.
5. **Calibrate first** — follow the calibration runbook before trusting
   auto-apply on real books.
6. **Back up your restore points** — `tar czf restore_backup.tar.gz .artifacts/restore/`
   gives you a single-file archive of all restore points.
7. **Trust the gate** — `auto_apply_eligible: false` is the engine telling
   you "don't trust this fix without looking". Read the per-field verdicts
   and the proposed patch before approving.

# Production readiness

## Current decision

**Certificate A (shadow production)** is the active operational bar for this
workstation stack: compose is up with pilot and auto-apply **disabled**,
readiness green, backup+restore drilled, MCP read tools exercised against the
deployed database, and Gitea gates green for documented SHAs.

The Manifestation V2 supervised-pilot development head is **not approved for
writes to a live Calibre library**. It is read-only/shadow by default. Legacy
direct apply and unattended V2 apply are disabled in every profile. Certificate
B (live canaries) remains out of scope until a reviewed corpus and clone apply
drill are complete.

The earlier `v1.2.1` operational assessment does not automatically approve live
writes. Write promotion is still per exact commit and immutable image digest.

## Baseline exact-commit gate evidence

On 2026-07-15, canonical Gitea run 24 (run ID `1737`) completed successfully for
commit `4a0d6d2326c0ef642fcdcc68e693a1f72632aa1f` on
`feat/v1-content-verification`.

- `Backend (pytest + mypy + ruff)` completed successfully after destructive
  PostgreSQL suites were isolated from later migrated-schema tests.
- `Manifestation V2 required integration` reported exactly one pass and no
  skips for the real Calibre/Tesseract/PostgreSQL/Valkey API → authorization →
  writer → readback → undo round trip.
- Benchmarks, WebUI E2E, and the production image contract also completed; the
  run conclusion is `success` for the exact SHA above.
- The run was triggered by the pushed commit. No Gitea Action was manually
  rerun.

This supersedes the failed run `1698` and clears the repository CI blocker for
that SHA only. It does not approve a live library: immutable image review,
operator-owned backup and restore evidence, a restored-clone rehearsal, and
explicitly reviewed canaries remain outstanding promotion gates.

Every later commit still requires its own automatically triggered Gitea run;
this baseline must not be presented as evidence for a different SHA.

### Development head after main unification (local only)

On 2026-07-17, `main` (`92b88ee`, v1.2.1 release/retention recovery) was merged
into `feat/v1-content-verification` as commit `3eb4830`. The merge tree was
unchanged relative to the pre-merge feature tip: the V2 head already contained
the release content; the merge only made `main` a git ancestor.

Local evidence for exact SHA `3eb4830511ca736afe7b9038b9aeb31a947c4471`:

- `./scripts/verify-calibre-gate.sh` passed (ruff, format, mypy, hermetic
  pytest: 442 passed, 7 skipped for missing optional/live deps, 44 deselected).
- `uv run python scripts/disposable_calibre_lab.py run` passed
  (`run_id=bookaudit-lab-c061dc89ec39`, Calibre 9.11.0, ACL write rejected,
  library identical, cleanup completed).

### Gitea exact-commit evidence (Certificate A HEAD)

| SHA | Run | Result |
|---|---|---|
| **`e2528df7dbac76df0b83ce8526b9a42f82e3327a`** (Certificate A) | run **31** / ID **`1799`** | **success** (all required jobs) |
| `c497ad64b1c57ace9e767d7d11f95256be75b12c` | run 27 / ID `1785` | success (prior baseline) |
| `ee9e5016b3e2573947e4e622d142f6578970a4d3` | run 28 / ID `1786` | success (prior baseline) |

Run 31 was triggered by automatic push of the MCP packaging fix. No Gitea
Action was manually rerun. Jobs green: Backend, Manifestation V2 required
integration (**1 passed**, 0 skipped — log asserts `1 passed` and rejects
`SKIPPED`), Benchmarks, WebUI E2E, Production image contract.

Treat **`e2528df` / run 1799** as the Certificate A CI baseline for this goal.

### Local shadow stack evidence (workstation, pilot off)

On 2026-07-17, against fixture library content under
`BOOKAUDIT_LIBRARY_HOST_PATH` (copied from `fake_library/`), with
`BOOKAUDIT_ALLOW_LOCAL_IMAGE=true` and image tag
`calibre-ai-auditor:1.2.1-shadow`:

- `./scripts/prepare-production.sh` passed (mode-0600 `.env`).
- Alembic migrated to head `a72c9d4e8f31`.
- Compose services `postgres`, `valkey`, `writer`, `app` healthy;
  authenticated `GET /api/health/ready` returned `status=ready` with fresh
  writer heartbeat and `pilot_binding=null`.
- Port bound to loopback only: `127.0.0.1:18080`.
- Supervised pilot remained disabled; auto-apply disabled.
- Paired backup drill produced
  `backups/20260717T070821Z/{bookaudit.dump,writer-artifacts.tar.gz,manifest.json}`
  with SHA-256 entries for both halves; stack returned to `ready` after restart.
- V2 shadow verify via API (`limit=2`, no OCR/LLM/vision) completed as run
  `verify_20260717_071101_7008a904` with counts `{"review": 2}` (Tier B for
  both fixture books).

**Local-only note:** the stock production Compose file mounts the library
read-only on `app`. Calibre 9.11 still attempts a case-sensitivity write probe
during `calibredb list`, which fails on that RO mount. The local smoke used
uncommitted `compose.shadow-local.yml` to remount the **fixture** library RW on
`app` only. Live libraries must continue to use Content Server (ADR-004) or a
restored clone via the documented writer boundary — do not treat the local
override as production policy.

This workstation stack is **shadow operational evidence**, not a Gitea release
digest, not a live-library inventory, and not write-pilot approval.

### Certificate A completion evidence (2026-07-17)

| Gate | Evidence |
|---|---|
| Gitea green (automatic) | **Run 31** (`1799`, SHA `e2528df`): Backend, V2 pilot (**1 passed** / 0 skip), WebUI E2E, Production image — all success. Prior baselines: runs 27–28. |
| Stack ready | `GET /api/health/ready` → `status=ready`, schema `a72c9d4e8f31`, writer `fresh=true`, `pilot_binding=null`; loopback `127.0.0.1:18080`. |
| Pilot / auto-apply | Env: both `ENABLED=false`; `BOOKAUDIT_READ_ONLY=true`. |
| Backup + restore | Paired dump+artifacts+manifest under `backups/20260717T075240Z-certA/` (DB sha256 `2a991d0d…`, artifacts sha256 `e250f6e1…`). Clean volume wipe, `pg_restore` exit 0, alembic head restored to `a72c9d4e8f31`, post-restore ready with writer fresh. |
| MCP read path | Production image ships `[mcp]` (fastmcp). In-container tools: `list_recent_runs`, `get_run_metrics`, `query_book_audit` returned non-error structured data for live run `run_ingest_20260717_071018`. Hermes VM `192.168.0.179` was unreachable (no route); local container MCP is the Certificate A evidence. |
| Content Server inventory | **Not run** this session (operator tunnel/credentials not available). Does not block Certificate A when other gates hold. Use ADR-004 path when ready. |

MCP packaging change: the production Docker image now installs the `mcp` extra
so `bookaudit mcp` works without a second install. Tool callables remain
importable without FastMCP for tests and programmatic smoke.

## Local Content Server mechanism evidence

On 2026-07-16, the local disposable gate passed with checksum-pinned Calibre
9.11.0 and five generated CC0 records. It observed five formats and five
shadow-review results, rejected the restricted account's write probe, found the
same regular-file paths, sizes, and SHA-256 values before and after inspection,
left scratch empty, and removed every run-scoped Docker resource.

Reconfirmed on 2026-07-17 for SHA `3eb4830` (report
`reports/disposable-calibre/bookaudit-lab-c061dc89ec39.json`).

This is local mechanism evidence only. It does not replace the automatically
triggered Gitea run for the pushed exact commit, authorize a connection to the
live server, establish the target account's ACLs, or approve any writer action.
See the [disposable lab runbook](runbooks/disposable-calibre-lab.md).

## Required promotion evidence

All of these gates are mandatory for the same commit and release digest:

1. Ruff, format, mypy, the complete hermetic backend suite, WebUI lint/build,
   and Playwright pass.
2. Gitea job `Manifestation V2 required integration` passes with real Calibre,
   Tesseract, PostgreSQL and Valkey. Its API-authorize, unit-queue, writer
   apply/readback, queued undo and restored-readback test must report exactly one
   pass and no skip.
3. The Gitea production-image contract and vulnerability gates pass. Do not
   manually rerun Actions to manufacture a green result.
4. `scripts/prepare-production.sh` accepts the operator-owned mode-0600 `.env`
   and proves that the configured pilot digest exactly matches the immutable
   `BOOKAUDIT_IMAGE` reference.
5. A disposable library apply/readback/undo drill succeeds with the exact image.
6. A restored clone of the real library completes shadow audit and a supervised
   apply/readback/undo rehearsal with no unexplained evidence or recovery state.
7. Only after explicit approval, at most five serial live canaries are manually
   reviewed one by one. Any stop condition closes the pilot immediately.

Repository tests cannot prove token revocation, host access control, backup
recoverability, a real-library clone rehearsal, or human review quality. Record
those operator-owned facts separately; never turn their absence into a passing
checkbox.

## Safety boundaries

- Only the dedicated writer receives a read-write Calibre library mount. The
  app can authorize and enqueue but cannot write library files.
- A persisted pilot row binds its exact ID to a canonical library-root SHA-256,
  immutable release digest, Alembic revision and budget of one to five reserved
  operations. Reusing the ID with different binding data is rejected.
- Queueing reserves exactly one operation under a fixed PostgreSQL transaction
  advisory lock plus the exact pilot row lock, including when simultaneous
  requests present different pilot IDs. Any nonterminal operation or
  unacknowledged unpublished outbox event blocks the next request. The
  reservation counter must equal the number of pilot-bound operations at both
  API and writer boundaries; reservations are never reset or refunded.
- A fresh writer heartbeat must match the pilot runtime before queueing; the
  authenticated readiness endpoint and writer both validate the complete pilot
  ID, max-operations, release, schema and canonical-root binding.
- Tier A still requires manual authorization of one exact sealed package. Tier
  B/C, title-only lookup, LLM confidence, vision alone and a single OCR engine
  cannot authorize a write.
- Content Server packages are always shadow-only. The remote adapter uses a
  loopback tunnel, a capability-limited command set, private ephemeral scratch,
  source-change detection, and logical format references; the apply coordinator
  rejects these packages regardless of review state.
- Writer exclusivity uses a PostgreSQL session advisory lock. A privileged host
  process that directly changes the bind mount is outside this trust boundary.
- Every apply preserves hashed OPF/custom-column/cover recovery evidence and
  performs readback. Unknown or failed recovery is a stop condition.
- `bookaudit pilot-stop PILOT_ID --yes` closes the persisted session. Stop app
  intake and the writer first because closing a row cannot interrupt a Calibre
  subprocess that has already started.
- A historical terminal V2 `failed` outbox can be bypassed only by the separate
  append-only `bookaudit incident-ack` workflow after live Calibre and recovery
  evidence are reconciled and the exact failed pilot is stopped under lock. It
  is unavailable for `unknown` and `restore_failed`, preserves all original
  rows, does not refund budget, and never permits reuse of the stopped ID.
- Retention remains a separate guarded maintenance workflow and requires the
  paired PostgreSQL/artifact backup manifest described in the operations
  runbook.

## Operator checklist

1. Revoke superseded Gitea and registry tokens; rotate deployment secrets.
2. Pin `BOOKAUDIT_IMAGE` to the exact reviewed digest and set the same digest in
   `BOOKAUDIT_MANIFESTATION_V2__SUPERVISED_PILOT__RELEASE_DIGEST`.
3. Keep `BOOKAUDIT_MANIFESTATION_V2__SUPERVISED_PILOT__ENABLED=false` through
   shadow audit, backup, restore drill and clone rehearsal.
4. Run `scripts/prepare-production.sh` and resolve every validation error.
5. Apply Alembic head `a72c9d4e8f31` explicitly; runtime services never migrate.
6. Load alerts and require authenticated readiness plus the exact writer
   binding metric.
7. Follow the serial start/stop procedure in the supervised rollout runbook.

See [DEPLOYMENT.md](../DEPLOYMENT.md), the
[production operations runbook](runbooks/production-operations.md), and the
[Manifestation V2 rollout runbook](calibration/manifestation-v2-runbook.md).

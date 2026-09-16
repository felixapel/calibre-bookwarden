# Bookwarden remediation plan

Status: implementation and local verification in progress. This document is not release approval.

## Product and release boundaries

Bookwarden is a supervised, content-grounded metadata auditor. Certificate A remains read-only. OCR, external metadata and visual scores are evidence, never write authority. The only supported future production mutation path is exact authorization through the existing Manifestation V2 coordinator and serialized writer. There is no promise of zero unknown vulnerabilities.

This work authorizes local source changes, generated fixtures and local verification. It does not authorize publication, a deployment, a live-library mutation, a service restart or enabling a production writer. Never use a real library to test writes.

## Recovery and baseline

The starting checkout was HEAD 20de0f2 with four tracked modifications and ten untracked implementation/test files. Before edits, 418 Git-listed and nonignored untracked files were archived with per-file SHA-256 values. The archive was extracted into a separate scratch tree and all 418 restored hashes were checked. This preserves pre-existing Antigravity work; do not reset the checkout or remove that work.

Local, ignored evidence lives under scratch/remediation-20260916/. The initial Windows backend run reported 63 failures, 574 passes, 13 skips and 46 deselections. These results are a baseline, not a passing gate. Failures include a real compound-surname defect plus Linux shell, POSIX permission, symlink privilege, descriptor-path and fixture-path assumptions. Production safety assertions must not be weakened to make Windows green.

## Acceptance matrix

| Stage | Required result | Verification |
| --- | --- | --- |
| 1. Inventory and recovery | Preserve dirty/untracked source; identify every mutation entrypoint and deployed app | Verified archive restoration; CLI/API/MCP/helper inventory |
| 2. Read-only containment | Direct SQLite mutations, destructive legacy commands and in-place cover endpoints cannot bypass the writer | No-side-effect rejection tests for both read_only settings; source hashes unchanged |
| 3. Backup correctness | SQLite snapshot includes committed WAL state and fails closed on error; clearly DB-only, never advertised as whole-library recovery | WAL fixture, reopened snapshot integrity/data checks, invalid destination and failure cleanup |
| 4. One authority | No prototype AUTO_APPLY, alternate rollback/schema creation or JSON lock as writer authority | Hostile candidate, retired-helper and existing coordinator/writer regressions |
| 5. Analysis correctness | Only complete cryptographic matches can be clones; safe EPUB/SVG extraction; bounded input; cautious names and optional model contracts | Generated EPUB2/3, corrupt archives, conflicting editions, invalid ONNX output and compound-author fixtures |
| 6. Honest UI | Unsupported apply fails visibly; no fake candidates or success; review progress does not cycle | API rejection, pagination, CSP-compatible page, desktop/mobile contract tests |
| 7. Security and performance | Existing provider/auth/path controls remain; dependencies audited; measured scope documented | Adversarial tests, dependency scans, bounded streaming tests and scoped measurements |
| 8. Release | Reproducible clean committed revision, required Linux/service/image gates and restorable library backup | Canonical Gitea evidence for exact revision; separately authorized deployment and canary |

## Implementation decisions

- Retire unsafe direct mutation entrypoints rather than introduce a second writer or an opt-in bypass.
- A SQLite database snapshot is not a backup of ebook files, covers or OPFs. Whole-library recovery requires a stopped/coherent source and an independently verified file-tree backup.
- Preserve the existing identity resolver, exact user authorization, sealed artifacts, writer fencing, expected-state comparisons and post-write/readback requirements.
- The experimental ledger and cooperative JSON lock must not become runtime dependencies. The former creates schema at runtime and accepts tampered backups; the latter cannot exclude external writers.
- Candidate ranking cannot promote itself to identity Tier A. A valid but conflicting ISBN is a conflict, not a lower score.
- Matching title/author or complementary formats permits review only. A connected-component cluster is not proof that every member contains identical bytes.
- Unknown authors, minimalist covers and model failures must not be converted into confident corruption findings. Preserve original metadata and report uncertainty.
- Keep the production SPA read-only. Do not add a cover writer until its patch/evidence contract, migration needs, crash recovery and real-service tests are designed and pass.
- Do not disable secure-path, seal or authorization assertions to accommodate platform-specific test infrastructure.

## Security review coverage

Inventory includes CLI, ordinary web app, Certificate A app, MCP, background tasks and low-level helpers. Check read-only enforcement at the mutation boundary, not only at routes. Verify OS-level read-only mounts and restricted runtime roles in the deployment gate.

Threat tests cover traversal, symlinks/junctions/reparse points, unsafe stored library paths, archive member/count/size/ratio limits, malformed XML/images, external-provider redirects and non-public peers, authentication/authorization, duplicate requests and changed evidence. Temporary files must be bounded and cleaned up; logs must not expose credentials. Existing production outbound adapters already reject redirects, require allowlisted HTTPS hosts and check connected peers; preserve these controls.

Writer validation must cover durable intent, interruption before/after writes, unknown outbox state, stale fencing, concurrent claims and recovery. PostgreSQL locks coordinate Bookwarden, not unrelated Calibre Desktop/Web/maintenance writers. A live writer window requires external-writer exclusion and explicit authorization.

## Required checks

Run targeted regressions before the repository gates:

```sh
uv run ruff check .
uv run ruff format --check .
uv run mypy src
uv run pytest -m 'not benchmark and not ocr_live and not network' --ignore=tests/test_retention_postgres_valkey.py --ignore=tests/test_v2_supervised_pilot_integration.py
```

Frontend: locked npm install, lint, build, dependency audit and Playwright desktop/mobile tests. Browser-contract tests use mocked APIs and do not prove real Calibre integration.

Canonical Gitea jobs are backend, real-services, webui, container and benchmarks. Backend/service checks include PostgreSQL/Valkey roles, migrations, writer recovery and real Calibre/OCR boundaries. Container checks include privilege/mount/Compose contracts and image vulnerability scans. Required skipped tests do not count as passes. Content Server or disposable lab changes additionally require the lab described in docs/runbooks/disposable-calibre-lab.md.

Run the production Linux checks on Linux. No WSL distribution or Docker executable was available on the inspected Windows host. Do not install a platform runtime or alter infrastructure as an implicit workaround.

## Delivery sequence and rollback

1. Stabilize and verify the read-only application.
2. Validate supervised mutations and complete-library restoration on disposable fixtures.
3. After exact-revision CI and separate authorization, deploy a digest-pinned build.
4. After a restorable backup and separate exact-operation authorization, run a bounded live canary and inspect readback/rollback evidence.

For local source recovery, recover individual files from the verified pre-change archive after checking for newer user edits. Never overwrite the whole checkout blindly. Keep each implementation/review scope bounded; no concurrent writes to the same files, no review before the scope's editing stops, and no unsupported performance or readiness claims.

## Remaining release evidence

Track final local counts and unresolved checks in REMEDIATION_RESULTS.md. Passing dependency audits or browser mocks does not establish production readiness. A clean commit, canonical remote ref and corresponding automatically triggered Gitea gates are required before a release can be claimed. No manual CI rerun is authorized here.

SQLite paths are opened with an escaped URI and `mode=ro`. UNC shares require a SQLite build supporting `SQLITE_ALLOW_URI_AUTHORITY`; unsupported builds fail closed without a writable fallback. No live UNC share was verified. See the [SQLite URI documentation](https://www.sqlite.org/uri.html).

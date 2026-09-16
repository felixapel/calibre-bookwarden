# Local remediation results — 2026-09-16

Status: local remediation implemented; release validation remains incomplete. This document does not certify a production release.

## Implemented scope

- Direct SQLite access is enforced read-only by SQLite, including after disabling `query_only`. UNC errors cannot trigger a writable fallback.
- Database snapshots use SQLite backup, include committed WAL data, verify integrity after reopening, and remain outside the library. They are database-only.
- Four legacy mutation commands, direct API writes, prototype ledger/schema/rollback, JSON writer lock and in-place optimizer/bridge mutations reject explicitly. The existing supervised V2 writer remains the only correction architecture.
- Duplicate classification requires complete matching SHA-256 maps throughout each cluster; uncertain editions remain review candidates. Provider consensus cannot auto-apply or grant Tier A.
- Archive and optional vision analysis handle uncertainty explicitly; SVG cover extraction and compound-author sorting have targeted regression coverage.
- Development CoverDeck uses server-rendered read-only review with a monotonic cursor, safe current-image reads, no invented cover candidates and no simulated apply/undo controls.
- V2 fixtures normalize valid host paths before sealing; malicious-path fixtures and production assertions retain their original protections. Calibration reports use the existing rooted atomic replacement helper.
- Public documentation now distinguishes experimental diagnostics, the production read-only profile, and the supervised writer gates.

## Observed local validation

Initial Windows baseline: 63 failed, 574 passed, 13 skipped, 46 deselected. The final integration run reported **649 passed, 36 failed, 13 skipped and 46 deselected** (43.04 seconds). The final corrupt-cover redaction/cleanup regression is included in that run; its focused API/UI suite also passed all 17 tests. Independent read-only review closed its reported archive, author, TOCTOU and error-disclosure findings with no further findings in the reviewed changes. No additional skips were introduced to hide failing checks.

The 36 remaining failures are: 15 Linux shell execution failures (`WinError 193`), 9 symlink-privilege failures (`WinError 1314`), 10 POSIX private-permission mismatches (9 environment checks plus inventory output), 1 `/proc/self/fd` expectation and 1 POSIX OCR process-group expectation. They require the supported Linux validation environment; they do not establish that those security gates pass there.

Observed checks:

| Check | Observed result | Scope/limit |
| --- | --- | --- |
| Backend Ruff | Passed | Full repository |
| Backend format | Passed | 283 files; 17 formatting fixes verified AST-identical |
| Backend mypy | Passed | 155 source files |
| Frontend locked install | Passed, 182 packages | `npm ci --ignore-scripts --no-audit --no-fund` |
| Frontend lint/build | Passed | Production SPA unchanged by remediation |
| Playwright Chromium and mobile Chromium | 17 passed | API contract mocks; not real Calibre integration |
| npm dependency audit | 0 known vulnerabilities | Registry audit at execution time |
| Python production dependency audit | No known vulnerabilities | Frozen export, no development dependencies; pip-audit 2.10.1 |
| Recovery archive | 418 restored hashes verified | Source recovery only |
| Local documentation links | Passed | Changed documentation, relative targets |

A repeated synthetic metadata benchmark using the existing verification engine processed 50,000 inputs in 7.176 seconds (6,967.21/s), with a 43.42 MiB peak process working set on Windows/Python 3.13.5. It excludes filesystem scanning, OCR, network and persistence. It is neither a measured full-library throughput nor the canonical Linux benchmark gate; it does not justify further optimization or a production performance promise.

Logs are local and ignored under `scratch/remediation-20260916/`: `baseline-pytest.log`, `playwright.log`, `npm-audit.json`, `pip-audit.json`, `benchmark-local.json`, plus final integration logs. `final-ruff.log`, `final-format.log` and `final-mypy.log` record passing checks on the final code. `final-static-verification.txt` additionally records the formatting-only AST comparison. Dependency audits cover known advisories, not all application vulnerabilities.

## Recovery and scope

The pre-change working tree included modified and untracked user work. It was preserved in `scratch/remediation-20260916/pre-change.zip` with a SHA-256 manifest. All 418 archived files were extracted into `restore-verification` and their hashes verified. This protects the original source state, not a real Calibre library.

During the local remediation, no real library mutation, production deployment, restart, push, commit or manual CI rerun was performed. The user subsequently authorized committing and pushing the remediation to Gitea for automatic runner validation; that follow-up uses `codex/bookwarden-remediation`, without updating `main` or deploying production. `final-source-manifest.json` records the final local source hashes, including preserved pre-existing untracked work. It is not a committed release identity. Recover individual source files from the archive only after checking for subsequent user edits; do not reset the entire checkout.

## Platform limitation

On Windows, the legacy DirectEngine file/cover inspection and CoverDeck image reads are unavailable (`UNSUPPORTED_SECURE_FILE_READ` / unavailable image). They fail closed because the current secure-file implementation cannot guarantee descriptor-anchored containment there. SQLite metadata-only inspection remains read-only. Run file/cover inspection in the supported Linux environment; this remediation does not claim Windows feature parity.

## Release evidence still required

1. Run required Linux backend and security tests without weakening POSIX, symlink, permission or process-group assertions. This host has no installed WSL distribution or Docker executable.
2. Run PostgreSQL/Valkey migrations, roles, fencing, writer crash/recovery and actual Calibre/OCR tests in the disposable environment.
3. Validate the final committed revision through canonical Gitea backend, real-services, webui, container and benchmark jobs. Check image vulnerabilities and deployment mounts/privileges.
4. Calibrate analysis against a representative human-reviewed book corpus. Synthetic fixtures do not establish real-world accuracy; optional ONNX needs a matching documented model contract.
5. Perform and verify a whole-library restore on a disposable copy. A database snapshot cannot restore missing book files or covers.
6. Only after these gates and separate exact-action authorization: deploy a pinned release and run a bounded supervised canary with external writers excluded.

UNC shares require SQLite URI-authority support; unsupported builds fail closed. No live share was tested. Reference: [SQLite URI filenames](https://www.sqlite.org/uri.html).

## Gitea follow-up

The existing `certificate-a-production-gates` workflow now accepts pushes to the exact remediation branch. Its backend, real-services, webui, container and benchmark jobs provide Linux evidence for the pushed revision. Results must be verified against that revision before claiming success. Local Windows limitations are not evidence that Linux tests passed. Whole-library restoration and any live write pilot remain separate gates.

### First Linux runner result

Gitea run #100 (run ID 5671), commit `5dbfdfcf63e086b38f3d6035711a0e193dce9318`, completed with failure: **689 passed, 2 failed, 7 skipped, 46 deselected** in the hermetic backend suite. The two failures were test portability/contract defects: a Windows UNC fixture was represented as a Linux `Path`, and the workflow contract still counted four locked commands after four required writer suites were added. Ruff, formatting and types passed before pytest. Later backend steps and dependent jobs did not run; no real-service success is inferred.

The follow-up uses `PureWindowsPath` for the simulated UNC URI and verifies all six exact required pytest commands, their pipeline exit checks and the no-skip guard. The URI remains read-only with exactly one SQLite connection attempt. Targeted local validation: 9 passed, Ruff/format/diff checks passed. A new commit must receive its own automatic Gitea result; the failed run is not manually rerun.

### Second Linux runner result

Gitea run #101 (run ID 5673), commit `7c91e713f27976a3048e3f196e30340d53c07a4c`, completed with overall failure, with substantial gates now passing:

| Gate | Observed result |
| --- | --- |
| Backend | Passed: 691 hermetic tests, 7 service-dependent skips, 46 deselected; locked style/types, migrations, PostgreSQL/Valkey tests, dependency audit and scoped metadata benchmark passed |
| WebUI | Passed: build/lint/audit and 17 desktop/mobile browser contracts |
| Benchmarks | Passed: 30 benchmark tests; 707 non-benchmark tests intentionally skipped by benchmark-only execution, 10 deselected |
| Real services | Verifier claim/fencing 1 passed, real Calibre/OCR 2 passed, PostgreSQL writer 2 passed, crash recovery 1 passed, V2 coordinator 20 passed; final supervised-pilot test failed at collection |
| Container/image/security | Skipped because real-services failed; image scans and runtime/Compose checks are not yet validated for this branch |

The remaining observed failure is `ModuleNotFoundError: openai`: the supervised-pilot test imports the generic development web application, whereas the preceding Certificate A suites deliberately install only the minimal production closure. It is a CI dependency-profile mismatch; this run did not execute the final API-to-writer/readback/undo round trip. The correction must use the existing locked `legacy` extra in a separate pilot environment and preserve the minimal Certificate A environment and all authorization/middleware checks.

At this review, the remote remediation branch matches the reviewed commit, `main` remains `20de0f2c599891d76c7313a3c8d7e6c81fff8d38`, and no deployment or live-library write has occurred. Remaining release work is still the completed supervised round trip, container/image gates, verified whole-library restoration, and a separately authorized release/canary. Passing benchmarks only characterize their tested workloads, not full-library accuracy or throughput.

The follow-up now places the pilot in its own locked legacy-extra virtual environment. Local isolated-environment collection selected the live test successfully (1 selected, 1 deselected); the workflow contract, YAML parsing and Ruff checks passed. Independent review confirmed profile isolation and unchanged authorization and no-skip checks. This proves the import issue is resolved in the tested profile, not that the live round trip has passed.

### Disposable pilot passed; explicit EPUB invariant follow-up

Run #102 (ID 5675), commit `47720c70ffbc8f4b8270638b850b5bfb7c35c461`: `real-services` succeeded. Its six suites reported 1 + 2 + 2 + 1 + 20 + 1 passes, with no skipped required tests. The supervised API-to-writer/readback/undo round trip passed in 8.55 seconds (one unrelated test deselected by the live marker). Backend and WebUI also passed; benchmarks and container checks were still pending at this observation.

The pilot uses real disposable PostgreSQL, Valkey, Calibre and Tesseract, production identity/seal/authorization/writer code, and the real FastAPI middleware. External catalog evidence, release digest and heartbeat are test fixtures; processing is invoked in-process. It does not validate a deployed writer daemon, TLS edge, external-writer exclusion, a real corpus/provider lookup or a whole-library restore. Metadata undo restores the tested title/OPF, not an entire library.

A narrow follow-up adds explicit one-format and SHA-256 equality checks after both apply and undo, resolving the current Calibre-reported file path after each operation so title-driven renames are handled. It changes only assertions, not application code. Ruff, formatting, collection and independent review passed locally; its strengthened live acceptance must pass on the next exact revision before being claimed.

### Run 103: pilot integrity approved; container vulnerability gate blocked

Run #103 (ID 5676), commit `0e2f29241946e7e2c559a0d0a286cfa22bb8038d`, passed backend, all required real-service suites (27 tests, no required skips), WebUI and benchmarks. The live supervised pilot passed its explicit unchanged-EPUB checks after apply and undo in 9.04 seconds. This closes the disposable-pilot acceptance described above.

The container job progressed beyond the previous PostgreSQL failure: image builds, runtime boundaries, Compose persistence, monitoring and Caddy validation passed. The previously suggested fresh-volume ownership explanation remains unproven and must not justify weakening PostgreSQL security. The current blocker is Trivy's HIGH `CVE-2026-84445` in `google.golang.org/grpc v1.82.1` inside the Caddy binary. Application and writer image scans reported zero HIGH/CRITICAL findings under their configured policy; Caddy's scan failed before the Prometheus scan ran.

The official [GO-2026-6443 advisory](https://pkg.go.dev/vuln/GO-2026-6443) identifies fixed versions 1.82.2 and 1.83.2. The targeted update uses 1.83.2 because [GO-2026-6441](https://pkg.go.dev/vuln/GO-2026-6441) and [GO-2026-6348](https://pkg.go.dev/vuln/GO-2026-6348) also require at least 1.83.1. A passing scan must not be described as zero vulnerabilities without acknowledging the existing scoped ignore files and `--ignore-unfixed` policy. Existing exceptions are reviewed separately; no new exception is being used to hide this failure.

CI also needs a validated run-specific Compose project and visible failure diagnostics/cleanup. The changes remain limited to generated disposable CI resources; production Compose privileges and the live library remain unchanged.

### Reviewed image remediation and explicit release hold

The Caddy builder is pinned to Go 1.26.8 by the verified official OCI index digest;
its locked module graph uses gRPC 1.83.2 and x/crypto 0.55.0. The Caddy ignore file
now contains no active exceptions. Local Go checksum verification and Linux amd64
CGO-disabled readonly-module compilation passed. Image-level acceptance still
requires the automatically triggered exact-commit Gitea scan.

The Compose persistence smoke now derives its project from the numeric run ID
and binds cleanup to an immutable expected project. Failure diagnostics precede
cleanup, the original failure status is preserved, and cleanup errors are visible.
Eleven no-daemon tests cover invalid IDs, ambient project replacement, failure
status propagation and rejection of a different valid run project.

The official [Prometheus LTS 3.13.3](https://prometheus.io/download/) image is pinned
to OCI index `sha256:6976aa8a60fec930796ce5772b8d12da7a318a5daa8d40d69c5c7819a05eeed7`.
Both Linux amd64 binaries were extracted from digest-verified registry layers and
inspected without execution using `go version -m`: Go 1.26.8, x/crypto 0.55.0,
x/net 0.57.0, but **gRPC 1.82.1 remains**. All monitoring vulnerability exceptions
were removed except the documented Prometheus pseudo-version false positive
CVE-2026-42154. Fixable HIGH/CRITICAL embedded dependency findings not listed in the scoped
ignore file must fail CI.

The user explicitly chose to retain the official LTS image and block release
until an official stable version corrects the gRPC findings. Do not substitute a
release candidate, custom rebuild, vulnerability suppression, deployment or merge
as a workaround. The next CI can establish Caddy and functional results while the
Prometheus security gate remains an expected release blocker. The whole-library
restore, production canary and other earlier release limits remain outstanding.

Local final checks: Ruff check/format and mypy passed; Bash syntax passed.
The combined deployment/helper test invocation reported 35 passed and 15 failures
because Windows cannot directly execute the existing POSIX `.sh` wrappers
(`WinError 193`). Those tests remain enabled unchanged in Linux CI; this is not a
local full-suite pass. The 11 new helper tests passed using Git Bash and simulated
Docker calls. Independent review found and verified the exact-run cleanup fix.

# Repository rules

## Product
Bookwarden checks every book in a Calibre library against the files and national-library records, proposes per-field fixes, and applies only the fixes the admin approves, through Calibre's content server, with readback and undo. An optional three-model agent panel (ADR-029) judges proposals in shadow; its verdicts are never applied. The design is `docs/superpowers/specs/2026-10-12-bookwarden-go-rewrite-design.md`, amended by `docs/superpowers/specs/2026-10-29-bookwarden-v2-design.md` (each v2 rule change applies only once its ADR has landed) and by `docs/superpowers/specs/2026-11-04-bookwarden-v1-production-design.md`, the v1.0 production design. Target values for every field follow the metadata style guide, ADR-031. Don't turn it into a general library manager, an unattended writer or an LLM product without an approved spec change.

## Safety
- Reach the library only through Calibre's content server. Never mount or open `metadata.db`.
- Never test writes against a real library. Use `test/calibre`, which is synthetic and disposable.
- Query sources by exact, checksum-valid ISBN only. Fail closed and never fabricate metadata.
- OCR, Calibre's current values and agreement between aggregators alone are never authority (spec section 7).
- Never weaken a seal, a tier rule, readback, the stale check or undo to make a test pass.
- No secrets, private book data or real library contents in git, logs or fixtures.
- Agent verdicts are a second opinion shown after a decision and never a source of values (ADR-029). Tests never call a real model API; only `-tags live` does.

## Go
- Go comes from `mise.toml`: run `mise install` first, and don't use a global Go.
- `gofmt -l .` must print nothing and `golangci-lint run ./...` must be clean.
- Write table-driven tests. Wrap errors as `fmt.Errorf("context: %w", err)`.
- Postgres tests need `BOOKWARDEN_TEST_DATABASE_URL` set to a server where the tests may create databases:

```bash
docker run -d --name bw-pg -e POSTGRES_PASSWORD=dev -p 127.0.0.1:55499:5432 postgres:18-alpine
export BOOKWARDEN_TEST_DATABASE_URL='postgres://postgres:dev@127.0.0.1:55499/postgres?sslmode=disable'
```

## Before every commit
```bash
gofmt -l .
golangci-lint run ./...
go test -race ./...
```
After image changes, also run:
```bash
docker build -t bookwarden:dev . && scripts/check-image-size.sh bookwarden:dev 100 && scripts/smoke-image.sh bookwarden:dev
```
After changes in `internal/library` or `internal/verify`, also run the read-only proof against a real Calibre:
```bash
scripts/integration-test.sh
```

## Delivery
- **Remote:** Gitea (`felix/calibre-bookwarden`) is the canonical remote, and Gitea Actions the canonical CI. Don't touch GitHub unless Felix asks; the GitHub mirror is updated by hand.
- **Branches:** the cutover merged `go-rewrite` into `main` on 2026-10-26 (spec section 17); `main` is the trunk, protected by the 6 CI jobs, and Certificate A's last commit is tagged `certificate-a-final`.
- **Pull requests:** work on a branch off `main`, and open a PR into `main`. Wait for green CI and a `reviewer` agent pass; Felix merges. Nothing is pushed straight to `main`.
- **CI failures:** never rerun Actions to get past a failure. Push a fix instead.
- **Commits and docs:** use Conventional Commits, and keep the README, spec and ADRs true to the code. Record lasting decisions as ADRs in `docs/decisions/`; the next number is ADR-049 (ADR-040 is reserved for work-level search).

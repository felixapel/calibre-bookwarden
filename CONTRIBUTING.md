# Contributing

## Ground rules

- Start with an issue for large changes
- Keep pull requests small and reviewable
- Any write-path change must include tests and docs
- No secrets, personal libraries, or copyrighted books in the repo

## Forge workflow

Calibre Bookwarden has a canonical development forge and a public mirror; it
does not maintain an automatic dual-forge synchronization:

1. **Gitea (Internal Primary & Hermetic CI)**:
   - URL: `http://192.168.0.122:3010/felix/calibre-bookwarden`
   - Hosts full-stack hermetic CI runners (PostgreSQL, Valkey, Tesseract, Calibre lab).
   - Core release gates and vulnerability scans run here.

2. **GitHub (Public mirror & Community)**:
   - URL: `https://github.com/felixapel/calibre-bookwarden`
   - Public distribution, issues, and external contributor PRs.
   - Does not automatically mirror commits, releases, or tags. GitHub workflow
     results do not replace canonical Gitea release evidence.

### Contribution Workflow

1. Fork the repository in the forge where the contribution will be reviewed.
2. Create a feature branch: `git checkout -b feat/my-improvement`.
3. Run formatting, linting, and local tests.
4. Open a Pull Request in that forge with:
   - Problem statement and motivation
   - Technical approach and risk assessment
   - Test evidence and benchmark diffs
   - Screenshots or CLI snippets if UI/terminal output changed

## Branch naming

Examples:

- `feat/cover-provider-cache`
- `fix/author-sort-particle-rule`
- `refactor/direct-engine-cursor`
- `docs/roadmap-update`

## Commit style

Use Conventional Commits format:

- `feat:` New user-facing feature or capability
- `fix:` Bug fix or error resolution
- `docs:` Documentation updates
- `refactor:` Code refactoring without behavior change
- `test:` Adding or updating tests
- `chore:` Build scripts, dependencies, CI configuration

## Coding standards

- Python: Python 3.12+ using `uv`.
- Type annotations: Strictly typed where practical (`mypy` compliant).
- Favor pure, deterministic functions for normalization and rule checking.
- Keep provider adapters thin, bounded, and failure-tolerant.
- Zero silent mutations: All writes require explicit confirmation or authorization.
- Fast execution: Direct engine queries must use streaming keysets, avoiding full-table memory loads.

## Required local checks

All checks must pass before opening a PR:

```bash
# Linting & Formatting
uv run ruff check .
uv run ruff format --check .

# Static Type Checking
uv run mypy src

# Deterministic backend gate (excludes service-backed suites explicitly)
uv run pytest \
  -m 'not benchmark and not ocr_live and not network' \
  --ignore=tests/test_retention_postgres_valkey.py \
  --ignore=tests/test_v2_supervised_pilot_integration.py

# Frontend Build (if modifying webui/)
cd webui && npm run build && cd ..
```

## PR checklist

- [ ] code follows project structure
- [ ] schemas updated if payloads changed
- [ ] CLI reference updated if flags changed
- [ ] tests added or updated
- [ ] snapshots reviewed intentionally
- [ ] no secrets or private evidence committed

## Test data rules

Allowed:

- synthetic fixtures
- public-domain books
- generated images
- mocked provider responses

Forbidden:

- commercial ebook uploads
- DRM-protected files
- full private evidence packages from real libraries

## Security and privacy

If the change affects:

- remote uploads
- credentials
- write/apply logic
- backups and undo

then mention it explicitly in the PR description.

## Publishing an exact revision

After an automatically triggered Gitea run passes for the candidate SHA, the
repository owner records a GitHub commit status named `bookwarden/gitea-canonical`
for that same SHA, linking its canonical Gitea run. The GitHub release workflow
requires this owner-attested result in addition to its local checks and immutable
image gates. This is a deliberate publication attestation because hosted GitHub
runners cannot reach the private Gitea endpoint; it is not an automatic mirror.
Never attest a failed, skipped, incomplete or different-revision run as successful.

Promote the reviewed source to both main branches without rewriting history,
then create the matching new version tag. Existing release tags are immutable.
All three image matrix jobs must finish successfully before the aggregate job
validates their receipts, emits `release-images.json`, and promotes semver image
tags. Create matching forge release notes and attach the manifest only after the
publication workflow succeeds. A source tag alone does not prove image delivery.

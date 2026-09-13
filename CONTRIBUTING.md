# Contributing

## Ground rules

- Start with an issue for large changes
- Keep pull requests small and reviewable
- Any write-path change must include tests and docs
- No secrets, personal libraries, or copyrighted books in the repo

## Dual-Forge Development Workflow

Calibre Bookwarden maintains a synchronized dual-forge topology:

1. **Gitea (Internal Primary & Hermetic CI)**:
   - URL: `http://192.168.0.122:3010/felix/calibre-bookwarden`
   - Hosts full-stack hermetic CI runners (PostgreSQL, Valkey, Tesseract, Calibre lab).
   - Core release gates and vulnerability scans run here.

2. **GitHub (Public Distribution & Community)**:
   - URL: `https://github.com/felixapel/calibre-bookwarden`
   - Canonical open-source distribution, issues, and external contributor PRs.
   - Mirrors releases and tags after Gitea verification.

### Contribution Workflow

1. Fork the repo (on GitHub or Gitea).
2. Create a feature branch: `git checkout -b feat/my-improvement`.
3. Run formatting, linting, and local tests.
4. Open a Pull Request on GitHub or Gitea with:
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

# Unit & Integration Tests (excluding heavy services/live OCR)
uv run pytest -m "not benchmark and not ocr_live and not network and not v2_live"

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
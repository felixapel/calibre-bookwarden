# Contributing

## Ground rules

- Start with an issue for large changes
- Keep pull requests small and reviewable
- Any write-path change must include tests and docs
- No secrets, personal libraries, or copyrighted books in the repo

## Development workflow

1. Fork the repo
2. Create a feature branch
3. Run formatting and tests locally
4. Open a PR with:
   - problem statement
   - approach
   - risk notes
   - test evidence
   - screenshots or sample report diffs if CLI output changed

## Branch naming

Examples:

- `feat/scan-command`
- `feat/openlibrary-adapter`
- `fix/judge-schema-validation`
- `docs/roadmap-update`

## Commit style

Use Conventional Commit-style prefixes where practical:

- `feat:`
- `fix:`
- `docs:`
- `refactor:`
- `test:`
- `chore:`

## Coding standards

- Python: typed where reasonable
- Favor pure functions in rules and normalization
- Keep provider adapters thin and explicit
- Do not hide risky behavior behind defaults
- Surface every external command in logs or artifacts

## Required local checks

All checks must pass before opening a PR:

```bash
uv run ruff check .
uv run ruff format --check .
uv run mypy src
uv run pytest -m "not benchmark and not ocr_live and not network"
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
# Repository operating rules

## Product boundary

This repository builds a local, supervised Calibre metadata auditor. Its core
job is to inspect each book and every attached format, identify the exact
manifestation when evidence permits, show uncertainty, and apply only an exact
human-authorized correction with verified rollback evidence.

Do not expand the product into a general library manager, SaaS, unattended
metadata writer, semantic search platform, Paperless replacement, audiobook
pipeline, or manga ecosystem unless a separately approved milestone supplies
user evidence and acceptance criteria.

## Safety and data

- Shadow/read-only verification is the default.
- Never test writes against a live Calibre library. Use generated fixtures or a
  disposable restored clone and prove the configured library root.
- Never weaken Tier A provenance, exact authorization, sealed evidence,
  before/after readback, format hashing, writer serialization, or rollback
  checks to make a test pass.
- OCR, vision, title search, and LLM output are review evidence, not identity
  authority by themselves.
- Runtime code must not create or migrate database schema. Use Alembic and
  explicit test schema setup.
- External providers fail closed: unavailable, malformed, or empty responses
  produce no candidate and never fabricated metadata.

## Repository and delivery

- Work only in this repository unless the user explicitly expands scope.
- Gitea is the canonical development remote and Gitea Actions is the canonical
  pipeline. Do not fetch, push, open PRs, or otherwise interact with GitHub
  unless the user explicitly requests it.
- Do not manually rerun Gitea Actions unless explicitly requested. New commits
  should trigger their own evidence.
- Keep commits atomic. Preserve unrelated user changes and inspect the working
  tree before staging.
- Frontend dependency management uses npm and `webui/package-lock.json`; do not
  add another lockfile.

## Required validation

For a normal backend change, run the smallest focused tests first, then:

```bash
uv run ruff check .
uv run ruff format --check .
uv run mypy src
uv run pytest \
  -m 'not benchmark and not ocr_live and not network' \
  --ignore=tests/test_retention_postgres_valkey.py \
  --ignore=tests/test_v2_supervised_pilot_integration.py
```

For frontend changes, from `webui/` run:

```bash
npm ci
npm run lint
npm run build
```

Changes to the supervised writer, migrations, production image, browser flow,
or real-service boundaries require the corresponding Gitea gate. A failed
required validation blocks progression unless the exception is explicit and
documented.

## Documentation

Keep README, roadmap, runbooks, and ADRs aligned with executable behavior. Do
not publish unsupported test counts, accuracy percentages, scale claims, release
tags, or production-readiness claims. Record durable architecture and product
decisions in `docs/decisions/`.

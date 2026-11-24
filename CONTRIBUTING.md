# Contributing

Bookwarden is developed on a private forge. The GitHub repository is a mirror,
updated by hand at each release, so a pull request there is read and, if it
fits, applied by hand on the forge, credited to you. Issues are welcome; for a
vulnerability, follow [SECURITY.md](SECURITY.md) instead.

## A change must pass

```bash
gofmt -l .                 # prints nothing
golangci-lint run ./...    # clean
go test -race ./...        # with BOOKWARDEN_TEST_DATABASE_URL set, see AGENTS.md
```

After a change to the image, also build it and run `scripts/check-image-size.sh`
and `scripts/smoke-image.sh`. After a change in `internal/library` or
`internal/verify`, run `scripts/integration-test.sh` against the synthetic
Calibre in `test/calibre`. Go comes from `mise.toml`. Commits follow
Conventional Commits. `AGENTS.md` has the full rules.

## Data

- Tests use synthetic data only. Never a real library, real book titles,
  excerpts or covers, and never test writes against a real Calibre.
- No secrets in commits, issues or logs: no API keys, passwords, tokens or
  internal host names.

# Security policy

## Reporting a vulnerability

Please report it privately, not in a public issue: use GitHub's private vulnerability reporting on the public repository. On its **Security** tab, choose "Report a vulnerability".

A useful report says:

- which version you run (the output of `bookwarden version`);
- how Bookwarden is deployed (Compose, Unraid, behind a reverse proxy or not);
- what you did, what happened, and what you expected;
- a way to reproduce it, which helps most.

Never include real library data: no book titles, authors, excerpts, database dumps or logs from your library, and no credentials. Describe the shape of the data instead, or use the synthetic library in `test/calibre`.

You get an answer within a week. Bookwarden is a one-person project, so a fix follows as soon as it can, not on a fixed schedule. Once a fix is released, the report is credited in the changelog unless you'd rather it wasn't.

## Supported versions

| Version | Supported |
|---|---|
| v2.0.x | Yes |
| v1.3.x and older (Certificate A, the Python version) | No. Retired. |

Only the newest v2.0.x patch release gets fixes.

## Threat model

Bookwarden is for one admin on a home or lab network. It isn't built to face the internet. In short:

- It protects your Calibre library, its own database and the API keys you give it.
- It reaches your library only through Calibre's content server, writes nothing until you turn writes on, and applies only fixes you approved, each read back and undoable.
- Book text leaves your machine only as front-matter excerpts sent to the extractor endpoint you configure.

The full model, with the code that enforces each control and the privacy section, is in [docs/threat-model.md](docs/threat-model.md).

Out of scope: an attacker who already controls the host, the Docker daemon, the Postgres database or the Calibre server; and exposing the web UI to the internet without a reverse proxy that you secure yourself.

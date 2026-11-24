# Threat model and privacy

Bookwarden is for one admin on a home or lab network. This page says what it protects, where it trusts or distrusts, which controls the code has, and which text leaves the machine. To report a problem, see [SECURITY.md](../SECURITY.md).

## Assets

| Asset | Why it matters | Where it lives |
|---|---|---|
| The Calibre library | The books and their metadata. The thing Bookwarden must not damage. | Calibre, reached only through its content server. Bookwarden never opens `metadata.db`. |
| The database | The sealed evidence of every check, the book text excerpts (up to 12,000 characters per format), decisions, the write ledger and the admin's password hash. | Postgres. A dump holds all of it. |
| The API keys | The Calibre password, the extractor and agent keys, the Google Books and Hardcover keys, the metrics token and the heartbeat URL, which holds a token. | Environment variables. Never logged, never shown (only whether they are set). |

## Trust boundaries

| Boundary | Trusted for | Not trusted for |
|---|---|---|
| **Calibre content server** | Its answers about the library, as a source to check against. | Being the truth: Calibre's current values are never authority on their own ([design section 7](superpowers/specs/2026-10-12-bookwarden-go-rewrite-design.md)). A write is accepted only after two readbacks agree. |
| **Metadata sources** (DNB, BNE, Open Library, Google Books, Wikidata, Hardcover) | Records found by an exact, checksum-valid ISBN. | Anything else. Wikidata and Hardcover are recorded and decide nothing. A source can be down or wrong, so nothing is guessed or made up. |
| **LLM extractors**, through an OpenAI-compatible endpoint | Choosing which printed span answers a question. | Their words. The excerpt is untrusted input, and an answer counts only if the grounding verifier finds it printed in the excerpt. A prompt injection can at worst pick a wrong printed span, never invent a value, and every fix still goes to you. |
| **The agent panel** (off by default) | A second opinion, shown after you decide. | Deciding anything. Its verdicts are never applied (ADR-029). |
| **The web UI** | The signed-in admin. | The network, other websites in the admin's browser, and the book files: titles and excerpts are shown escaped under a strict Content-Security-Policy. |
| **Book files** | Nothing. | Everything: EPUB, PDF and image parsing are bounded, and the native tools run sandboxed. |

## Controls

Each control names the code that holds it.

**Reaching out**
- **Exact-CIDR dialer** (ADR-033; `internal/netguard`, `internal/config/config.go`). The source, agent seat, extractor and heartbeat clients refuse every non-public address: loopback, private, link-local, CGNAT and the special ranges. Seats, extractors and the heartbeat reach a LAN or plain-http address only with their `_ALLOW_CIDR`, which accepts single addresses (`/32`, `/128`) and nothing wider; once set, that client can dial only those addresses. The sources can never reach a private address. The Calibre client is the exception: you configure its address explicitly, and it is usually on the LAN. The check runs at dial time, so a DNS name that later resolves to a private address is refused as well.
- **Source clients** (`internal/sources/client.go`). A fixed list of hosts, a pace per host, a time budget and a redirect limit.
- **Keys** stay out of logs, errors and pages. Configuration errors name the variable and never show a value (`internal/config/config.go`).

**Book files**
- **Sandboxed inspector** (`internal/sandbox`, ADR-016 and ADR-018). `pdftotext`, `pdftoppm` and `tesseract` run in a child process with rlimits for CPU, memory, file size and open files and a seccomp filter: no network, no new processes, no tracing, no mounts, no namespaces, keyrings, bpf, userfaultfd or perf events. Archive and XML limits bound the EPUB and ZIP readers (`internal/inspect`). A limit that is hit marks the format as not inspected; it is never a pass.

**Evidence and decisions**
- **Sealed evidence** (ADR-017, ADR-038; `internal/evidence`, `internal/decide/seal.go`). Each book's evidence is a canonical package with a hash. Decisions are derived from the sealed package alone. `bookwarden verify-seals` and the run's own seal check find any damage, and a broken seal turns the heartbeat down.
- **No fabricated values.** Sources are asked by exact ISBN only. A proposed value must come from a source or from the book's printed text, with the tier rules of ADR-020 and ADR-036.

**Writes**
- **Off by default.** The switch "Allow changes to Calibre" is off until you turn it on, and every change to it is recorded (`internal/store/fixrepo.go`). Keep the Calibre account read-only until then; Calibre itself refuses the write.
- **No automatic apply.** Mode "on" of the agent panel was retired (ADR-029, amendment of 2026-11-04). No setting or variable makes Bookwarden write a fix nobody approved, and Apply ignores approvals made by an agent.
- **Guarded writes** (ADR-021; `internal/fix`). Before each book, Bookwarden reads it again: if the book changed since the check, its fixes are stale and nothing is written. Blocked values, which Calibre would store differently, are never sent.
- **Readback.** After each write the book is read back twice, from Calibre's answer and from a fresh read. A difference restores the old values and opens an incident that pauses all writes until you acknowledge it.
- **Undo.** Every operation records each value before, as written and after, and can be undone while the book is unchanged. A crash is recovered from the ledger at the next start.

**The web UI**
- **Setup and password** (`internal/auth`). The first start prints a one-time setup code to the log; the password is at least 12 characters and stored as argon2id. A restart before setup issues a new code.
- **Sessions** (`internal/auth/service.go`, `internal/web/session.go`). Server-side, expiring after 12 hours idle or 7 days. The cookie is `HttpOnly` and `SameSite=Strict`, and `Secure` when the request came over https.
- **CSRF.** Every signed-in state-changing request needs the session's CSRF token (`requireSession`). Setup and login are protected by the setup code and the login backoff.
- **Login backoff** (`internal/auth/limiter.go`). After five failures a client waits longer each time, up to 15 minutes, per IPv4 address or IPv6 /64. Across all clients, after 50 failures within an hour every attempt waits 2 seconds before it is checked, and before it takes one of the two hashing slots, so waiting attempts hold nothing shared; it slows, never locks out.
- **Allowed hosts** (`internal/web/middleware.go`). Requests for another `Host` are refused, which blocks DNS rebinding. Probes (`/healthz`, `/readyz`, `/metrics`) are exempt; `/metrics` needs its bearer token.
- **Headers.** A Content-Security-Policy, `X-Content-Type-Options`, `X-Frame-Options: DENY`, and a 64 KiB cap on request bodies.
- **Forwarded headers** are believed only from `BOOKWARDEN_TRUSTED_PROXIES`, whose CIDRs may be no wider than /16 (IPv4) or /48 (IPv6). Even so, a trusted proxy that passes on a forged `X-Forwarded-For` lets a client pick its address; the login limiter's global budget (2 s per attempt after 50 failures within an hour, from anywhere) bounds that.

**The container** (`Dockerfile`, `deploy/unraid/bookwarden.xml`, `deploy/compose/docker-compose.yml`). Non-root user, read-only root file system, a tmpfs `/tmp`, all capabilities dropped, `no-new-privileges`, and memory and process limits. Postgres sits on a network with no route out.

## Privacy: which text leaves the machine

**Front-matter excerpts go to the extractor endpoint, and nothing else of the book's text does.**

- **What is sent.** For each EPUB format, an excerpt of the front matter, at most 12,000 characters (`internal/evidence/excerpt.go`), with the question to answer. It goes to the endpoint you configure, `BOOKWARDEN_EXTRACTOR_BIFROST_URL` or the opencode-go endpoint, and from there to that endpoint's upstream providers, for each of the three extractors (`internal/extractor`). With no extractor key or gateway set, nothing is sent.
- **Where it goes after that.** An OpenAI-compatible gateway such as Bifrost can log every prompt and answer. When Bookwarden calls through `BOOKWARDEN_EXTRACTOR_BIFROST_URL`, it sends `x-bf-disable-content-logging: true` on every call (`internal/agent/seat.go`, set in `internal/config/config.go`). The gateway honours it only if its per-request override of content logging is enabled. Check that, or turn content logging off for the whole gateway. A gateway of your own on the LAN, or a local model behind one, keeps the text on your network.
- **What Bookwarden logs.** Excerpt text and raw model answers never appear in logs, metrics or error messages. Logs say which book and which outcome, and run counts.
- **What the database keeps.** The excerpts, so the book page can show where a hint came from, and the extractors' answers. They are part of every dump. Retention deletes the excerpts that no kept run refers to (`BOOKWARDEN_RETAIN_RUNS`).

**Other things that leave the machine**
- **Source lookups** send an exact ISBN, and the request's `User-Agent` (`bookwarden/<version> (+<project URL>)`), to DNB, BNE, Open Library, Google Books, Wikidata and Hardcover. No title, author or text goes with them.
- **The agent panel**, off by default, sends each judge a packet per book: the open proposals, Calibre's record of the book (title, authors and the like), the files' metadata and the source records. It carries no file text, no OCR and no covers. It goes to the three seats' endpoints (Anthropic, opencode and Google by default). If you don't want that, leave agent fixing off.
- **The heartbeat** sends a short message such as `run 15 completed: 3207 books, 0 broken seals ...` to your Uptime Kuma. It never holds book text.
- **Nothing else.** No telemetry, no update check and no analytics.

## Supply chain

- **What is pinned.** Workflow actions by commit SHA; tools the workflows download (`crane`, `cosign`, `git-cliff`, `golangci-lint`, `gitleaks`) by SHA-256; container images in the Dockerfile, the workflows and the scripts (base images, Postgres, Trivy, Syft, Renovate) by `name:tag@sha256` digest; Go modules by `go.sum`.
- **What a release signature proves** (ADR-042). When the cosign secrets are set, release images are signed and their SBOM attested by digest with a key pair and no transparency log. A valid signature says that someone with the private key signed this digest. It does not say when: whoever holds a leaked key can sign a new image that looks like an old release.
- **How to verify.** `cosign verify --key cosign.pub --insecure-ignore-tlog <image>@sha256:<digest>`, and `cosign verify-attestation` for the SBOM, as the README's "Releases" section shows. To run exactly what you verified, pull it by its digest; the compose example and the Unraid template name a release tag.
- **How updates arrive.** Renovate opens pull requests for Go modules, the Dockerfile's images, the actions and images pinned in the workflows, the images in `scripts/` and `deploy/compose/`, and the Go pin in `mise.toml` (`renovate.json`; it needs its token). The nightly workflow builds `main` and scans that image with Trivy, failing on any fixable HIGH or CRITICAL vulnerability. Tools pinned by SHA-256 are bumped by hand.

## Known limits

- Anyone who can reach the web UI and has the setup code, before you finish setup, becomes the admin. Finish setup right after the first start.
- The UI speaks plain HTTP on the LAN unless you put a reverse proxy with TLS in front. Don't expose it to the internet without one.
- A leaked Calibre account, or database access, defeats the controls above. Protect the Postgres volume and its dumps like the library.
- Release signatures have no transparency log (ADR-042). Verify by digest.
- Browsers don't separate cookies by port. On a server where Bookwarden and other services share one address, every request to those services carries Bookwarden's session cookie; a service that logs headers, or is compromised, can act as the admin for up to 7 days. With such a session one can turn writes on, approve and apply any fix Bookwarden proposed, undo past changes, acknowledge incidents and read the stored excerpts; one cannot write arbitrary values. Give Bookwarden a host name of its own and put only that name in `BOOKWARDEN_ALLOWED_HOSTS`, so the shared address is refused.

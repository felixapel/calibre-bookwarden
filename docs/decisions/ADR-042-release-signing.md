# ADR-042: Release signing without a transparency log

- Status: Accepted
- Date: 2026-11-04

## Context

A `v*` tag builds the image in Gitea Actions, pushes it to the LAN Gitea registry
and, when its secrets exist, to `ghcr.io/felixapel/bookwarden`, and signs every
pushed digest and its SBOM with cosign (`release.yml`). The key is a plain key
pair (`COSIGN_KEY`, `COSIGN_PASSWORD`); keyless signing needs an OIDC identity
that the self-hosted runner doesn't have.

Cosign v3 signs with a signing config. By default that config includes the public
Rekor transparency log, and a key-based signature is then uploaded to it, which
publishes the image name and digest. The forge's LAN image name (`<forge-host>/...`)
has no business in a public log. `--tlog-upload=false` alone is rejected by cosign
v3.1.4 together with its default signing config.

## Decision

1. Sign with a key pair, using `cosign signing-config create` with no services
   (no Rekor, no timestamp authority, no Fulcio), passed as `--signing-config`.
   Nothing is uploaded to a public service. Verification therefore needs
   `--insecure-ignore-tlog`, and passes a key (`--key cosign.pub`).
2. Sign and attest by digest, never by tag, and sign each pushed digest (Gitea and
   GHCR). The SBOM is attached as a signed `spdxjson` attestation.
3. TLS stays on for ghcr.io. Only references under the Gitea registry get
   `--allow-http-registry`, because that registry is plain HTTP.
4. Keep the private key and its password only as repository secrets, read from the
   environment (`env://COSIGN_KEY`), never written to disk.

## Consequences

- **Nothing proves when a signature was made.** Without a transparency log entry or
  a signed timestamp, a signature carries no time. Anyone who gets the private key
  and its password can sign any image, including one built today that claims to be
  an old release, and a verifier cannot tell it from a signature made at release
  time. The signature says "someone with the key signed this digest", not "this was
  signed on release day". Keep the key secret, rotate it if it leaks, and publish
  the new `cosign.pub` with the next release; older signatures can't be told apart
  from forgeries made with the old key.
- Verifiers must ignore the transparency log explicitly, which the README shows.
- If a public image needs a verifiable time, switch the GHCR signature to the
  default signing config (Rekor) in a later ADR; the Gitea signature would stay
  log-free.
- The signed claim covers the digest of what `crane push` wrote, which is the
  `docker save` tarball loaded as an image. It's the digest printed at the end of
  the run and recorded in the release's `digests.txt`.

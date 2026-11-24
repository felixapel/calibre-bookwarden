# ADR-014: Remove the bundled Prometheus

- Status: Accepted
- Date: 2026-10-12
- Decision owners: maintainer

## Context

Certificate A offered an opt-in `monitoring` Compose profile that ran the
official Prometheus image next to the app. It was the second source of
scanner-gate failures: ADR-011 needed a time-bounded exception for the 3.13.3
LTS image, and on 2026-10-12 the 3.15.0 image (the newest release) failed the
HIGH/CRITICAL gate on CVE-2026-78667, CVE-2026-78669 and CVE-2026-97031 in its
Go 1.27.1 standard library and `golang.org/x/net` 0.58.0. No patched release
existed, so every pull request's container gate failed on an image the
production deployment never ran.

The bundled collector also added a container, a network, a generated secret
file and preflight topology checks, while the maintainer wants fewer containers.

## Decision

Bookwarden ships no Prometheus. The app keeps `GET /api/metrics` (authenticated
with `X-API-Key`) on its loopback port, and `ops/monitoring/alerts.yml` remains
as tested alert rules for a Prometheus the operator already runs. CI validates
the rules with `promtool test rules`, using the pinned Prometheus image as a
test tool only: it is neither shipped nor deployed, so it is not part of the
release-image vulnerability gate.

Removed: the `prometheus` service, the `monitoring` profile and network, the
`bookaudit-api-key` Compose secret, `ops/monitoring/prometheus.yml`,
`ops/monitoring/Dockerfile.ci`, `ops/monitoring/.trivyignore`, the preflight's
monitoring graph check and secret generation, and the CI Prometheus smoke and
scan.

## Consequences

- The release-image gate covers only images Bookwarden ships: app, writer and
  Caddy edge.
- Monitoring becomes an operator integration. The runbook documents the scrape
  configuration and the single-scope API key caveat.
- Existing deployments that enabled the profile keep their `.monitoring/`
  directory; remove it and the stopped `prometheus` container by hand.
- ADR-011 stays revoked and historical.

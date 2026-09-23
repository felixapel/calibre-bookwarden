# Security policy

## Reporting a vulnerability

Please avoid filing a public issue with exploit details, library records,
credentials, or unredacted logs. Use GitHub's
[private vulnerability reporting](https://github.com/felixapel/calibre-bookwarden/security/advisories/new)
if it is available for this repository. If that form is unavailable, open a
public issue containing **only** a request for a private reporting channel;
do not include the vulnerability details in that issue.

Include the affected version or commit, a minimal reproduction using synthetic
data, the impact, and any relevant configuration. Please do not probe other
people's libraries or services.

## Supported scope

The current Certificate A release line receives security fixes. Historical
single-container templates and development-only write surfaces are not
production-supported. The supported runtime and deployment boundary are
described in [production readiness](docs/production-readiness.md) and the
[operations runbook](docs/runbooks/production-operations.md).

Known residual risks are recorded in the relevant release notes and
[security decisions](docs/decisions/), including the time-bounded
[Prometheus LTS exception](docs/decisions/ADR-011-prometheus-lts-publication-exception.md).
An upstream dependency advisory does not by itself establish that a particular
deployment exposes the vulnerable code path.

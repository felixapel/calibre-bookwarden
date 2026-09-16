# ADR-011: Time-bounded Prometheus LTS publication exception

- Status: Accepted
- Date: 2026-09-16
- Decision owners: release maintainers

## Context

The Certificate A monitoring profile pins the official
`prom/prometheus:v3.13.3` LTS OCI index
`sha256:6976aa8a60fec930796ce5772b8d12da7a318a5daa8d40d69c5c7819a05eeed7`.
Inspection of its Linux amd64 binaries found Go 1.26.8 and gRPC 1.82.1. Trivy
reports HIGH findings `CVE-2026-84304` and `CVE-2026-84445` in that gRPC
dependency. A release hold previously required a future official image with
the fixes.

The publication decision now accepts these two known findings for the official
LTS image. The exception is a risk acceptance, not a statement that the image
is clean or that loopback-only access and an internal network eliminate the
risk. The monitoring profile remains private/loopback for its UI, but that
deployment shape is not the rationale for suppressing the findings.

`CVE-2026-42154` is separate: it is the Prometheus module
`+dirty` pseudo-version false positive already distinguished by the scanner
policy. It must not be used to characterize the gRPC findings as false
positives.

## Decision

Allow only `CVE-2026-84304` and `CVE-2026-84445` for the pinned official
Prometheus 3.13.3 LTS image in the scoped Prometheus scanner policy. The
exception must:

1. apply only to that image and those two CVEs;
2. include a review deadline of 2026-10-16;
3. be revoked immediately when an upstream official fixed image is available;
4. require a fresh exact-commit Gitea scan and monitoring smoke after the pin
   changes; and
5. leave all other fixable HIGH/CRITICAL findings failing the release gate.

No general waiver, release-candidate substitution, custom rebuild, or scanner
weakening is authorized by this decision.

## Consequences

v1.3.1 may be prepared for publication with this explicitly documented
residual risk. It cannot claim zero HIGH/CRITICAL vulnerabilities for the full
stack. The app, writer, and Caddy scan observations remain scoped to the
reviewed source revision and configured policy.

Publication still requires a successful exact-final-revision pipeline and a
verified manifest of immutable image digests. Deployment readiness separately
requires a whole-library restore drill, a calibrated reviewed corpus,
external-writer exclusion, and explicit deployment authorization. Publishing
artifacts does not claim that these deployment gates have passed.

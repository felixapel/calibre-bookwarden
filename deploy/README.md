# Deployment support

Use the repository-root [Compose definition](../docker-compose.yml),
[installation guide](../INSTALL.md), and
[production operations runbook](../docs/runbooks/production-operations.md).
The supported profile is Certificate A: a stopped Calibre library mounted
read-only, PostgreSQL and Valkey, an exact release digest and restricted roles.

## Retired single-container templates

The historical Unraid XML and TrueNAS YAML templates are preserved under
`archive/` with `.disabled` extensions for provenance only. They configure
SQLite/in-memory state, mutable image tags, and writable library defaults;
they do not satisfy the Certificate A contract. Do not import, rename or launch
them as v1.3.1 installers. No supported one-click Unraid/TrueNAS adapter is
included in this release. A future adapter needs its own integration validation.

The images published with a release do not by themselves authorize a live
library write or prove production readiness. Follow the digest-bound preparation
and operator gates in the runbook, including the documented Prometheus risk
exception where the optional monitoring profile is enabled.

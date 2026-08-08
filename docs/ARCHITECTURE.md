# Certificate A architecture

The production architecture is defined by
[ADR-005](decisions/ADR-005-certificate-a-production-boundary.md).

```text
Caddy TLS + whole-site auth
            |
            v
loopback Certificate A app -- bookaudit_app ACL --> PostgreSQL
                                                   ^
                                                   |
stopped Calibre folder --ro--> verifier -----------+
                              | claim + fence + sealed evidence
                              +----> private tmpfs scratch

Valkey <---- rate limits and release/schema/library-bound heartbeat only
Google Books / Open Library <---- one checksum-valid ISBN only
```

The app has no library mount and cannot persist evidence. The verifier has no
Calibre executable and cannot write the library. PostgreSQL owns request order,
atomic claim, lease expiry, monotonic fencing, cancellation, progress, and
evidence. Runtime processes validate the Alembic head but never migrate it.

The historical root [architecture document](../ARCHITECTURE.md) describes the
broader research codebase. Where it discusses V1, LLM, vector, MCP, watcher,
Content Server, or writer components, those components are outside Certificate
A and cannot override ADR-005.

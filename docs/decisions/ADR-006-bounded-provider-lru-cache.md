# ADR-006: Bounded persistent cache for structured providers

## Status
Superseded by [ADR-015](ADR-015-go-rewrite.md). Previously: Accepted; revised 2026-10-07 for the manifestation V2 adapters.

## Context
Provider instances are recreated for each verifier run. An in-memory cache
cannot reuse successful lookups across runs. The production verifier already
has a private, persistent scratch volume.

## Decision
Cache only fully parsed successful provider JSON, including valid no-match,
under `scratch_dir/provider-cache` in production and the artifacts directory
elsewhere. The key includes provider, canonical ISBN and response contract.
Re-run adapter parsing and exact-ISBN filtering on every hit. Check Google
credentials before cache access. Failed requests and malformed responses are
never cached; expiry cannot fall back to old evidence after a failed fetch.

Default TTL is 24 hours; zero bypasses all cache I/O. Stored retrieval/expiry
are never refreshed on hits, and lowering TTL shortens existing entries.
8192 fixed hash slots and a 128 KiB per-entry limit bound completed entries to 1 GiB.
Collisions replace entries; full envelope keys prevent cross-ISBN/provider reuse.
Oversized valid responses bypass caching. Secure atomic writes require a
private verifier-owned directory; cache failures preserve valid fresh results.

## Consequences
Warm bounded scans can avoid provider requests across process/run boundaries.
Capacity collisions and expiry may require another lookup. Each configured
client paces HTTP attempts, including retries, at least one second apart inside
its total request deadline. Pacing is per client/run, not a distributed quota.
Attempt, cache-hit and failure logs provide the operational counts.

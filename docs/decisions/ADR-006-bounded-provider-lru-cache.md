# ADR-006: Bounded In-Memory LRU/TTL Cache for Metadata Providers

## Status
Accepted

## Date
2026-08-20

## Context
When auditing large collections or multi-format books sharing identical ISBNs and titles, querying external providers (OpenLibrary, Google Books) created a fresh HTTP client per fetch without response caching. This resulted in redundant network round-trips, rate-limiting HTTP 429 penalties, and excessive latency during bulk audits.

External provider responses for a given query (URL + sorted query parameters) are essentially idempotent over a short time horizon.

## Decision
Introduce a process-level, bounded LRU cache with time-to-live (`TTL = 300` seconds) in `src/calibre_ai_auditor/providers/cache.py`:
- Implemented using an `OrderedDict[str, tuple[float, Any]]` protected by an `asyncio.Lock`.
- Hard-bounded to a maximum of 512 entries (`_MAX_ENTRIES = 512`) to strictly prevent memory leaks.
- Uses `cached_get_json(url, params, timeout=15.0, ttl=300.0)`.
- Evicts oldest entries (`popitem(last=False)`) when capacity is reached.
- Provides an explicit `clear_provider_cache()` hook to ensure zero state pollution across automated tests.

## Consequences
- Multi-format books sharing an ISBN pay exactly one network round-trip.
- Total memory usage remains bounded to less than 5 MB even during 10,000+ volume scans.
- Test suites can safely clear the cache between test fixtures.

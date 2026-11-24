# ADR-003: Supervised local Calibre auditor scope

- **Status:** Accepted
- **Date:** 2026-07-15

## Context

The repository contains useful but overlapping historical surfaces: legacy
scan/audit/apply flows, Manifestation V2, comics enrichment, MCP, ingest,
semantic duplicates, Paperless integration, and multi-host inference. That
breadth obscures the user outcome and encourages claims that are not supported
by representative library data.

The strongest implemented safety properties are in Manifestation V2: per-format
inspection, exact-identifier provider checks, provenance tiers, sealed evidence,
manual authorization, a serial privileged writer, readback, and rollback.
Repository evidence does not establish demand for a general library-management
platform or safe unattended correction.

## Decision

The product is a local, supervised metadata auditor for a Calibre operator.
Its primary workflow is:

1. Inventory a configured Calibre library without modifying it.
2. Audit one book at a time and inspect every attached ebook format.
3. Separate embedded metadata, visible content, OCR, vision, LLM, and external
   provider evidence by provenance and independence root.
4. Identify the exact manifestation when the evidence contract permits it;
   otherwise return an explicit review or conflict state.
5. Show current values, proposed values, sources, and uncertainty to a human.
6. Apply one exact authorized patch through the sole writer, verify readback,
   preserve rollback artifacts, and record the outcome.

Manifestation V2 is the target contract. Legacy V1 records may remain readable
during migration but must not regain a write path.

The following are explicit non-goals until separate evidence and approval exist:

- unattended or bulk metadata writes;
- replacing Calibre as the library manager;
- treating LLM, vision, OCR, title search, or the current Calibre record as
  self-authenticating identity evidence;
- multi-user SaaS;
- expansion of Paperless, Qdrant, ingest watchers, audiobooks, Komga/Kavita, or
  advanced manga workflows;
- performance claims for complete libraries based only on resolver or in-memory
  microbenchmarks.

## Architecture constraints

- Shadow/read-only remains the default and production writes remain a staged,
  operator-owned promotion decision.
- Recognition and evidence collection are separate from correction policy.
- External providers fail closed and may not fabricate fallback metadata.
- Database schema is owned by Alembic, never request-time code.
- The audit execution path should become durable before long-running real
  library runs are treated as reliable; the current in-process task mechanism
  is not sufficient evidence of restart safety.
- One canonical V2 persistence/read model should replace V1/V2 duplication
  incrementally, with compatibility and rollback documented.
- Gitea is the canonical development and validation system.

## Consequences

The project continues and is corrected incrementally rather than rewritten.
Work that improves exactness, durability, review quality, safe correction, and
operator diagnostics has priority. Scope expansion is deferred. Tier B is a
valid honest result, and a lower correction rate is preferable to a plausible
but incorrect edition match.

Success must be measured on a representative, human-reviewed corpus: evidence
coverage, Tier A/B/C distribution, exact-identity precision, proposed-patch
precision, provider conflicts, processing latency, and review time. Synthetic
tests and microbenchmarks remain regression tools, not product-accuracy proof.

## Alternatives considered

### Continue as a full-stack library-management system

Rejected because the repository does not demonstrate users or operational value
for that breadth, while several integrations remain partial or legacy.

### Rebuild the project

Rejected because the V2 evidence and writer boundaries are valuable and tested.
Incremental correction is safer, faster, and more reversible.

### Enable unattended Tier A writes after calibration

Rejected for the current target. Calibration informs human review and future
decisions but does not authorize writes by itself.

### Stop the project

Rejected while the supervised auditor can still deliver a clear outcome. The
decision should be revisited only after representative read-only data shows
that exact identification or review economics are not viable.

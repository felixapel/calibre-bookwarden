# Architecture Decision Records (ADRs)

This directory documents the key architectural, safety, and operational decisions for **Calibre Bookwarden**:

| ADR | Title | Date | Status |
|---|---|---|---|
| [ADR-001](ADR-001-journaled-retention-recovery.md) | Resume Journaled Retention Deletion After Interruption | 2026-07-13 | Superseded by ADR-015 |
| [ADR-002](ADR-002-exact-manifestation-v2.md) | Exact-Manifestation Metadata Verification (V2) | 2026-07-14 | Accepted; amended by ADR-015 |
| [ADR-003](ADR-003-supervised-local-auditor-scope.md) | Supervised Local Calibre Auditor Scope | 2026-07-15 | Accepted |
| [ADR-004](ADR-004-read-only-content-server-inventory.md) | Capability-Limited Content Server Adapter for Live Inventory | 2026-07-16 | Superseded by ADR-015 |
| [ADR-005](ADR-005-certificate-a-production-boundary.md) | Certificate A Production Boundary (Read-Only Aisle) | 2026-08-08 | Superseded by ADR-015 |
| [ADR-006](ADR-006-bounded-provider-lru-cache.md) | Bounded In-Memory LRU/TTL Cache for Metadata Providers | 2026-08-20 | Superseded by ADR-015 |
| [ADR-007](ADR-007-mathematical-cover-quality-scoring.md) | Mathematical Cover Quality Scoring (CQS 0-100) & Defect Detection | 2026-08-25 | Superseded by ADR-015 |
| [ADR-008](ADR-008-interactive-cover-deck-triage.md) | Interactive Cover Deck Triage Workflow (HTMX + React) | 2026-08-28 | Superseded by ADR-010 |
| [ADR-009](ADR-009-direct-calibre-engine-forensics.md) | DirectCalibreEngine SQLite Access for Homelab Forensics | 2026-09-01 | Superseded by ADR-010 |
| [ADR-010](ADR-010-retire-direct-mutations.md) | Retire Direct Mutations and Preserve the Supervised Writer | 2026-09-16 | Superseded by ADR-015 |
| [ADR-011](ADR-011-prometheus-lts-publication-exception.md) | Time-bounded Prometheus LTS publication exception | 2026-09-16 | Revoked 2026-10-02 |
| [ADR-012](ADR-012-retire-development-web-app.md) | Retire the development web application | 2026-10-04 | Superseded by ADR-015 |
| [ADR-013](ADR-013-retire-v1-cli-pipeline.md) | Retire the V1 CLI pipeline | 2026-10-05 | Superseded by ADR-015 |
| [ADR-014](ADR-014-remove-bundled-prometheus.md) | Remove the bundled Prometheus | 2026-10-12 | Accepted |
| [ADR-015](ADR-015-go-rewrite.md) | Rewrite bookwarden in Go as one container | 2026-10-12 | Accepted |
| [ADR-016](ADR-016-inspector-memory-bounds.md) | Bound inspector allocations and preserve incomplete evidence | 2026-10-13 | Accepted |
| [ADR-017](ADR-017-sealed-findings-and-reports.md) | Version local findings and verify reports against sealed evidence | 2026-10-13 | Accepted |
| [ADR-018](ADR-018-ocr-sandbox-isbn-hints-title-sort.md) | Sandbox PDF ISBN hints and check title sort against Calibre's rule | 2026-10-18 | Accepted |
| [ADR-019](ADR-019-source-lookups.md) | Look up ISBNs through one hardened client and seal the validated records | 2026-10-18 | Accepted |
| [ADR-020](ADR-020-tiers-and-proposals.md) | Derive tiers and per-field proposals from sealed files and identity records | 2026-10-18 | Accepted |
| [ADR-021](ADR-021-guarded-writes.md) | Write approved proposals through a guarded, write-ahead ledger | 2026-10-19 | Accepted |
| [ADR-022](ADR-022-fixing-pages.md) | Drive the guarded writer from the pages, in the background, one book at a time | 2026-10-19 | Accepted |
| [ADR-023](ADR-023-cover-checks.md) | Check covers through the content server and seal integer measures | 2026-10-19 | Accepted |
| [ADR-024](ADR-024-duplicates.md) | Report possible duplicates as a re-verified projection of each run | 2026-10-20 | Accepted |
| [ADR-025](ADR-025-cutover-after-early-retirement.md) | Cut over against Certificate A's archive, since it was retired first | 2026-10-20 | Accepted |
| [ADR-026](ADR-026-title-proposals-never-shorten.md) | A title proposal never shortens Calibre's title | 2026-10-26 | Accepted |
| [ADR-027](ADR-027-author-proposals-never-drop-or-reform.md) | An authors proposal never only drops or re-forms Calibre's authors | 2026-10-26 | Accepted |
| [ADR-028](ADR-028-sort-fixes-from-calibre-rules.md) | Title and author sort fixes restate Calibre's own rules | 2026-10-26 | Accepted |
| [ADR-029](ADR-029-agent-fixing.md) | A unanimous three-model panel may decide proposals | 2026-10-27 | Accepted; amended 2026-11-04: mode on retired |
| [ADR-030](ADR-030-record-match-tolerances.md) | A source record matches the files despite what records routinely leave out | 2026-10-28 | Accepted |
| [ADR-031](ADR-031-metadata-style-guide.md) | A metadata style guide defines the value every field should have | 2026-10-29 | Accepted |
| [ADR-032](ADR-032-front-matter-evidence.md) | Read ISBN hints from EPUB front matter, with their context, and keep bounded excerpts | 2026-10-29 | Accepted (parts 1 and 2) |
| [ADR-033](ADR-033-providers.md) | Agent seats are providers with their own egress, family and quota state | 2026-10-29 | Accepted |
| [ADR-034](ADR-034-blind-individual-decisions-are-labels.md) | Blind individual decisions are the labels that earn automation | 2026-10-29 | Accepted (part 1; carried decisions); amended 2026-11-04: review-first; amended 2026-11-24: approvals carry |
| [ADR-035](ADR-035-identity-v6-and-more-sources.md) | Look books up in Wikidata and Hardcover, recorded only; identity v6; findings v5 | 2026-10-29 | Accepted (parts 1, 2 and 3) |
| [ADR-036](ADR-036-text-root.md) | The book's printed text is a root (decision v8) | 2026-11-04 | Accepted |
| [ADR-037](ADR-037-series-and-cover-writes.md) | Series and cover writes | 2026-11-04 | Deferred (2026-11-08) |
| [ADR-038](ADR-038-verified-projection.md) | Serve report pages from a verified projection | 2026-10-29 | Accepted |
| [ADR-039](ADR-039-scheduled-incremental-runs-and-retention.md) | Run daily incrementally and weekly in full, reuse unchanged file evidence, and prune runs and excerpts | 2026-11-01 | Accepted |
| [ADR-041](ADR-041-heartbeat.md) | Report every scheduled run to an Uptime Kuma push monitor | 2026-11-04 | Accepted |
| [ADR-042](ADR-042-release-signing.md) | Sign release images with a key and no transparency log | 2026-11-04 | Accepted |
| [ADR-043](ADR-043-cover-candidates.md) | Cover candidates: the EPUB's own cover, measures only | 2026-11-08 | Accepted |
| [ADR-044](ADR-044-source-cache-refresh-budget.md) | Source cache: stale answers and a refresh budget | 2026-11-22 | Accepted |
| [ADR-045](ADR-045-decision-v10-text-root-corrections.md) | Decision v10: corrections to the text root | 2026-11-23 | Accepted |
| [ADR-046](ADR-046-extract-v4-folds.md) | Extract v4: the verifier folds case, dashes and invisible characters | 2026-11-24 | Accepted |
| [ADR-047](ADR-047-public-mirror-snapshots.md) | The public mirror is a snapshot per release | 2026-11-23 | Accepted |

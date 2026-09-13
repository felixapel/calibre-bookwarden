# Calibre Bookwarden: Architecture Specification

Calibre Bookwarden is architected around two complementary execution profiles:
1. **Certificate A Production Boundary**: Strict enterprise fail-closed auditing on a cold, stopped library with complete process isolation, PostgreSQL ACLs, and non-root containers.
2. **Homelab & Forensic Power Tools (DirectCalibreEngine)**: High-speed, in-process SQLite companion engine operating at **6,090 books/sec** with zero N+1 queries, CQS vision scoring, and zero-downtime hot-reloading for Calibre-Web.

---

## 1. Profile Comparison

| Characteristic | Certificate A (Enterprise) | Homelab Sidecar / Power Tools |
|---|---|---|
| **Primary Target** | Immutable compliance & sealed evidence | Rapid library forensics, cover triage, and metadata repair |
| **Calibre State** | Strictly stopped; WAL/sidecars forbidden | Active companion with Calibre-Web |
| **Database Backend** | PostgreSQL (partitioned roles) + Valkey | SQLite (`metadata.db` direct + `.state/bookaudit.db`) |
| **Throughput** | Sequential format inspection (safe tmpfs) | Keyset streaming up to **6,090 books/sec** |
| **Web App Authority** | Zero library mount; request creation only | Direct read & supervised curation (`DirectCalibreEngine`) |
| **Curation Tools** | Sealed Tier A/B/C packages; privileged writer | 360° Auditing, CQS (0-100), Cover Deck, Author Sort Sync |
| **Governance ADR** | [ADR-005](decisions/ADR-005-certificate-a-production-boundary.md) | [ADR-009](decisions/ADR-009-direct-calibre-engine-forensics.md) |

---

## 2. Certificate A Architecture

Defined by [ADR-005](decisions/ADR-005-certificate-a-production-boundary.md):

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

- **App Isolation**: The web application has no filesystem mount to the Calibre library and cannot persist evidence.
- **Verifier Fence**: The verifier mounts `/library:ro` only. It copies formats into an ephemeral, bounded tmpfs scratch area for inspection, hashing original bytes without alteration.
- **Transactional State**: PostgreSQL owns request order, atomic claims (`FOR UPDATE SKIP LOCKED`), monotonic fence tokens, lease timeouts, and cryptographic evidence seals.
- **Privilege Separation**: Dedicated PostgreSQL roles (`bookaudit_app`, `bookaudit_verifier`, `bookaudit_migrator`). Runtime workers never provision roles or migrate schema.

---

## 3. Homelab Sidecar & DirectCalibreEngine Architecture

Defined by [ADR-009](decisions/ADR-009-direct-calibre-engine-forensics.md):

```text
┌────────────────────────────────────────────────────────┐
│               Calibre Bookwarden (Sidecar)             │
│                                                        │
│  ┌────────────────────┐      ┌──────────────────────┐  │
│  │  Cover Deck UI     │      │   CLI (bookwarden)   │  │
│  │ (HTMX + Tailwind)  │      │ (audit-360, sync)    │  │
│  └─────────┬──────────┘      └──────────┬───────────┘  │
│            │                            │              │
│            ▼                            ▼              │
│  ┌──────────────────────────────────────────────────┐  │
│  │              DirectCalibreEngine                 │  │
│  │  • Keyset Streaming (<32 MB RAM)                │  │
│  │  • Python SQLite Triggers (title_sort/author)    │  │
│  │  • Atomic VACUUM INTO snapshots                  │  │
│  │  • CQS Evaluator (Laplacian Sharpness, Entropy)  │  │
│  └──────────────────────┬───────────────────────────┘  │
└─────────────────────────┼──────────────────────────────┘
                          │ Direct SQLite connection
                          ▼
           ┌──────────────────────────────┐
           │   /calibre/metadata.db       │
           │  (books, authors, data, ...) │
           └──────────────┬───────────────┘
                          │ HTTP /reconnect
                          ▼
           ┌──────────────────────────────┐
           │     Calibre-Web Automated    │
           │ (Zero-Downtime Session Sync) │
           └──────────────────────────────┘
```

- **Direct SQLite Invariants**: Connects directly to `metadata.db` with `busy_timeout=30000` and registers custom Python functions (`title_sort`, `author_sort`) to match Calibre's native trigger behavior.
- **Keyset Streaming**: Replaces offset pagination with keyset cursors (`WHERE id > last_id ORDER BY id LIMIT 500`), enabling linear-time scanning across 100,000+ volumes without memory bloat.
- **Cover Forensics (CQS)**: Evaluates cover images using mathematical algorithms (pixel dimensions, golden aspect ratio 2:3, Laplacian edge sharpness, and Shannon entropy) without requiring heavyweight neural networks.
- **Zero-Downtime Sync**: Interacts with Calibre-Web via `GET /reconnect` to instantly refresh cached database sessions and purges physical thumbnail files (`/thumbnails/<bid>.*`) to reflect upgraded covers immediately.

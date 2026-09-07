# Data Model

## Overview

**Calibre Bookwarden** uses a multi-layered persistence strategy to balance performance and explainability.

1.  **System-of-Record**: A relational database (**PostgreSQL** or **SQLite**) stores book records, audit runs, background job status, and resolution decisions.
2.  **Artifact Store**: A directory structure stores heavyweight evidence such as cover images, text snippets, and OPF backups.

---

## Manifestation V2 source records

Manifestation V2 stores its strict schema inside
`EvidencePackage.observations` with `schema_version=2`; the legacy `decision`
column remains null so V1 consumers cannot apply a V2 result accidentally.

- A local Calibre record uses `book_key=calibre:<id>` and seals local format
  paths and hashes beneath the configured library root.
- A Content Server record uses
  `book_key=calibre-server:<source_fingerprint>:<id>`. Its snapshot includes a
  non-secret source descriptor and `source_revision_sha256`; formats are logical
  remote references, not paths on either host.
- Remote formats exist on local disk only in a private per-run scratch tree
  while each format is inspected. The persistent package retains hashes,
  provenance, and logical locators but not those temporary paths.
- The remote record revision and format set are re-read after inspection. Any
  difference produces terminal package state `source_changed` and prevents a
  normal identity decision.
- Content Server packages are evidence for review only and are structurally
  ineligible for the local supervised writer.

See [ADR-002](docs/decisions/ADR-002-exact-manifestation-v2.md) and
[ADR-004](docs/decisions/ADR-004-read-only-content-server-inventory.md).

---

## 1. Database Schema (SQLModel)

### Runs (`Run`)
One row per library scan or bulk audit operation.
- `run_id`: Unique identifier (e.g., `run_20260516_120000`).
- `status`: `started`, `completed`, `failed`.

### Book Records (`BookRecord`)
A snapshot of a book discovered in the library.
- `book_key`: Unique key (e.g., `calibre:123`).
- `files`: List of available formats and their paths.
- `current_metadata`: JSON blob of Calibre metadata.
- `status`: `scanned`, `needs_review`, `suggest_fix`, `applied`, `undone`.

### Evidence Packages (`EvidencePackage`)
The primary source of truth for an audit decision.
- `evidence_id`: Unique ID.
- `extracted`: Metadata found directly in the file (Heuristics/Tika).
- `candidates`: List of metadata results from external providers.
- `decision`: JSON verdict from the LLM Judge or Deterministic Resolver.

### Changes (`Change`)
Audit log for write operations.
- `backup_opf_path`: Path to the OPF file exported before application.
- `before_metadata`: Snapshot before change.
- `after_metadata`: Snapshot after change.

---

## 2. Evidence-First Models

The system uses a formal **Evidence Ladder** to resolve metadata conflicts.

### Evidence Source Types (`EvidenceSourceType`)
Priority (lower is better):
1.  `user_override`: 0
2.  `book_content`: 1 (Title page, Copyright page)
3.  `cover`: 2
4.  `embedded_metadata`: 3
5.  `current_calibre`: 4
6.  `external_provider`: 5
7.  `llm_inference`: 6
8.  `filename`: 7

### Resolved Field (`ResolvedField`)
Represents the outcome of the resolution engine for a single field (e.g., Title).
```json
{
  "field": "title",
  "selected_value": "The Great Gatsby",
  "selected_source": { "source_type": "book_content", "confidence": 95 },
  "confidence": 95,
  "requires_review": false,
  "risk_flags": []
}
```

---

## 3. LLM Judge Interface

The LLM Judge is provided with the full evidence package and must output a valid JSON response matching this schema:

| Property | Type | Description |
|---|---|---|
| `recommended_action` | enum | `no_change`, `suggest_fix`, `needs_review`, `defer` |
| `confidence` | int | 0-100 score of the overall match. |
| `proposed_patch` | object | The metadata fields to update. |
| `risk_flags` | list | `author_swap`, `isbn_conflict`, `edition_ambiguous`, `cover_mismatch`. |
| `reasons` | list | Human-readable explanation for the decision. |

---

## 4. Directory Structure

```text
.state/
  bookaudit.db      # SQLite system-of-record
.artifacts/
  covers/           # Extracted cover images
  backups/          # OPF backups for Reversible Apply
    calibre_1/
      before_20260516.opf
```

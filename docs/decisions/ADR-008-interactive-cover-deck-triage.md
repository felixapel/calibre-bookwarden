# ADR-008: Interactive Cover Deck Triage Workflow (HTMX + React)

## Status
Accepted

## Date
2026-08-28

## Context
Automatic cover replacement carries aesthetic subjectivity: an older original scan may have historical charm, while a modern high-resolution publisher reprint might look better to another user. Unattended bulk cover replacement risks operator dissatisfaction. Conversely, inspecting thousands of books one by one in desktop Calibre is painfully slow.

## Decision
Introduce a fast, keyboard-driven triage interface nicknamed **"Cover Deck"**:
- Implemented as a lightweight HTML/HTMX endpoint (`/api/covers/ui/deck`) and embedded into the React 19 SPA (`webui/src/pages/CoverStudio.tsx`).
- Queues books classified as `Tier C`, `Tier D`, or `spurious` by CQS.
- For each item, displays the current cover side-by-side with high-resolution candidates fetched on-demand from OpenLibrary / Hardcover.
- **Keyboard Ergonomics**:
  - `Left Arrow (←)`: Reject / Keep current cover.
  - `Right Arrow (→)`: Accept candidate cover and write atomic rollback record.
  - `Spacebar`: Fullscreen zoom inspection.
- When an upgrade is accepted, the server atomic writes the new cover, backs up the old cover to the restore directory, updates `has_cover=1`, and emits a hot-reload notification to Calibre-Web.

## Consequences
- Operators can triage and upgrade hundreds of degraded covers in minutes.
- Eliminates risk of unwanted automatic cover overwrites.
- Direct feedback loop with zero page reloads via HTMX swapping.

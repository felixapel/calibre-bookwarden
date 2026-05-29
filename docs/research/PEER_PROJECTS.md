# Peer Project Research

This document summarizes a deep technical audit of open-source projects operating in the metadata management, ebook toolkit, and AI orchestration space. Analyzing these peer projects helps contextualize `calibre-ai-auditor` and identifies proven architectural patterns we can adopt.

## 1. `ebk` (E-Book Toolkit)
**Repository**: `queelius/ebk`
**Language**: Python
**Focus**: Digital Resilience, Personal Archiving, AI Enrichment

### Core Architecture
*   **Hierarchical Resolution**: Uses a priority ladder (`Sidecar OPF > Embedded Metadata > Filename Heuristics`). This focuses heavily on archival durability.
*   **Knowledge Graph**: Moves beyond simple tag arrays by utilizing `NetworkX` to build semantic relationships and extract concepts from book content.
*   **Database Engine**: Relies on a clean SQLAlchemy/SQLite backend augmented with **FTS5** (Full-Text Search) for rapid local querying.

### Key Takeaway for Us
`ebk` focuses heavily on "Concept Extraction" for discovery. While powerful, `calibre-ai-auditor` differentiates itself by focusing on **Correction and Integrity** (fixing bad data) rather than just enrichment.

---

## 2. `paperless-gpt`
**Repository**: `icereed/paperless-gpt`
**Language**: Go
**Focus**: AI Sidecar for Paperless-ngx, OCR Enhancement

### Core Architecture
*   **"Match-Not-Hallucinate" Prompting**: Uses Go's `text/template` engine to dynamically inject *existing* system tags and custom fields into the LLM prompt. The LLM is instructed to map evidence to these known entities rather than inventing new ones.
*   **Robust Output Parsing**: Implements a dedicated "Cleaning" layer that strips LLM chatter and Markdown code blocks before unmarshaling the payload into strict internal structs.
*   **State-Machine Polling**: Avoids complex webhook setups by implementing a "Ticker Loop" that polls the host API for specific tags (e.g., `ai-process`), updating the tag upon completion.

### Key Takeaway for Us
The **Context Injection** strategy is highly relevant. When judging metadata, we should inject the user's existing Calibre tags/series into the prompt to encourage standardization.

---

## 3. `Librario`
**Repository**: `librario-dev/librario` (SourceHut)
**Language**: Go
**Focus**: Deterministic Metadata Aggregation and API Serving

### Core Architecture
*   **The "Merger" Engine**: A purely code-deterministic system without LLMs. It uses highly tuned heuristics to merge conflicting data from Google Books, ISBNDB, and Hardcover.
*   **Penalty Scoring**: Implements specific "Field Penalties". For example, titles containing `()` or `[]` are heavily penalized because they usually denote unwanted edition information (e.g., `[Special Edition]`).
*   **Image Dimension Scoring**: Cover candidates are downloaded in the background and scored based on resolution and aspect ratio, ensuring the highest quality asset is selected.

### Key Takeaway for Us
We should implement **Deterministic Penalties** (like stripping `(EPUB)` or `[1st Edition]`) *before* we query external providers like OpenLibrary, as clean search queries drastically improve candidate hit rates.

---

## 4. `BookReconciler`
**Repository**: `Post45-Data-Collective/BookReconciler`
**Language**: Python (often used with OpenRefine)
**Focus**: Academic Bibliographic Clustering

### Core Architecture
*   **Work vs. Edition Separation**: The core logic explicitly distinguishes between a "Work" (the abstract concept, e.g., *Dune*) and an "Edition" (the physical manifestation, e.g., *1965 Ace Paperback*).
*   **API Targeting**: Integrates deeply with authoritative academic APIs like OCLC/WorldCat and the Library of Congress.
*   **Fuzzy Deduplication**: Relies heavily on algorithms like Levenshtein distance for initial clustering before invoking heavy API lookups.

### Key Takeaway for Us
Our Qdrant vector database integration (planned for v0.6) should explicitly aim to solve the "Same Work / Different Edition" problem, which is a major pain point for users with large Calibre libraries.

---

## Strategic Summary: The `calibre-ai-auditor` Moat

Based on this ecosystem analysis, `calibre-ai-auditor` occupies a unique space:

1.  **The "Evidence Package" as a First-Class Citizen**: Unlike tools that "magically" update tags using AI, we surface the *proof* (e.g., a text snippet from the copyright page) alongside the LLM's reasoning in the WebUI.
2.  **Hybrid Resolution**: We combine the deterministic scoring techniques of `Librario` (for speed and safety) with the semantic reasoning of `paperless-gpt` (for complex conflicts).
3.  **High-Concurrency Architecture**: Utilizing sidecars (Valkey, Tika, Qdrant) makes our system robust enough for massive homelab libraries, scaling beyond simple single-threaded CLI scripts.
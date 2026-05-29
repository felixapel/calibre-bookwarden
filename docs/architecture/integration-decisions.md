# Integration Decisions

This document details the Architecture Decision Records (ADRs) for `calibre-ai-auditor`.

---

## ADR-001: Calibre Integration Layer

### Status
Accepted

### Context
`calibre-ai-auditor` needs to retrieve book metadata from Calibre libraries and write corrected metadata back. Calibre has an internal Python database engine (`new_api`) as well as a CLI interface (`calibredb`).
*   Directly importing Calibre's Python package requires running inside Calibre's custom python environment (`calibre-debug`), which creates complex packaging issues.
*   Running subprocess commands (`calibredb list`) is simple but introduces high execution latency (~200ms per call), making library-wide scans slow.

### Decision
We adopt a hybrid integration model:
1.  **Read Operations (Scans)**: Attempt to read using `calibre-debug` and Python direct imports first. If unavailable, fall back to parsing SQLite database records directly or executing CLI commands.
2.  **Write Operations (Applies)**: Always write back using the official CLI `calibredb set_metadata` commands. This ensures Calibre's database events, file names, and layout changes are synchronized safely without risk of database corruption.

### Consequences
*   **Safety**: Writing via the official CLI prevents file tree de-synchronization and library corruption.
*   **Performance**: Reading via direct database interfaces speeds up discovery scans of massive libraries.

---

## ADR-002: In-Memory Caching and Valkey Fallbacks

### Status
Accepted

### Context
Audits involve heavy embedding lookups and LLM completions. These external API calls are slow and can quickly exceed rate limits. While Valkey is the target production queue and cache server, forcing local CLI developers to spin up Valkey containers degrades developer ergonomics.

### Decision
We implement a unified caching interface that:
1.  Checks if Valkey is running. If active, uses Valkey for caching with strict Time-to-Live (TTL) key evictions.
2.  Falls back to a thread-safe, process-level in-memory RAM dictionary if Valkey is offline.

### Consequences
*   **Ergonomics**: Developers can run the CLI locally in offline/offline-mocked mode instantly without deploying Docker services.
*   **Resiliency**: Production deployments get shared worker caching, while local deployments stay lightweight.

---

## ADR-003: Native LLM Provider Adapters over LiteLLM proxy

### Status
Accepted

### Context
The original architecture proposed using LiteLLM to route tasks across local Ollama and remote OpenAI providers.
*   LiteLLM brings a heavy package footprint and complex dependency trees.
*   We require precise structured JSON output and vision handling (such as Gemini's native structured SDK features).

### Decision
We implement clean, native async provider clients (Ollama, OpenAI, LM Studio, and Google Gemini) directly in Python. 

### Consequences
*   **Control**: We can directly utilize provider-specific SDK features (e.g. Google Gemini's structured models and Vision input formats).
*   **Simplicity**: We reduce the project's dependency load, making Docker builds faster and image sizes smaller.

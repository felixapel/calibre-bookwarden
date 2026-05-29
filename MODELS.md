# Models and Routing

`calibre-ai-auditor` uses a direct provider abstraction (`LLMRouter`) to handle requests to both local and remote Large Language Models.

---

## 1. Routing Principles

- **Deterministic Before Model**: ISBN matches and exact Title/Author matches are resolved by the **Deterministic Resolver** before calling an LLM.
- **Privacy First**: Remote providers (OpenAI) are only sent data if `allow_remote_text` or `allow_remote_images` is enabled.
- **Capped Context**: Snippets sent to models are capped by `max_remote_chars` (default: 4000) to ensure low cost and high speed.
- **Local Priority**: If an Ollama instance is detected and responding, it can be used for utility tasks like normalization or ranking.

---

## 2. LLM Providers

### Ollama (Local)
- **Use Case**: Default provider for all homelab deployments.
- **Recommended Model**: `qwen3.5:9b-q4_K_M` or `llama3:8b`.
- **Capability**: Supports structured JSON output and Vision (with compatible models like `llava`).

### OpenAI (Remote)
- **Use Case**: Deep reasoning for complex metadata conflicts or high-accuracy vision checks.
- **Recommended Model**: `gpt-4o-mini`.
- **Capability**: Native structured output support and state-of-the-art vision.

### Google Gemini (Remote)
- **Use Case**: Native multi-modal processing and high-performance structured metadata auditing.
- **Recommended Model**: `gemini-2.5-flash` or `gemini-2.5-pro`.
- **Capability**: State-of-the-art native structured schema enforcement, vision cover inspection, and fast connection checks via the new `google-genai` SDK.

### LM Studio (Local)
- **Use Case**: Running local open-weight models with an OpenAI-compatible API interface.
- **Recommended Model**: `meta-llama-3-8b-instruct`.
- **Capability**: OpenAI compatible chat completions with fallback strict schema parsing capabilities.

---

## 3. The Judge Contract

The **Metadata Judge** (`src/calibre_ai_auditor/judge/engine.py`) provides the model with a structured "Evidence Package" containing:
1.  **Current Metadata**: What's currently in your library.
2.  **Extracted Evidence**: Snippets found in the file, bylines, and ISBNs.
3.  **Candidates**: Matching records from OpenLibrary/Google Books.

### Expected Output
The model MUST respond with valid JSON following the schema in `DATA_MODEL.md`. It must assign an **Action** (`suggest_fix`, `needs_review`, etc.) and provide **Reasons** for its choice.

---

## 4. Privacy Configuration

Routing is strictly governed by the `privacy` section of your configuration:

```yaml
privacy:
  allow_remote_text: false    # Snippets will be replaced with [HIDDEN] for remote models
  allow_remote_images: false  # Images will be stripped from remote model requests
  max_remote_chars: 4000      # Hard limit on text length sent to OpenAI
```

These filters are applied in real-time by the `LLMRouter` before any network request is initiated.

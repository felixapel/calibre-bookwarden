# Calibre Bookwarden WebUI

The frontend interface for **Calibre Bookwarden** is a modern Single Page Application (SPA) designed for rapid bibliographic inspection, cover triage, and evidence verification.

---

## 🛠️ Technology Stack

- **Framework**: React 19 with TypeScript
- **Bundler & Tooling**: Vite 6, Tailwind CSS, Lucide React icons
- **Dynamic Interactions**: Native React hooks + HTMX integration for the swipeable Cover Deck
- **Testing**: Playwright (E2E testing across desktop and mobile Chromium viewports)

---

## 📱 Core Views & Features

### 1. Bento Dashboard (`src/pages/Dashboard.tsx`)
- High-level overview of library health, active background workers, and persistent storage.
- Real-time connectivity status for homelab inference hosts (Ollama, LM Studio).
- Aggregate metrics: total books audited, Tier A/B/C breakdown, pending reviews, and disk savings.

### 2. Manifestation V2 Verification (`src/pages/Verify.tsx` & `Review.tsx`)
- Submits exact-edition audit runs (`pipeline: "v2"`) with granular OCR and witness model options.
- Detailed evidence inspector displaying:
  - Cryptographic SHA-256 package seal.
  - Per-format container extraction (EPUB OCF, PDF XMP, CBZ/CBR).
  - Exact external provider candidates (OpenLibrary, Google Books).
  - Canonical proposed patches with field-by-field diff views.
  - Single-click manual authorization for verified Tier A packages.

### 3. Cover Studio & Cover Deck (`src/pages/CoverStudio.tsx`)
- **Cover Deck (Swipeable Review)**: Fast, keyboard-driven triage of defective and low-resolution covers (powered by HTMX at `/api/covers/ui/deck`).
- **Mathematical CQS Inspector**: Displays Cover Quality Scores (0–100), Laplacian sharpness metrics, Shannon entropy, and aspect ratio adherence.
- **Side-by-Side Comparison**: Review current covers against candidate high-definition replacements before applying.

### 4. Curation & Library Diagnostics (`src/pages/Duplicates.tsx`, `Series.tsx`)
- **Series Gap Hunter**: Visualizes multi-volume series, identifying missing leading and intermediate volumes.
- **FRBR Multi-Format Consolidator**: Detects duplicate book entries across distinct formats (e.g. EPUB vs PDF) with safe merge actions.

### 5. Settings & Security (`src/pages/Settings.tsx`)
- Configures external metadata provider toggles, local API keys, and privacy boundaries.

---

## 🚀 Development & Build Scripts

From the `webui/` directory:

```bash
# Install dependencies
npm install

# Start local development server (proxies /api to http://localhost:8080)
npm run dev

# Run TypeScript type check and linter
npm run lint

# Build production assets (outputs to webui/dist/)
npm run build

# Run Playwright end-to-end tests
npx playwright test
```

---

## 🔒 Authentication & API Integration

The SPA communicates with the backend via `src/api/client.ts`:
- **Development**: Vite development server automatically proxies API requests (`/api/*`) to the backend running at `http://localhost:8080`.
- **Production**: When served from the compiled container image, static assets are hosted directly by the Bookwarden backend or reverse-proxied via Caddy Edge.
- **Security Headers**: API requests include the configured `X-API-Key` header when required. In enterprise Certificate A deployments, the entire site is protected by Caddy TLS and Basic Auth with bcrypt hashing.

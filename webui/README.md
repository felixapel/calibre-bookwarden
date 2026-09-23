# Bookwarden web UI

The production build is the Certificate A interface: **Overview**, **Verify**,
and **Review**. It requests bounded, read-only verification of a stopped Calibre
library and displays sealed evidence. The production API has no cover editor,
library writer, upload, or legacy curation endpoint.

The repository also retains experimental screens for development. They are
hidden from the default build and require `VITE_ENABLE_LEGACY_UI=true` at build
time. That flag does not add corresponding endpoints to the production API or
make those screens a supported deployment.

## Develop and verify

Use the locked Node version from [INSTALL.md](../INSTALL.md). From `webui/`:

```sh
npm ci
npm run lint
npm run build
npm run e2e
```

Vite's local development server proxies `/api` to `127.0.0.1:8080`. Production
serves the compiled assets from the Certificate A API behind the configured
same-host Caddy authentication and TLS. The API key stays in page memory; it is
cleared when the page reloads.

The root [README](../README.md) shows screenshots generated from this actual
production build with synthetic API responses. Regenerate them after `npm run
build` with:

```sh
node scripts/capture-readme-screenshots.mjs
```

Do not use a private library or production API for documentation screenshots.

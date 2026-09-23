// Capture the actual Certificate A UI with synthetic, deterministic API responses.
// Run `npm run build` first, then `node scripts/capture-readme-screenshots.mjs`.
import { chromium } from '@playwright/test'
import { preview } from 'vite'
import { existsSync, mkdirSync } from 'node:fs'
import { dirname, join, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

const webuiRoot = resolve(dirname(fileURLToPath(import.meta.url)), '..')
const assetRoot = resolve(webuiRoot, '..', 'docs', 'assets')
if (!existsSync(join(webuiRoot, 'dist', 'index.html'))) {
  throw new Error('Build the web UI before capturing documentation screenshots.')
}
mkdirSync(assetRoot, { recursive: true })

const run = {
  run_id: 'example-certificate-a-run',
  status: 'completed',
  started_at: '2026-09-01T10:00:00Z',
  finished_at: '2026-09-01T10:03:00Z',
  total: 3,
  completed: 3,
  counts: { shadowed: 2, review: 1 },
  error_code: null,
}

const server = await preview({
  root: webuiRoot,
  configFile: false,
  preview: { host: '127.0.0.1', port: 5176, strictPort: true },
})
let browser
try {
  browser = await chromium.launch({ headless: true })
  const page = await browser.newPage({ viewport: { width: 1440, height: 900 }, deviceScaleFactor: 1 })
  await page.route(/\/api\/health\/ready$/, route => route.fulfill({ json: { status: 'ready', certificate: 'A' } }))
  await page.route(/\/api\/capabilities$/, route => route.fulfill({
    json: {
      certificate: 'A', mode: 'shadow', pipeline: 'manifestation-v2',
      library_source: 'offline-folder', providers: ['google_books_isbn', 'openlibrary_isbn'],
      ocr: { enabled: true, backend: 'tesseract', max_pages: 12 }, writes_enabled: false,
    },
  }))
  await page.route(/\/api\/verify\/runs(?:\?.*)?$/, route => route.fulfill({
    json: { runs: [run], limit: 50, offset: 0 },
  }))
  await page.route(/\/api\/verify\/example-certificate-a-run$/, route => route.fulfill({
    json: {
      ...run,
      results: [
        { book_key: 'sample:001', state: 'shadowed', evidence_id: 'sample-evidence-001' },
        { book_key: 'sample:002', state: 'review', evidence_id: 'sample-evidence-002' },
        { book_key: 'sample:003', state: 'shadowed', evidence_id: 'sample-evidence-003' },
      ],
    },
  }))

  const base = 'http://127.0.0.1:5176'
  await page.goto(base)
  await page.getByText('google_books_isbn + openlibrary_isbn').waitFor()
  await page.screenshot({ path: join(assetRoot, 'certificate-a-overview.png'), animations: 'disabled', fullPage: true })

  await page.goto(`${base}/verify/example-certificate-a-run`)
  await page.getByRole('link', { name: 'Open sealed evidence' }).first().waitFor()
  await page.screenshot({ path: join(assetRoot, 'certificate-a-verify.png'), animations: 'disabled', fullPage: true })
  await page.close()
} finally {
  await browser?.close()
  await new Promise((resolveClose, rejectClose) => server.httpServer.close(error => error ? rejectClose(error) : resolveClose()))
}

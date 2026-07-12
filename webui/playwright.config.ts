import { defineConfig, devices } from '@playwright/test'
import { tmpdir } from 'node:os'
import { dirname, join, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

const __dirname = dirname(fileURLToPath(import.meta.url))

/**
 * Playwright config for calibre-ai-auditor v1.0 WebUI E2E.
 *
 * Strategy:
 *  - FastAPI serves BOTH the Vite-built static files AND the /api/* endpoints
 *  - One port (5174), no need for a separate Vite dev server
 *  - File-based SQLite DB in tmpdir (in-memory doesn't share across workers)
 *  - Read-only by default; per-test override for write tests
 */
const TEST_DB = join(tmpdir(), `bookaudit_e2e_${Date.now()}.db`)
const STATIC_DIR = resolve(__dirname, 'dist')
const PORT = 5174

export default defineConfig({
  testDir: './e2e',
  fullyParallel: true,
  forbidOnly: !!process.env.CI,
  retries: process.env.CI ? 2 : 0,
  workers: process.env.CI ? 1 : undefined,
  reporter: process.env.CI
    ? [['github'], ['html', { open: 'never', outputFolder: 'playwright-report' }]]
    : 'list',
  outputDir: './test-results/',

  use: {
    baseURL: `http://127.0.0.1:${PORT}`,
    trace: 'retain-on-failure',
    screenshot: 'only-on-failure',
    video: 'retain-on-failure',
    actionTimeout: 10_000,
    navigationTimeout: 15_000,
  },

  projects: [
    { name: 'chromium', use: { ...devices['Desktop Chrome'] } },
    {
      name: 'mobile-chromium',
      testMatch: /accessibility\.spec\.ts/,
      use: { ...devices['Pixel 7'] },
    },
  ],

  webServer: {
    command: `cd .. && source .venv/bin/activate && BOOKAUDIT_STATIC_DIR=${STATIC_DIR} python -m calibre_ai_auditor.cli.main web --port ${PORT} --host 127.0.0.1`,
    port: PORT,
    reuseExistingServer: !process.env.CI,
    timeout: 30_000,
    env: {
      BOOKAUDIT_DATABASE__BACKEND: 'sqlite',
      BOOKAUDIT_DB_PATH: TEST_DB,
      BOOKAUDIT_QUEUE__BACKEND: 'memory',
      BOOKAUDIT_READ_ONLY: 'true',
      BOOKAUDIT_LIBRARY_PATH: '/dev/null',
      BOOKAUDIT_LOG_LEVEL: 'WARNING',
    },
  },
})

import { expect, test, type Page } from '@playwright/test'

async function mockDashboard(page: Page) {
  await page.route(/\/api\/health\/ready$/, (route) => route.fulfill({ json: { status: 'ready', certificate: 'A' } }))
  await page.route(/\/api\/capabilities$/, (route) => route.fulfill({
    json: {
      certificate: 'A',
      mode: 'shadow',
      pipeline: 'manifestation-v2',
      library_source: 'offline-folder',
      providers: ['google_books_isbn', 'openlibrary_isbn'],
      ocr: { enabled: true, backend: 'tesseract', max_pages: 12 },
      writes_enabled: false,
    },
  }))
  await page.route(/\/api\/verify\/runs$/, (route) => route.fulfill({
    json: {
      runs: [{
        run_id: 'verify_certificate_a_001',
        status: 'completed',
        started_at: '2026-08-08T10:00:00Z',
        finished_at: '2026-08-08T10:03:00Z',
        total: 25,
        completed: 25,
        counts: { shadowed: 25 },
        error_code: null,
      }],
      limit: 50,
      offset: 0,
    },
  }))
}

test.beforeEach(async ({ page }) => mockDashboard(page))

test('presents the production Certificate A boundary', async ({ page }) => {
  await page.goto('/')
  await expect(page.getByRole('heading', { name: /Audit your Calibre metadata/ })).toBeVisible()
  await expect(page.getByText('Shadow only', { exact: true })).toBeVisible()
  await expect(page.getByText('Read-only', { exact: true })).toBeVisible()
  await expect(page.getByText('google_books_isbn + openlibrary_isbn', { exact: true })).toBeVisible()
  await expect(page.getByText('Writes', { exact: true })).toBeVisible()
  await expect(page.getByText('Disabled', { exact: true })).toBeVisible()
})

test('shows the stopped-library sequence and latest run', async ({ page }) => {
  await page.goto('/')
  await expect(page.getByText('Stop Calibre', { exact: true })).toBeVisible()
  await expect(page.getByText('verify_certificate_a_001')).toBeVisible()
  await page.getByRole('link', { name: 'Start a shadow audit' }).click()
  await expect(page).toHaveURL(/\/verify$/)
})

test('contains no quarantined production navigation', async ({ page }) => {
  await page.goto('/')
  for (const label of ['Command Center', '360° Forensic Audit', 'Cover Studio & Deck', 'Curation Guardian']) {
    await expect(page.getByRole('link', { name: label, exact: true })).toHaveCount(0)
  }
  await expect(page.getByRole('navigation', { name: 'Main Navigation' }).getByRole('link')).toHaveText([
    'Overview',
    'Verify',
    'Review',
  ])
})

test('redirects client-side navigation to unknown and retired routes back to Overview', async ({ page }) => {
  await page.goto('/')
  for (const path of ['/command-center', '/audit-360', '/covers', '/curation', '/unknown']) {
    await page.evaluate((nextPath) => {
      window.history.pushState({}, '', nextPath)
      window.dispatchEvent(new PopStateEvent('popstate'))
    }, path)
    await expect(page).toHaveURL(/\/$/)
    await expect(page.getByRole('heading', { name: /Audit your Calibre metadata/ })).toBeVisible()
  }
})

test('loads no third-party browser resources', async ({ page }) => {
  const thirdPartyRequests: string[] = []
  page.on('request', (request) => {
    const url = new URL(request.url())
    if (url.origin !== 'http://127.0.0.1:5174') thirdPartyRequests.push(url.href)
  })

  await page.goto('/')
  await expect(page.getByRole('heading', { level: 1 })).toBeVisible()

  expect(thirdPartyRequests).toEqual([])
})

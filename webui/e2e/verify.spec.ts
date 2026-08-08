import { expect, test, type Page } from '@playwright/test'

const run = {
  run_id: 'verify_certificate_a_001',
  status: 'running',
  started_at: '2026-08-08T10:00:00Z',
  finished_at: null,
  total: 100,
  completed: 42,
  counts: { shadowed: 35, review: 7 },
  error_code: null,
}

async function mockShell(page: Page) {
  await page.route(/\/api\/health\/ready$/, (route) => route.fulfill({ json: { status: 'ready', certificate: 'A' } }))
  await page.route(/\/api\/verify\/runs$/, (route) => route.fulfill({ json: { runs: [], limit: 50, offset: 0 } }))
}

test.beforeEach(async ({ page }) => mockShell(page))

test('requires an explicit stopped-Calibre confirmation', async ({ page }) => {
  await page.goto('/verify')
  const request = page.getByRole('button', { name: 'Request shadow audit' })
  await expect(request).toBeDisabled()
  await page.getByRole('checkbox', { name: /I confirm Calibre/ }).check()
  await expect(request).toBeEnabled()
  await page.getByLabel('Maximum books').fill('10001')
  await expect(request).toBeDisabled()
  await expect(page.getByText('Enter a whole number from 1 to 10,000.')).toBeVisible()
})

test('submits only the strict request contract and an idempotency key', async ({ page }) => {
  let body: unknown
  let idempotencyKey = ''
  await page.route(/\/api\/verify$/, async (route) => {
    body = route.request().postDataJSON()
    idempotencyKey = route.request().headers()['idempotency-key'] ?? ''
    await route.fulfill({ status: 202, json: run })
  })
  await page.route(/\/api\/verify\/verify_certificate_a_001$/, (route) => route.fulfill({ json: { ...run, results: [] } }))

  await page.goto('/verify')
  await page.getByLabel('Maximum books').fill('25')
  await page.getByRole('checkbox', { name: /Bounded Tesseract OCR/ }).uncheck()
  await page.getByRole('checkbox', { name: /I confirm Calibre/ }).check()
  await page.getByRole('button', { name: 'Request shadow audit' }).click()

  await expect.poll(() => body).toEqual({ limit: 25, use_ocr: false, confirm_calibre_stopped: true })
  expect(idempotencyKey).toMatch(/^certificate-a:[0-9a-f-]{36}$/)
  await expect(page).toHaveURL(/\/verify\/verify_certificate_a_001$/)
})

test('shows inventory uncertainty, progress, and sealed evidence links', async ({ page }) => {
  await page.route(/\/api\/verify\/verify_certificate_a_001$/, (route) => route.fulfill({
    json: {
      ...run,
      total: null,
      completed: 3,
      results: [{ book_key: 'calibre:7', state: 'shadowed', evidence_id: 'evidence-7' }],
    },
  }))
  await page.goto('/verify/verify_certificate_a_001')
  await expect(page.getByText('3 processed · inventory pending')).toBeVisible()
  await expect(page.getByRole('link', { name: 'Open sealed evidence' })).toHaveAttribute('href', '/review/evidence-7')
})

test('requests cancellation without claiming it is immediate', async ({ page }) => {
  let cancelled = false
  await page.route(/\/api\/verify\/verify_certificate_a_001$/, (route) => route.fulfill({ json: { ...run, results: [] } }))
  await page.route(/\/api\/verify\/verify_certificate_a_001\/cancel$/, async (route) => {
    cancelled = route.request().method() === 'POST'
    await route.fulfill({ status: 202, json: { ...run, status: 'cancelling' } })
  })
  await page.goto('/verify/verify_certificate_a_001')
  await page.getByRole('button', { name: 'Request cancellation' }).click()
  await expect.poll(() => cancelled).toBe(true)
  await expect(page.getByText(/stop at a safe boundary/)).toBeVisible()
})

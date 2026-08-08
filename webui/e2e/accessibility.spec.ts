import AxeBuilder from '@axe-core/playwright'
import { expect, test } from '@playwright/test'

test.beforeEach(async ({ page }) => {
  await page.route(/\/api\/health\/ready$/, (route) => route.fulfill({ json: { status: 'ready', certificate: 'A' } }))
  await page.route(/\/api\/capabilities$/, (route) => route.fulfill({ json: { certificate: 'A', mode: 'shadow', pipeline: 'manifestation-v2', library_source: 'offline-folder', providers: ['google_books_isbn', 'openlibrary_isbn'], ocr: { enabled: true, backend: 'tesseract', max_pages: 12 }, writes_enabled: false } }))
  await page.route(/\/api\/verify\/runs$/, (route) => route.fulfill({ json: { runs: [], limit: 50, offset: 0 } }))
})

test('dashboard has no serious accessibility violations or horizontal overflow', async ({ page }) => {
  await page.goto('/')
  await expect(page.getByRole('heading', { level: 1 })).toBeVisible()

  const results = await new AxeBuilder({ page }).analyze()
  const serious = results.violations.filter(({ impact }) => impact === 'serious' || impact === 'critical')
  expect(serious, JSON.stringify(serious, null, 2)).toEqual([])

  const dimensions = await page.evaluate(() => ({
    clientWidth: document.documentElement.clientWidth,
    scrollWidth: document.documentElement.scrollWidth,
  }))
  expect(dimensions.scrollWidth).toBeLessThanOrEqual(dimensions.clientWidth)
})

test('dashboard stays within browser performance budgets', async ({ page }) => {
  await page.goto('/')
  await expect(page.getByRole('heading', { level: 1 })).toBeVisible()
  const metrics = await page.evaluate(() => {
    const navigation = performance.getEntriesByType('navigation')[0] as PerformanceNavigationTiming
    const scriptTransferBytes = performance.getEntriesByType('resource')
      .filter((entry) => entry.name.includes('/assets/') && entry.name.endsWith('.js'))
      .reduce((total, entry) => total + (entry as PerformanceResourceTiming).encodedBodySize, 0)
    return { domContentLoaded: navigation.domContentLoadedEventEnd, scriptTransferBytes }
  })
  expect(metrics.domContentLoaded).toBeLessThanOrEqual(2_500)
  expect(metrics.scriptTransferBytes).toBeLessThanOrEqual(350_000)
})

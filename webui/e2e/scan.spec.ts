import { test, expect } from '@playwright/test'
import { mockApi } from './helpers/api-mock'
import { setApiKey } from './helpers/auth'

test.describe('Scan page', () => {
  test.beforeEach(async ({ page }) => {
    await setApiKey(page)
    await mockApi(page, /\/api\/config/, {
      get: { body: { status: 'success', data: { library: { path: '/library', read_only: true } } } },
    })
  })

  test('renders the Scan page without errors', async ({ page }) => {
    await page.goto('/scan')
    await expect(page.locator('body')).toBeVisible()
    // Look for typical scan-related text
    const bodyText = await page.locator('body').textContent()
    expect(bodyText?.toLowerCase()).toMatch(/scan|library|calibre/i)
  })

  test('Limit input accepts a number', async ({ page }) => {
    await page.goto('/scan')
    const limitInput = page.locator('input[type="number"]')
    await expect(limitInput).toBeVisible()
    await limitInput.fill('10')
    await expect(limitInput).toHaveValue('10')
  })

  test('Start Scan button submits to /api/runs/scan', async ({ page }) => {
    let scanCalled = false
    await page.route(/\/api\/runs\/scan/, async (route) => {
      scanCalled = true
      await route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({ status: 'success', data: { run_id: 'run_20260705_120000' } }),
      })
    })
    await page.goto('/scan')
    await page.click('button:has-text("Start Scan")')
    await expect.poll(() => scanCalled).toBe(true)
  })
})
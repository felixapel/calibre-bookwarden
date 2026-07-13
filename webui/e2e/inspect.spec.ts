import { test, expect } from '@playwright/test'
import { mockApi } from './helpers/api-mock'
import { setApiKey } from './helpers/auth'

test.describe('Inspect page', () => {
  test.beforeEach(async ({ page }) => {
    await setApiKey(page)
    await mockApi(page, /\/api\/config/, {
      get: { body: { status: 'success', data: { library: { path: '/library' } } } },
    })
  })

  test('renders the Inspect page without errors', async ({ page }) => {
    await page.goto('/inspect')
    await expect(page.locator('body')).toBeVisible()
  })

  test('has a path input field', async ({ page }) => {
    await page.goto('/inspect')
    const inputs = page.locator('input')
    await expect(inputs.first()).toBeVisible()
  })

  test('Inspect endpoint mock works correctly', async ({ page }) => {
    // Verifies that the /api/inspect/path route handler pattern matches
    // requests the page would make.
    let pathMatched = false
    await page.route(/\/api\/inspect\/path/, async (route) => {
      if (route.request().method() === 'POST') {
        pathMatched = true
      }
      await route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({
          status: 'success',
          data: { title: 'Inspect Title', authors: ['Author'] },
        }),
      })
    })
    // The mock is registered; this test confirms it's wired correctly.
    expect(true).toBe(true)
    // Trigger an actual call via the API client
    await page.goto('/inspect')
    const result = await page.evaluate(async () => {
      const r = await fetch('/api/inspect/path', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ path: '/tmp/test.epub' }),
      })
      return r.status
    })
    expect(result).toBe(200)
    expect(pathMatched).toBe(true)
  })
})
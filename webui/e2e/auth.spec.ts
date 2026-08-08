import { expect, test } from '@playwright/test'

test('an API key is kept in memory and retries unauthorized requests', async ({ page }) => {
  let authenticatedReviewRequests = 0
  await page.route(/\/api\//, async (route) => {
    const authenticated = route.request().headers()['x-api-key'] === 'production-test-key-with-32-characters'
    if (!authenticated) {
      await route.fulfill({ status: 401, json: { detail: 'Unauthorized' } })
      return
    }
    const url = route.request().url()
    if (url.includes('/api/review/v2?')) authenticatedReviewRequests += 1
    if (url.endsWith('/api/health/ready')) {
      await route.fulfill({ json: { status: 'ready', certificate: 'A' } })
    } else {
      await route.fulfill({ json: { status: 'success', data: [], meta: { total: 0, limit: 50, offset: 0 } } })
    }
  })

  await page.goto('/review')
  await expect(page.getByRole('dialog', { name: 'API key required' })).toBeVisible()
  await page.getByRole('textbox', { name: 'API key', exact: true }).fill('production-test-key-with-32-characters')
  await page.getByRole('button', { name: 'Save and retry' }).click()

  await expect.poll(() => authenticatedReviewRequests).toBeGreaterThan(0)
  await expect(page.getByRole('dialog', { name: 'API key required' })).toBeHidden()
  expect(await page.evaluate(() => localStorage.length)).toBe(0)
})

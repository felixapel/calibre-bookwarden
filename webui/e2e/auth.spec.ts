import { expect, test } from '@playwright/test'

test('loading an API key retries failed authenticated queries', async ({ page }) => {
  let authenticatedReviewRequests = 0
  await page.route(/\/api\//, async (route) => {
    const authenticated = route.request().headers()['x-api-key'] === 'production-test-key-with-32-characters'
    if (!authenticated) {
      await route.fulfill({ status: 401, contentType: 'application/json', body: JSON.stringify({ detail: 'Unauthorized' }) })
      return
    }
    if (route.request().url().includes('/api/review/v2?')) authenticatedReviewRequests += 1
    await route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({ status: 'success', data: [], meta: { total: 0, limit: 50, offset: 0 } }),
    })
  })

  await page.goto('/review')
  await expect(page.getByRole('dialog', { name: 'API Key Authentication' })).toBeVisible()
  await page.getByRole('textbox', { name: 'API key', exact: true }).fill('production-test-key-with-32-characters')
  await page.getByRole('button', { name: 'Save and retry' }).click()

  await expect.poll(() => authenticatedReviewRequests).toBeGreaterThan(0)
  await expect(page.getByRole('dialog', { name: 'API Key Authentication' })).toBeHidden()
})

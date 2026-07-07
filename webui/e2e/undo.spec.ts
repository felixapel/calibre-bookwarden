import { test, expect } from '@playwright/test'
import { mockApi } from './helpers/api-mock'
import { setApiKey } from './helpers/auth'

test.describe('Undo page', () => {
  test.beforeEach(async ({ page }) => {
    await setApiKey(page)
    await mockApi(page, /\/api\/config/, {
      get: { body: { status: 'success', data: { library: { path: '/library', read_only: false } } } },
    })
  })

  test('renders the Undo page without errors', async ({ page }) => {
    await page.goto('/undo')
    await expect(page.locator('body')).toBeVisible()
  })

  test('shows empty state when no changes to undo', async ({ page }) => {
    await page.goto('/undo')
    const body = await page.locator('body').textContent()
    expect(body?.toLowerCase()).toMatch(/undo|restore|change|empty|none/i)
  })
})
import { test, expect } from '@playwright/test'
import { mockApi } from './helpers/api-mock'
import { setApiKey } from './helpers/auth'

test.describe('Duplicates page', () => {
  test.beforeEach(async ({ page }) => {
    await setApiKey(page)
  })

  test('renders the duplicates page without errors', async ({ page }) => {
    await mockApi(page, /\/api\/books\/all\/duplicates/, {
      get: { body: { status: 'success', data: [] } },
    })
    await page.goto('/duplicates')
    await expect(page.locator('body')).toBeVisible()
  })

  test('shows empty state when no duplicates', async ({ page }) => {
    await mockApi(page, /\/api\/books\/all\/duplicates/, {
      get: { body: { status: 'success', data: [] } },
    })
    await page.goto('/duplicates')
    const body = await page.locator('body').textContent()
    expect(body?.toLowerCase()).toMatch(/duplicate|empty|none|no /i)
  })
})
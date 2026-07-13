import { test, expect } from '@playwright/test'
import { mockApi } from './helpers/api-mock'
import { setApiKey } from './helpers/auth'

test.describe('Settings page', () => {
  test.beforeEach(async ({ page }) => {
    await setApiKey(page)
    await mockApi(page, /\/api\/config/, {
      get: {
        body: {
          status: 'success',
          data: {
            mutable: true,
            config: {
            library: { path: '/library', read_only: true },
            ollama_enabled: true,
            ollama_base_url: 'http://localhost:11434',
            judge_model: 'qwen3:8b',
            vision_model: 'qwen2.5vl:7b',
            privacy: { allow_remote_text: false, allow_remote_images: false },
            },
          },
        },
      },
    })
  })

  test('loads and displays config sections', async ({ page }) => {
    await page.goto('/settings')
    await expect(page.locator('text=Settings').first()).toBeVisible()
  })

  test('Save button submits to POST /api/config', async ({ page }) => {
    let savedConfig: unknown = null
    await page.route(/\/api\/config/, async (route) => {
      if (route.request().method() === 'POST') {
        savedConfig = JSON.parse(route.request().postData() ?? '{}')
        await route.fulfill({
          status: 200,
          contentType: 'application/json',
          body: JSON.stringify({ status: 'success', data: { message: 'saved' } }),
        })
      } else {
        await route.continue()
      }
    })
    await page.goto('/settings')
    await page.click('button:has-text("Save")')
    await expect.poll(() => savedConfig !== null).toBe(true)
  })

  test('displays local API key field', async ({ page }) => {
    await page.goto('/settings')
    await expect(page.locator('input[placeholder*="API" i], input[placeholder*="key" i]').first()).toBeVisible()
  })
})

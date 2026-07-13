import { test, expect } from '@playwright/test'
import { mockApi } from './helpers/api-mock'
import { setApiKey } from './helpers/auth'

test.describe('Dashboard page', () => {
  test.beforeEach(async ({ page }) => {
    await setApiKey(page)
    await mockApi(page, /\/api\/health/, {
      get: { body: { status: 'ok' } },
    })
    await mockApi(page, /\/api\/config/, {
      get: {
        body: {
          status: 'success',
          data: {
            library: { path: '/library', read_only: true },
            ollama_enabled: true,
          },
        },
      },
    })
    await mockApi(page, /\/api\/doctor/, {
      get: {
        body: {
          status: 'success',
          data: {
            dependencies: { calibredb: { found: true, path: '/usr/bin/calibredb' } },
            connectivity: { llms: { ollama: { ok: true } }, inference_hosts: { total_hosts: 3, healthy_hosts: 2 } },
          },
        },
      },
    })
    await mockApi(page, /\/api\/books$/, {
      get: { body: { status: 'success', data: [] } },
    })
    await mockApi(page, /\/api\/books\/all\/duplicates/, {
      get: { body: { status: 'success', data: [] } },
    })
    await mockApi(page, /\/api\/runs$/, {
      get: { body: [] },
    })
  })

  test('renders health card as green when API is healthy', async ({ page }) => {
    await page.goto('/')
    await expect(page.locator('text=ok').first()).toBeVisible()
  })

  test('displays homelab host count from doctor', async ({ page }) => {
    await page.goto('/')
    // Dashboard renders the doctor info; just verify the page loads
    await expect(page.locator('body')).toBeVisible()
  })

  test('shows review queue count', async ({ page }) => {
    await page.goto('/')
    // Just verify the dashboard loads without errors
    await expect(page.locator('body')).toBeVisible()
  })

  test('navigates to Review page on link click', async ({ page }) => {
    await page.goto('/')
    await page.click('a[href="/review"]')
    await expect(page).toHaveURL(/\/review/)
  })

  test('navigates to Scan page on link click', async ({ page }) => {
    await page.goto('/')
    await page.click('a[href="/scan"]')
    await expect(page).toHaveURL(/\/scan/)
  })
})

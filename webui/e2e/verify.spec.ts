import { test, expect } from '@playwright/test'
import { mockApi } from './helpers/api-mock'
import { setApiKey } from './helpers/auth'

test.describe('Manifestation V2 Verify page', () => {
  test.beforeEach(async ({ page }) => {
    await setApiKey(page)
    await mockApi(page, /\/api\/config/, {
      get: { body: { status: 'success', data: { library: { path: '/library', read_only: false } } } },
    })
    // Default empty list of runs
    await mockApi(page, /\/api\/verify\/runs/, {
      get: { body: { status: 'success', data: { runs: [] } } },
    })
  })

  test('renders the Verify page with empty state', async ({ page }) => {
    await page.goto('/verify')
    await expect(page.locator('text=Verify Library').first()).toBeVisible()
    await expect(page.getByText(/Manifestation V2/).first()).toBeVisible()
    await expect(page.locator('text=No verify runs yet')).toBeVisible()
  })

  test('has a Limit input', async ({ page }) => {
    await page.goto('/verify')
    const limitInput = page.locator('input[type="number"]')
    await expect(limitInput).toBeVisible()
    await limitInput.fill('25')
    await expect(limitInput).toHaveValue('25')
  })

  test('has Use LLM witness checkbox', async ({ page }) => {
    await page.goto('/verify')
    const checkbox = page.locator('input[type="checkbox"]')
    await expect(checkbox).toBeVisible()
  })

  test('Run Manifestation V2 button submits the explicit safe contract', async ({ page }) => {
    let verifyCalled = false
    let requestedLimit: number | null = null
    let requestedUseLlm = false
    let requestedPipeline: string | null = null
    let requestedUseOcr = false
    await page.route(/\/api\/verify/, async (route) => {
      if (route.request().method() === 'POST') {
        verifyCalled = true
        try {
          const body = JSON.parse(route.request().postData() ?? '{}')
          requestedLimit = body.limit ?? null
          requestedUseLlm = body.use_llm ?? false
          requestedPipeline = body.pipeline ?? null
          requestedUseOcr = body.use_ocr ?? false
        } catch {}
        await route.fulfill({
          status: 200,
          contentType: 'application/json',
          body: JSON.stringify({
            status: 'success',
            data: {
              run_id: 'verify_test_001',
              started_at: '2026-07-05T12:00:00+00:00',
              total: 10,
              status: 'running',
            },
          }),
        })
      } else {
        await route.continue()
      }
    })
    await page.goto('/verify')
    await page.locator('input[type="number"]').fill('10')
    await page.click('button:has-text("Run Manifestation V2")')
    await expect.poll(() => verifyCalled).toBe(true)
    expect(requestedLimit).toBe(10)
    expect(requestedUseLlm).toBe(false)
    expect(requestedPipeline).toBe('v2')
    expect(requestedUseOcr).toBe(true)
  })

  test('shows live run progress with action counters when run is active', async ({ page }) => {
    // Mock POST to return a run_id
    await page.route(/\/api\/verify$/, async (route) => {
      if (route.request().method() === 'POST') {
        await route.fulfill({
          status: 200,
          contentType: 'application/json',
          body: JSON.stringify({
            status: 'success',
            data: {
              run_id: 'verify_active_001',
              started_at: '2026-07-05T12:00:00+00:00',
              total: 100,
              status: 'running',
            },
          }),
        })
      } else {
        await route.continue()
      }
    })
    // Mock the run detail endpoint to return in-progress state
    await page.route(/\/api\/verify\/verify_active_001/, async (route) => {
      await route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({
          status: 'success',
          data: {
            run_id: 'verify_active_001',
            status: 'running',
            started_at: '2026-07-05T12:00:00+00:00',
            finished_at: null,
            total: 100,
            completed: 42,
            counts: {
              shadowed: 25,
              review: 12,
              deferred: 4,
              failed: 1,
            },
            verdicts: [],
          },
        }),
      })
    })
    await page.goto('/verify')
    await page.click('button:has-text("Run Manifestation V2")')
    await expect(page.locator('text=verify_active_001')).toBeVisible()
    await expect(page.getByText('Tier A shadowed', { exact: true })).toBeVisible()
    await expect(page.locator('text=25').first()).toBeVisible()
    await expect(page.locator('text=42/100')).toBeVisible()
  })

  test('lists recent runs in the runs section', async ({ page }) => {
    await mockApi(page, /\/api\/verify\/runs/, {
      get: {
        body: {
          status: 'success',
          data: {
            runs: [
              {
                run_id: 'verify_completed_001',
                status: 'completed',
                started_at: '2026-07-05T11:00:00+00:00',
                finished_at: '2026-07-05T11:05:00+00:00',
                total: 50,
                completed: 50,
                pipeline_version: 'manifestation-v2',
                mode: 'shadow',
                counts: { shadowed: 40, review: 8, deferred: 2, failed: 0 },
              },
              {
                run_id: 'verify_running_001',
                status: 'running',
                started_at: '2026-07-05T12:00:00+00:00',
                finished_at: null,
                total: 100,
                completed: 25,
                pipeline_version: 'manifestation-v2',
                mode: 'shadow',
                counts: { shadowed: 18, review: 5, deferred: 2, failed: 0 },
              },
            ],
          },
        },
      },
    })
    await page.goto('/verify')
    await expect(page.locator('text=verify_completed_001')).toBeVisible()
    await expect(page.locator('text=verify_running_001')).toBeVisible()
    await expect(page.locator('text=25/100').first()).toBeVisible()
    await expect(page.locator('text=50/50').first()).toBeVisible()
  })
})

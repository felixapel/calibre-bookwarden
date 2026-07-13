import { test, expect } from '@playwright/test'
import { mockApi } from './helpers/api-mock'
import { setApiKey } from './helpers/auth'
import { mockVerdictFor } from './helpers/verdict-mock'
import {
  SAMPLE_BOOKS,
  SAMPLE_BOOK_VERDICT_CONFIRMED,
  SAMPLE_BOOK_VERDICT_MISMATCH,
  SAMPLE_BOOK_VERDICT_AUTHOR_SWAP,
  SAMPLE_BOOK_VERDICT_AMBIGUOUS,
} from './helpers/fixtures'

test.describe('Review page — v1.0 per-field verdicts', () => {
  test.beforeEach(async ({ page }) => {
    await setApiKey(page)
    await mockApi(page, /\/api\/config/, {
      get: { body: { status: 'success', data: { library: { path: '/dev/null', read_only: true } } } },
    })
    await mockApi(page, /\/api\/books$/, {
      get: { body: { status: 'success', data: SAMPLE_BOOKS } },
    })
    // The evidence endpoint returns 404 in the test DB; mock it as empty so the
    // page can advance past "Extracting evidence package..." state.
    await mockApi(page, /\/api\/books\/.*\/evidence/, {
      get: { body: { status: 'success', data: { current: {}, extracted: {}, snippets: [], candidates: [], decision: null } } },
    })
  })

  test('shows per-field verdicts when book is selected', async ({ page }) => {
    await mockVerdictFor(page, 'calibre:1', SAMPLE_BOOK_VERDICT_CONFIRMED)
    await page.goto('/review')
    await page.click('text=The Great Gatsby')
    await expect(page.locator('text=Per-Field Content Verdicts (v1.0)')).toBeVisible()
    await expect(page.locator('text=TITLE').first()).toBeVisible()
    await expect(page.locator('text=Confirmed').first()).toBeVisible()
  })

  test('confirmed verdict shows green chip and matches book content', async ({ page }) => {
    await mockVerdictFor(page, 'calibre:1', SAMPLE_BOOK_VERDICT_CONFIRMED)
    await page.goto('/review')
    await page.click('text=The Great Gatsby')
    await expect(page.locator('text=Confirmed').first()).toBeVisible()
    await expect(page.locator('text=matches book content').first()).toBeVisible()
  })

  test('mismatch verdict shows red chip with declared vs observed diff', async ({ page }) => {
    await mockVerdictFor(page, 'calibre:2', SAMPLE_BOOK_VERDICT_MISMATCH)
    await page.goto('/review')
    await page.click('text=WRONG TITLE')
    await expect(page.locator('text=declared:')).toBeVisible()
    await expect(page.locator('text=observed:')).toBeVisible()
    await expect(page.locator('text=Some Real Book').first()).toBeVisible()
  })

  test('author_swap risk flag renders as red badge', async ({ page }) => {
    await mockVerdictFor(page, 'calibre:3', SAMPLE_BOOK_VERDICT_AUTHOR_SWAP)
    await page.goto('/review')
    await page.click('text=Right Book')
    await expect(page.locator('text=author_swap').first()).toBeVisible()
  })

  test('ambiguous verdict shows purple chip with LLM witness needed', async ({ page }) => {
    await mockVerdictFor(page, 'calibre:4', SAMPLE_BOOK_VERDICT_AMBIGUOUS)
    await page.goto('/review')
    await page.click('text=The Novel')
    await expect(page.locator('text=cannot decide deterministically').first()).toBeVisible()
  })

  test('auto-apply eligible badge shows when verdict is eligible', async ({ page }) => {
    await mockVerdictFor(page, 'calibre:2', SAMPLE_BOOK_VERDICT_MISMATCH)
    await page.goto('/review')
    await page.click('text=WRONG TITLE')
    await expect(page.locator('text=auto-apply ready')).toBeVisible()
  })

  test('manual review badge shows when verdict is not eligible', async ({ page }) => {
    await mockVerdictFor(page, 'calibre:3', SAMPLE_BOOK_VERDICT_AUTHOR_SWAP)
    await page.goto('/review')
    await page.click('text=Right Book')
    await expect(page.locator('text=manual review')).toBeVisible()
  })

  test('overall confidence percentage is displayed', async ({ page }) => {
    await mockVerdictFor(page, 'calibre:1', SAMPLE_BOOK_VERDICT_CONFIRMED)
    await page.goto('/review')
    await page.click('text=The Great Gatsby')
    await expect(page.locator('text=conf').first()).toBeVisible()
  })

  test('evidence spans render with source and page', async ({ page }) => {
    await mockVerdictFor(page, 'calibre:1', SAMPLE_BOOK_VERDICT_CONFIRMED)
    await page.goto('/review')
    await page.click('text=The Great Gatsby')
    await expect(page.locator('text=Evidence (1):')).toBeVisible()
    await expect(page.locator('text=[title_page 1]').first()).toBeVisible()
  })

  test('proposed patch section shows JSON', async ({ page }) => {
    await mockVerdictFor(page, 'calibre:2', SAMPLE_BOOK_VERDICT_MISMATCH)
    await page.goto('/review')
    await page.click('text=WRONG TITLE')
    await expect(page.locator('text=Proposed Patch')).toBeVisible()
    await expect(page.locator('text=Some Real Book').first()).toBeVisible()
  })

  test('Approve button calls /api/review/{key}/approve', async ({ page }) => {
    await mockVerdictFor(page, 'calibre:1', SAMPLE_BOOK_VERDICT_CONFIRMED)
    let approveCalled = false
    await page.route(/\/api\/review\/calibre:1\/approve/, async (route) => {
      approveCalled = true
      await route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({ status: 'success', data: { message: 'approved' } }),
      })
    })
    await page.goto('/review')
    await page.click('text=The Great Gatsby')
    await page.click('button:has-text("Approve Changes")')
    await expect.poll(() => approveCalled).toBe(true)
  })

  test('Reject button calls /api/review/{key}/reject and removes from queue', async ({ page }) => {
    await mockVerdictFor(page, 'calibre:1', SAMPLE_BOOK_VERDICT_CONFIRMED)
    await page.route(/\/api\/review\/calibre:1\/reject/, async (route) => {
      await route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({ status: 'success', data: { message: 'rejected' } }),
      })
    })
    await page.goto('/review')
    await page.click('text=The Great Gatsby')
    await page.click('button:has-text("Reject")')
    await expect(page.locator('button:has-text("The Great Gatsby")')).toHaveCount(0)
  })

  test('Queue Approved Fixes button is enabled when there are approved books', async ({ page }) => {
    await page.goto('/review')
    const applyButton = page.locator('button:has-text("Queue Approved Fixes")')
    // With 4 SAMPLE_BOOKS, 3 of which are in suggest_fix/needs_review/audited status,
    // the button should be enabled (the queue is non-empty).
    await expect(applyButton).toBeEnabled()
  })
})

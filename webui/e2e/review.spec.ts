import { expect, test } from '@playwright/test'
import { setApiKey } from './helpers/auth'

const tierASummary = {
  evidence_id: 'evidence-tier-a',
  run_id: 'run-v2',
  book_key: 'calibre:1',
  created_at: '2026-07-14T12:00:00Z',
  state: 'shadowed',
  tier: 'A',
  current_metadata: { title: 'Old title', authors: ['Current Author'] },
  manifestation_ids: { isbn: '9780306406157' },
  patch_fields: ['title', 'publisher'],
  risk_flags: [],
}

const tierBSummary = {
  evidence_id: 'evidence-tier-b',
  run_id: 'run-v2',
  book_key: 'calibre:2',
  created_at: '2026-07-14T12:01:00Z',
  state: 'review',
  tier: 'B',
  current_metadata: { title: 'Unresolved edition', authors: ['Unknown Author'] },
  manifestation_ids: {},
  patch_fields: [],
  risk_flags: ['manifestation_unresolved'],
}

const detailFor = (summary: typeof tierASummary | typeof tierBSummary) => ({
  status: 'success',
  data: {
    package: {
      schema_version: 2,
      policy_version: 'manifestation-v2',
      evidence_id: summary.evidence_id,
      run_id: summary.run_id,
      book_key: summary.book_key,
      created_at: summary.created_at,
      state: summary.state,
      snapshot: {
        book_key: summary.book_key,
        calibre_book_id: summary.book_key === 'calibre:1' ? 1 : 2,
        current_metadata: summary.current_metadata,
        files: [`/library/${summary.book_key}.epub`],
        library_root: '/library',
        snapshot_sha256: '1'.repeat(64),
      },
      formats: [
        {
          path: `/library/${summary.book_key}.epub`,
          format: 'EPUB',
          sha256: '2'.repeat(64),
          status: 'readable',
          identifiers: summary.manifestation_ids,
          title: summary.current_metadata.title,
          authors: summary.current_metadata.authors,
          languages: ['en'],
          evidence_ids: ['source-title'],
          error: null,
        },
      ],
      source_evidence: [
        {
          evidence_id: 'source-title',
          root_id: 'ebook-content',
          independence_root: null,
          source_kind: 'content_native',
          field: 'title',
          value: 'Exact title',
          manifestation_ids: summary.manifestation_ids,
          locator: 'EPUB title page',
          artifact_sha256: '3'.repeat(64),
          source_url: null,
          authoritative: true,
        },
      ],
      identity: {
        tier: summary.tier,
        manifestation_ids: summary.manifestation_ids,
        field_decisions:
          summary.tier === 'A'
            ? {
                title: {
                  field: 'title',
                  current_value: 'Old title',
                  resolved_value: 'Exact title',
                  status: 'auto',
                  evidence_ids: ['source-title'],
                  root_ids: ['ebook-content'],
                  reasons: ['Exact manifestation corroborated'],
                },
              }
            : {},
        auto_patch: summary.tier === 'A' ? { title: 'Exact title', publisher: 'Exact Publisher' } : {},
        risk_flags: summary.risk_flags,
        reasons: summary.tier === 'A' ? ['Tier A exact identity'] : ['Manifestation remains unresolved'],
      },
      privacy_receipts: [],
      warnings: [],
      error: null,
      package_sha256: '4'.repeat(64),
    },
    authorization: null,
    operation: null,
  },
})

test.describe('Review page — sealed Manifestation V2 workflow', () => {
  test.beforeEach(async ({ page }) => {
    await setApiKey(page)
    await page.route(/\/api\/review\/v2(?:\?.*)?$/, async (route) => {
      await route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({
          status: 'success',
          data: [tierASummary, tierBSummary],
          meta: { total: 2, limit: 50, offset: 0 },
        }),
      })
    })
    await page.route(/\/api\/review\/v2\/evidence-tier-a$/, async (route) => {
      await route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify(detailFor(tierASummary)),
      })
    })
    await page.route(/\/api\/review\/v2\/evidence-tier-b$/, async (route) => {
      await route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify(detailFor(tierBSummary)),
      })
    })
  })

  test('lists only V2 evidence and has no bulk queue action', async ({ page }) => {
    await page.goto('/review')

    await expect(page.getByRole('heading', { name: 'Manifestation review' })).toBeVisible()
    await expect(page.getByRole('button', { name: /Old title/ })).toBeVisible()
    await expect(page.getByRole('button', { name: /Unresolved edition/ })).toBeVisible()
    await expect(page.getByText('Legacy V1 records are historical and read-only')).toBeVisible()
    await expect(page.getByRole('button', { name: /Queue Approved Fixes/i })).toHaveCount(0)
  })

  test('shows exact manifestation evidence, hashes, and proposed field diff', async ({ page }) => {
    await page.goto('/review')
    await page.getByRole('button', { name: /Old title/ }).click()

    await expect(page.getByText('Tier A', { exact: true }).last()).toBeVisible()
    await expect(page.getByText('9780306406157')).toBeVisible()
    await expect(page.getByText('EPUB title page')).toBeVisible()
    await expect(page.getByText('2'.repeat(64))).toBeVisible()
    await expect(page.getByText('Old title', { exact: true }).last()).toBeVisible()
    await expect(page.getByText('Exact title', { exact: true })).toBeVisible()
  })

  test('keeps Tier B evidence read-only', async ({ page }) => {
    await page.goto('/review')
    await page.getByRole('button', { name: /Unresolved edition/ }).click()

    await expect(page.getByText('Tier B', { exact: true }).last()).toBeVisible()
    await expect(page.getByText('Tier B cannot be authorized or queued')).toBeVisible()
    await expect(page.getByRole('button', { name: 'Authorize exact patch' })).toBeDisabled()
    await expect(page.getByRole('button', { name: 'Queue this write' })).toBeDisabled()
  })

  test('ignores a stale detail response after selecting another package', async ({ page }) => {
    let releaseTierA: () => void = () => undefined
    const tierARelease = new Promise<void>((resolve) => {
      releaseTierA = resolve
    })
    let tierARequested: () => void = () => undefined
    const tierARequest = new Promise<void>((resolve) => {
      tierARequested = resolve
    })
    await page.route(/\/api\/review\/v2\/evidence-tier-a$/, async (route) => {
      tierARequested()
      await tierARelease
      await route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify(detailFor(tierASummary)),
      })
    })

    await page.goto('/review')
    await page.getByRole('button', { name: /Old title/ }).click()
    await tierARequest
    await page.getByRole('button', { name: /Unresolved edition/ }).click()
    await expect(page.getByRole('heading', { name: 'Unresolved edition' })).toBeVisible()

    const staleResponse = page.waitForResponse(/\/api\/review\/v2\/evidence-tier-a$/)
    releaseTierA()
    await staleResponse
    await page.evaluate(() => new Promise<void>((resolve) => {
      requestAnimationFrame(() => requestAnimationFrame(() => resolve()))
    }))

    await expect(page.getByRole('heading', { name: 'Unresolved edition' })).toBeVisible()
    await expect(page.getByRole('button', { name: 'Authorize exact patch' })).toBeDisabled()
    await expect(page.getByRole('button', { name: 'Queue this write' })).toBeDisabled()
  })

  test('requires authorization before queueing exactly one write', async ({ page }) => {
    let authorizationBody: unknown
    let applyBody: unknown
    await page.route(/\/api\/review\/v2\/evidence-tier-a\/authorize$/, async (route) => {
      authorizationBody = route.request().postDataJSON()
      await route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({ status: 'success', data: { authorization_id: 'authorization-a' } }),
      })
    })
    await page.route(/\/api\/apply\/v2$/, async (route) => {
      applyBody = route.request().postDataJSON()
      await route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({
          status: 'success',
          data: {
            operation_id: 'operation-a',
            evidence_id: 'evidence-tier-a',
            pilot_id: 'pilot-a',
          },
        }),
      })
    })

    await page.goto('/review')
    await page.getByRole('button', { name: /Old title/ }).click()
    await expect(page.getByRole('button', { name: 'Queue this write' })).toBeDisabled()

    await page.getByLabel('Authorization reason').fill('Matched ISBN and title page')
    await page.getByRole('button', { name: 'Authorize exact patch' }).click()
    await expect.poll(() => authorizationBody).toEqual({ reason: 'Matched ISBN and title page' })
    await expect(page.getByRole('button', { name: 'Queue this write' })).toBeEnabled()

    await page.getByRole('button', { name: 'Queue this write' }).click()
    await expect.poll(() => applyBody).toEqual({
      force: true,
      evidence_id: 'evidence-tier-a',
      authorization_id: 'authorization-a',
    })
    await expect(page.getByText('operation-a')).toBeVisible()
  })
})

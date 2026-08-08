import { expect, test, type Page } from '@playwright/test'

const summary = {
  evidence_id: 'evidence-tier-a',
  run_id: 'verify_certificate_a_001',
  book_key: 'calibre:1',
  created_at: '2026-08-08T10:01:00Z',
  state: 'shadowed',
  tier: 'A',
  current_metadata: { title: 'Old title', authors: ['Current Author'] },
  manifestation_ids: { isbn: '9780306406157' },
  patch_fields: ['title'],
  risk_flags: [],
}

const detail = {
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
        calibre_book_id: 1,
        current_metadata: summary.current_metadata,
        files: [`calibre-offline:${'a'.repeat(64)}:1:EPUB`],
        snapshot_sha256: '1'.repeat(64),
      },
      formats: [{
        path: `calibre-offline:${'a'.repeat(64)}:1:EPUB`,
        format: 'EPUB',
        sha256: '2'.repeat(64),
        status: 'readable',
        identifiers: summary.manifestation_ids,
        title: 'Exact title',
        authors: ['Exact Author'],
        languages: ['en'],
        evidence_ids: ['source-title'],
        error: null,
      }],
      source_evidence: [{
        evidence_id: 'source-title',
        root_id: 'google-books-isbn',
        independence_root: 'google-books',
        source_kind: 'provider',
        field: 'title',
        value: 'Exact title',
        locator: 'Google Books exact ISBN response',
        authoritative: true,
      }],
      identity: {
        tier: 'A',
        manifestation_ids: summary.manifestation_ids,
        auto_patch: { title: 'Exact title' },
        risk_flags: [],
        reasons: ['Exact ISBN evidence agreed'],
      },
      warnings: [],
      error: null,
      package_sha256: '3'.repeat(64),
    },
    authorization: null,
    operation: null,
    writes_enabled: false,
  },
}

async function mockEvidence(page: Page) {
  await page.route(/\/api\/health\/ready$/, (route) => route.fulfill({ json: { status: 'ready', certificate: 'A' } }))
  await page.route(/\/api\/review\/v2(?:\?.*)?$/, (route) => route.fulfill({ json: { status: 'success', data: [summary], meta: { total: 1, limit: 50, offset: 0 } } }))
  await page.route(/\/api\/review\/v2\/evidence-tier-a$/, (route) => route.fulfill({ json: detail }))
}

test.beforeEach(async ({ page }) => mockEvidence(page))

test('lists only sealed Certificate A evidence', async ({ page }) => {
  await page.goto('/review')
  await expect(page.getByRole('heading', { name: 'Sealed evidence', exact: true })).toBeVisible()
  await expect(page.getByRole('button', { name: /Old title/ })).toBeVisible()
  await expect(page.getByText(/Nothing on this screen can authorize/)).toBeVisible()
})

test('shows identities, format hashes, evidence, and shadow differences', async ({ page }) => {
  await page.goto('/review/evidence-tier-a')
  await expect(page.getByRole('heading', { name: 'Old title' })).toBeVisible()
  await expect(page.getByText('9780306406157')).toBeVisible()
  await expect(page.getByText('2'.repeat(64))).toBeVisible()
  await expect(page.getByText('Google Books exact ISBN response')).toBeVisible()
  await expect(page.getByRole('cell', { name: 'Exact title' })).toBeVisible()
  await expect(page.getByText(/writes are disabled by the API contract/)).toBeVisible()
})

test('has no production authorization, queue, or apply action', async ({ page }) => {
  await page.goto('/review/evidence-tier-a')
  for (const label of [/authorize/i, /queue.*write/i, /apply/i]) {
    await expect(page.getByRole('button', { name: label })).toHaveCount(0)
  }
})

test('passes run and tier filters to the read-only endpoint', async ({ page }) => {
  let requestedUrl = ''
  await page.route(/\/api\/review\/v2(?:\?.*)?$/, async (route) => {
    requestedUrl = route.request().url()
    await route.fulfill({ json: { status: 'success', data: [summary], meta: { total: 1, limit: 50, offset: 0 } } })
  })
  await page.goto('/review?run_id=verify_certificate_a_001')
  await page.getByLabel('Identity tier').selectOption('A')
  await expect.poll(() => requestedUrl).toContain('run_id=verify_certificate_a_001')
  await expect.poll(() => requestedUrl).toContain('tier=A')
})

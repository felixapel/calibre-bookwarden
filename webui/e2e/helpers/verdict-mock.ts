/**
 * Test fixture helpers for the v1.0 verdict flow.
 */

import type { Page } from '@playwright/test'
import { mockApi } from './api-mock'
import type { BookVerdict } from './types'

/**
 * book_key values are URL-encoded when the React app uses `encodeURIComponent`.
 * E.g. "calibre:1" → "calibre%3A1" in the request URL.
 *
 * This helper accepts the unencoded form and matches both encodings.
 */
export async function mockVerdictFor(
  page: Page,
  bookKey: string,
  verdict: BookVerdict
): Promise<void> {
  // The colon in "calibre:N" gets URL-encoded to %3A. Match both forms.
  const safeKey = bookKey.replace(/:/g, '%3A')
  await mockApi(page, new RegExp(`/api/books/${safeKey}/verdict`), {
    get: { body: { status: 'success', data: verdict } },
  })
}

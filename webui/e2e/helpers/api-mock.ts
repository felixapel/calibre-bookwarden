/**
 * API mock helper — intercepts /api/* requests and returns canned responses.
 *
 * Use this when you want a test that:
 *  - Doesn't need a real backend running
 *  - Needs very specific response shapes (e.g. error states, edge cases)
 *  - Should be fast (no network)
 */

import { Page, Route } from '@playwright/test'

export interface MockResponse {
  status?: number
  body: unknown
}

export async function mockApi(
  page: Page,
  pathPattern: string | RegExp,
  responses: { get?: MockResponse; post?: MockResponse; put?: MockResponse; delete?: MockResponse }
): Promise<void> {
  await page.route(pathPattern, async (route: Route) => {
    const method = route.request().method().toLowerCase()
    const handler = (responses as Record<string, MockResponse | undefined>)[method]
    if (handler) {
      await route.fulfill({
        status: handler.status ?? 200,
        contentType: 'application/json',
        body: JSON.stringify(handler.body),
      })
    } else {
      await route.fallback()
    }
  })
}

export async function mockAllApi(
  page: Page,
  baseResponses: Record<string, MockResponse>
): Promise<void> {
  for (const [path, response] of Object.entries(baseResponses)) {
    await mockApi(page, path, { get: response })
  }
}

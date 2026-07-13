/**
 * Auth helper — sets API key in localStorage so the WebUI sends the
 * X-API-Key header. No-op if the WebUI is in dev mode without auth.
 */

import { Page } from '@playwright/test'

export async function setApiKey(page: Page, key: string = 'test-key-12345'): Promise<void> {
  await page.addInitScript((k: string) => {
    localStorage.setItem('BOOKAUDIT_API_KEY', k)
  }, key)
}

export async function clearApiKey(page: Page): Promise<void> {
  await page.addInitScript(() => {
    localStorage.removeItem('BOOKAUDIT_API_KEY')
  })
}

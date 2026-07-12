import AxeBuilder from '@axe-core/playwright'
import { expect, test } from '@playwright/test'
import { mockApi } from './helpers/api-mock'

test.beforeEach(async ({ page }) => {
  await mockApi(page, /\/api\/health\/ready/, {
    get: { body: { status: 'ready', checks: {} } },
  })
  await mockApi(page, /\/api\/config/, {
    get: {
      body: {
        status: 'success',
        data: {
          config: {
            library: { path: '/library', read_only: true },
            ollama_enabled: false,
          },
          mutable: false,
        },
      },
    },
  })
  await mockApi(page, /\/api\/doctor/, {
    get: { body: { status: 'success', data: { dependencies: {}, connectivity: {} } } },
  })
  await mockApi(page, /\/api\/books$/, {
    get: { body: { status: 'success', data: [], meta: { total: 0, limit: 50, offset: 0 } } },
  })
  await mockApi(page, /\/api\/books\/all\/duplicates/, {
    get: { body: { status: 'success', data: [] } },
  })
  await mockApi(page, /\/api\/runs$/, {
    get: { body: { status: 'success', data: [], meta: { total: 0, limit: 50, offset: 0 } } },
  })
})

test('dashboard has no serious accessibility violations or horizontal overflow', async ({ page }) => {
  await page.goto('/')
  await expect(page.getByRole('heading', { level: 1 })).toBeVisible()

  const results = await new AxeBuilder({ page }).analyze()
  const serious = results.violations.filter(({ impact }) => impact === 'serious' || impact === 'critical')
  expect(serious, JSON.stringify(serious, null, 2)).toEqual([])

  const dimensions = await page.evaluate(() => ({
    clientWidth: document.documentElement.clientWidth,
    scrollWidth: document.documentElement.scrollWidth,
    overflowing: [...document.querySelectorAll<HTMLElement>('body *')]
      .filter((element) => element.getBoundingClientRect().right > document.documentElement.clientWidth + 1)
      .slice(0, 10)
      .map((element) => ({ tag: element.tagName, className: element.className, right: element.getBoundingClientRect().right })),
  }))
  expect(dimensions.scrollWidth, JSON.stringify(dimensions, null, 2)).toBeLessThanOrEqual(dimensions.clientWidth)
})

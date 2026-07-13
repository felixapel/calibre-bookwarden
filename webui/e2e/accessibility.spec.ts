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

test('dashboard stays within production web-performance budgets', async ({ page }) => {
  await page.addInitScript(() => {
    const vitals = { cls: 0, lcp: 0 }
    ;(window as unknown as { __bookauditVitals: typeof vitals }).__bookauditVitals = vitals
    new PerformanceObserver((list) => {
      for (const entry of list.getEntries()) {
        const shift = entry as PerformanceEntry & { hadRecentInput?: boolean; value?: number }
        if (!shift.hadRecentInput) vitals.cls += shift.value ?? 0
      }
    }).observe({ type: 'layout-shift', buffered: true })
    new PerformanceObserver((list) => {
      const latest = list.getEntries().at(-1)
      if (latest) vitals.lcp = latest.startTime
    }).observe({ type: 'largest-contentful-paint', buffered: true })
  })

  await page.goto('/')
  await expect(page.getByRole('heading', { level: 1 })).toBeVisible()
  await page.waitForTimeout(500)
  const metrics = await page.evaluate(() => {
    const navigation = performance.getEntriesByType('navigation')[0] as PerformanceNavigationTiming
    const vitals = (window as unknown as { __bookauditVitals: { cls: number; lcp: number } }).__bookauditVitals
    const scriptTransferBytes = performance.getEntriesByType('resource')
      .filter((entry) => entry.name.includes('/assets/') && entry.name.endsWith('.js'))
      .reduce((total, entry) => total + (entry as PerformanceResourceTiming).encodedBodySize, 0)
    return { ...vitals, domContentLoaded: navigation.domContentLoadedEventEnd, scriptTransferBytes }
  })
  expect(metrics.cls).toBeLessThanOrEqual(0.1)
  if (metrics.lcp > 0) expect(metrics.lcp).toBeLessThanOrEqual(2_500)
  expect(metrics.domContentLoaded).toBeLessThanOrEqual(2_500)
  expect(metrics.scriptTransferBytes).toBeLessThanOrEqual(150_000)
})

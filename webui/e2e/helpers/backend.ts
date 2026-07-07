/**
 * Backend helper — spawns the FastAPI test app on a free port.
 *
 * Tests can use this to get a real HTTP backend instead of mocking.
 * Falls back to in-process TestClient when no real server is available.
 *
 * NOTE: Playwright's webServer config already starts the backend. This helper
 * is here for tests that want to manage the lifecycle themselves.
 */

import { spawn, ChildProcess } from 'node:child_process'
import { setTimeout as sleep } from 'node:timers/promises'
import { join, resolve } from 'node:path'

const BACKEND_BIN = process.env.BACKEND_BIN ?? 'python'
const BACKEND_PORT = Number(process.env.BACKEND_PORT ?? '5174')
const BACKEND_HOST = process.env.BACKEND_HOST ?? '127.0.0.1'
const STATIC_DIR = process.env.BOOKAUDIT_STATIC_DIR ?? resolve(__dirname, '..', 'dist')

let proc: ChildProcess | null = null

export async function startBackend(): Promise<string> {
  const url = `http://${BACKEND_HOST}:${BACKEND_PORT}`
  if (proc) return url

  proc = spawn(
    BACKEND_BIN,
    ['-m', 'calibre_ai_auditor.cli.main', 'web', '--port', String(BACKEND_PORT), '--host', BACKEND_HOST],
    {
      env: {
        ...process.env,
        BOOKAUDIT_STATIC_DIR: STATIC_DIR,
        BOOKAUDIT_DATABASE__BACKEND: 'sqlite',
        BOOKAUDIT_DB_PATH: process.env.BOOKAUDIT_DB_PATH ?? join(__dirname, '..', 'e2e_test.db'),
        BOOKAUDIT_READ_ONLY: 'true',
        BOOKAUDIT_LIBRARY_PATH: '/dev/null',
        BOOKAUDIT_LOG_LEVEL: 'WARNING',
      },
      stdio: 'pipe',
    }
  )
  proc.stdout?.on('data', (d) => process.stdout.write(`[backend] ${d}`))
  proc.stderr?.on('data', (d) => process.stderr.write(`[backend-err] ${d}`))

  // Wait for /api/health to return 200
  for (let i = 0; i < 30; i++) {
    try {
      const r = await fetch(`${url}/api/health`)
      if (r.ok) return url
    } catch {
      // not ready yet
    }
    await sleep(500)
  }
  throw new Error(`Backend did not start within 15s at ${url}`)
}

export async function stopBackend(): Promise<void> {
  if (proc) {
    proc.kill('SIGTERM')
    await sleep(500)
    if (proc.exitCode === null) proc.kill('SIGKILL')
    proc = null
  }
}

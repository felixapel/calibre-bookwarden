import { getApiKey } from './auth'

const fetchWithAuth = async (url: string, options: RequestInit = {}) => {
  const apiKey = getApiKey()
  const headers = new Headers(options.headers || {})
  if (apiKey) {
    headers.set('X-API-Key', apiKey)
  }

  const response = await fetch(url, {
    ...options,
    headers,
  })

  if (response.status === 401) {
    window.dispatchEvent(new Event('bookaudit-unauthorized'))
  }

  return response
}

export const fetchConfig = async () => {
  const res = await fetchWithAuth('/api/config')
  if (!res.ok) throw new Error('Failed to fetch config')
  return res.json()
}

export const updateConfig = async (config: any) => {
  const res = await fetchWithAuth('/api/config', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(config),
  })
  if (!res.ok) throw new Error('Failed to update config')
  return res.json()
}

export const fetchDoctor = async () => {
  const res = await fetchWithAuth('/api/doctor')
  if (!res.ok) throw new Error('Failed to fetch doctor')
  return res.json()
}

export const fetchHealth = async () => {
  const res = await fetchWithAuth('/api/health')
  if (!res.ok) throw new Error('Failed to fetch health')
  return res.json()
}

export const inspectPath = async (path: string, noProviders: boolean = false) => {
  const res = await fetchWithAuth('/api/inspect/path', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ path, no_providers: noProviders }),
  })
  if (!res.ok) {
    let errorMessage = 'Failed to inspect path'
    try {
      const data = await res.json()
      if (data.detail) errorMessage = data.detail
    } catch {
      errorMessage = `Server Error: ${res.status} ${res.statusText}`
    }
    throw new Error(errorMessage)
  }
  return res.json()
}

export const fetchFs = async (dirPath: string) => {
  const res = await fetchWithAuth(`/api/inspect/fs?dir_path=${encodeURIComponent(dirPath)}`)
  if (!res.ok) throw new Error('Failed to list directory')
  return res.json()
}

export const scanLibrary = async (req: { library?: string, search?: string, limit?: number }) => {
  const res = await fetchWithAuth('/api/runs/scan', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(req),
  })
  if (!res.ok) throw new Error('Failed to start scan')
  return res.json() // returns { job_id }
}

export const fetchJobStatus = async (jobId: string) => {
  const res = await fetchWithAuth(`/api/jobs/${jobId}`)
  if (!res.ok) throw new Error('Failed to fetch job')
  return res.json()
}

export const fetchBooks = async () => {
  const res = await fetchWithAuth('/api/books')
  if (!res.ok) throw new Error('Failed to fetch books')
  return res.json()
}

export const fetchEvidence = async (bookKey: string) => {
  const res = await fetchWithAuth(`/api/books/${bookKey}/evidence`)
  if (!res.ok) throw new Error('Failed to fetch evidence')
  return res.json()
}

export const approvePatch = async (bookKey: string) => {
  const res = await fetchWithAuth(`/api/review/${bookKey}/approve`, { method: 'POST' })
  if (!res.ok) throw new Error('Failed to approve patch')
  return res.json()
}

export const rejectPatch = async (bookKey: string) => {
  const res = await fetchWithAuth(`/api/review/${bookKey}/reject`, { method: 'POST' })
  if (!res.ok) throw new Error('Failed to reject patch')
  return res.json()
}

export const applyPatches = async (force = true) => {
  const res = await fetchWithAuth('/api/apply', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ force }),
  })
  if (!res.ok) {
    let errorMessage = 'Failed to apply patches'
    try {
      const data = await res.json()
      if (data.detail) errorMessage = data.detail
    } catch {
      errorMessage = `Server Error: ${res.status} ${res.statusText}`
    }
    throw new Error(errorMessage)
  }
  return res.json()
}

export const fetchRuns = async () => {
  const res = await fetchWithAuth('/api/runs')
  if (!res.ok) throw new Error('Failed to fetch runs')
  return res.json()
}

export const revertRun = async (runId: string, force = true) => {
  const res = await fetchWithAuth(`/api/runs/${encodeURIComponent(runId)}/revert`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ force }),
  })
  if (!res.ok) {
    let errorMessage = 'Failed to revert run'
    try {
      const data = await res.json()
      if (data.detail) errorMessage = data.detail
    } catch {}
    throw new Error(errorMessage)
  }
  return res.json()
}

export const startAudit = async (runId: string) => {
  const res = await fetchWithAuth(`/api/runs/${runId}/audit`, { method: 'POST' })
  if (!res.ok) throw new Error('Failed to start audit')
  return res.json()
}

export const fetchDuplicates = async () => {
  const res = await fetchWithAuth('/api/books/all/duplicates')
  if (!res.ok) throw new Error('Failed to fetch duplicates')
  return res.json()
}


// ─── v1.0 verify API ──────────────────────────────────────────────────────

export interface VerifyRunSummary {
  run_id: string
  status: 'running' | 'completed' | 'failed'
  started_at: string
  finished_at: string | null
  total: number
  completed: number
  counts: Record<string, number>
}

export interface VerifyRunDetail extends VerifyRunSummary {
  verdicts: unknown[]
}

export const startVerify = async (req: {
  library?: string
  limit?: number
  use_llm?: boolean
}) => {
  const res = await fetchWithAuth('/api/verify', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(req),
  })
  if (!res.ok) throw new Error('Failed to start verify')
  return res.json() as Promise<{ status: string; data: { run_id: string; started_at: string; total: number; status: string } }>
}

export const fetchVerifyRuns = async () => {
  const res = await fetchWithAuth('/api/verify/runs')
  if (!res.ok) throw new Error('Failed to fetch verify runs')
  return res.json() as Promise<{ status: string; data: { runs: VerifyRunSummary[] } }>
}

export const fetchVerifyRun = async (runId: string) => {
  const res = await fetchWithAuth(`/api/verify/${encodeURIComponent(runId)}`)
  if (!res.ok) throw new Error(`Failed to fetch verify run ${runId}`)
  return res.json() as Promise<{ status: string; data: VerifyRunDetail }>
}

// ─── Prometheus metrics ────────────────────────────────────────────────────

export const fetchMetrics = async () => {
  const res = await fetchWithAuth('/api/metrics')
  if (!res.ok) throw new Error('Failed to fetch metrics')
  return res.text()
}

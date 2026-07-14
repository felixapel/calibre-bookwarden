import { getApiKey, markUnauthorized } from './auth'

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

  if (response.status === 401 && getApiKey() === apiKey) {
    markUnauthorized()
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
  const envelope = await res.json()
  return envelope.data
}

export const fetchHealth = async () => {
  const res = await fetchWithAuth('/api/health/ready')
  if (!res.ok) throw new Error('Failed to fetch health')
  return res.json()
}

export const fetchBookVerdict = async (bookKey: string) => {
  const res = await fetchWithAuth(`/api/books/${encodeURIComponent(bookKey)}/verdict`)
  if (!res.ok) throw new Error('Failed to fetch verdict')
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
  const envelope = await res.json()
  return envelope.data
}

export const fetchFs = async (dirPath: string) => {
  const res = await fetchWithAuth(`/api/inspect/fs?dir_path=${encodeURIComponent(dirPath)}`)
  if (!res.ok) throw new Error('Failed to list directory')
  const envelope = await res.json()
  return envelope.data
}

export const scanLibrary = async (req: { library?: string, search?: string, limit?: number }) => {
  const res = await fetchWithAuth('/api/runs/scan', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(req),
  })
  if (!res.ok) throw new Error('Failed to start scan')
  const envelope = await res.json()
  return envelope.data
}

export const fetchJobStatus = async (jobId: string) => {
  const res = await fetchWithAuth(`/api/jobs/${jobId}`)
  if (!res.ok) throw new Error('Failed to fetch job')
  const envelope = await res.json()
  return envelope.data
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

export const applyPatches = async (bookKeys: string[], force = true) => {
  const res = await fetchWithAuth('/api/apply', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ force, book_keys: bookKeys }),
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

// ─── Manifestation V2 supervised review ──────────────────────────────────

export type IdentityTier = 'A' | 'B' | 'C'

export interface ReviewV2Summary {
  evidence_id: string
  run_id: string
  book_key: string
  created_at: string
  state: string
  tier: IdentityTier
  current_metadata: Record<string, unknown>
  manifestation_ids: Record<string, string>
  patch_fields: string[]
  risk_flags: string[]
}

export interface FormatEvidenceV2 {
  path: string
  format: string
  sha256: string
  status: string
  identifiers: Record<string, string>
  title: string | null
  authors: string[]
  languages: string[]
  evidence_ids: string[]
  error: string | null
}

export interface SourceEvidenceV2 {
  evidence_id: string
  root_id: string
  independence_root: string | null
  source_kind: string
  field: string
  value: unknown
  locator: string | null
  authoritative: boolean
}

export interface FieldDecisionV2 {
  field: string
  current_value: unknown
  resolved_value: unknown
  status: string
  evidence_ids: string[]
  root_ids: string[]
  reasons: string[]
}

export interface EvidencePackageV2 {
  schema_version: 2
  policy_version: 'manifestation-v2'
  evidence_id: string
  run_id: string
  book_key: string
  created_at: string
  state: string
  snapshot: {
    book_key: string
    calibre_book_id: number
    current_metadata: Record<string, unknown>
    files: string[]
    library_root: string | null
    snapshot_sha256: string
  }
  formats: FormatEvidenceV2[]
  source_evidence: SourceEvidenceV2[]
  identity: {
    tier: IdentityTier
    manifestation_ids: Record<string, string>
    field_decisions: Record<string, FieldDecisionV2>
    auto_patch: Record<string, unknown>
    risk_flags: string[]
    reasons: string[]
  }
  warnings: string[]
  error: string | null
  package_sha256: string
}

export interface OperationStatusV2 {
  operation_id: string
  state: string
  pilot_id: string | null
  change_id: number | null
  created_at: string
  updated_at: string
  completed_at: string | null
  error_code: string | null
}

export interface ReviewV2Detail {
  package: EvidencePackageV2
  authorization: {
    authorization_id: string
    actor: string
    reason: string
    created_at: string
  } | null
  operation: OperationStatusV2 | null
}

const apiError = async (response: Response, fallback: string) => {
  try {
    const payload = await response.json()
    return new Error(typeof payload.detail === 'string' ? payload.detail : fallback)
  } catch {
    return new Error(`${fallback} (${response.status})`)
  }
}

export const fetchReviewV2 = async (params: {
  runId?: string
  state?: string
  tier?: IdentityTier
  limit?: number
  offset?: number
} = {}) => {
  const query = new URLSearchParams()
  if (params.runId) query.set('run_id', params.runId)
  if (params.state) query.set('state', params.state)
  if (params.tier) query.set('tier', params.tier)
  query.set('limit', String(params.limit ?? 50))
  query.set('offset', String(params.offset ?? 0))
  const response = await fetchWithAuth(`/api/review/v2?${query}`)
  if (!response.ok) throw await apiError(response, 'Failed to load V2 review queue')
  return response.json() as Promise<{
    status: 'success'
    data: ReviewV2Summary[]
    meta: { total: number; limit: number; offset: number }
  }>
}

export const fetchReviewV2Detail = async (evidenceId: string) => {
  const response = await fetchWithAuth(`/api/review/v2/${encodeURIComponent(evidenceId)}`)
  if (!response.ok) throw await apiError(response, 'Failed to load sealed evidence')
  const envelope = await response.json() as { status: 'success'; data: ReviewV2Detail }
  return envelope.data
}

export const authorizeReviewV2 = async (evidenceId: string, reason: string) => {
  const response = await fetchWithAuth(`/api/review/v2/${encodeURIComponent(evidenceId)}/authorize`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ reason }),
  })
  if (!response.ok) throw await apiError(response, 'Failed to authorize exact patch')
  const envelope = await response.json() as {
    status: 'success'
    data: { authorization_id: string }
  }
  return envelope.data
}

export const queueReviewV2 = async (evidenceId: string, authorizationId: string) => {
  const response = await fetchWithAuth('/api/apply/v2', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({
      force: true,
      evidence_id: evidenceId,
      authorization_id: authorizationId,
    }),
  })
  if (!response.ok) throw await apiError(response, 'Failed to queue supervised write')
  const envelope = await response.json() as {
    status: 'success'
    data: { operation_id: string; evidence_id: string; pilot_id: string }
  }
  return envelope.data
}

export const fetchOperationV2 = async (operationId: string) => {
  const response = await fetchWithAuth(`/api/operations/${encodeURIComponent(operationId)}`)
  if (!response.ok) throw await apiError(response, 'Failed to refresh operation status')
  const envelope = await response.json() as { status: 'success'; data: OperationStatusV2 }
  return envelope.data
}


// ─── Versioned verify API (Manifestation V2 by default) ───────────────────

export interface VerifyRunSummary {
  run_id: string
  status: 'running' | 'completed' | 'failed'
  started_at: string
  finished_at: string | null
  total: number
  completed: number
  counts: Record<string, number>
  pipeline_version: string
  mode: string
}

export interface VerifyRunDetail extends VerifyRunSummary {
  verdicts: unknown[]
}

export const startVerify = async (req: {
  library?: string
  limit?: number
  use_llm?: boolean
  use_ocr?: boolean
  use_vision?: boolean
  pipeline?: 'v2' | 'v1'
  allow_remote_text?: boolean
  allow_remote_images?: boolean
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

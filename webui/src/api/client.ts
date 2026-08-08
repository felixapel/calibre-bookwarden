import { getApiKey, markUnauthorized } from './auth'

const fetchWithAuth = async (url: string, options: RequestInit = {}) => {
  const apiKey = getApiKey()
  const headers = new Headers(options.headers ?? {})
  if (apiKey) headers.set('X-API-Key', apiKey)
  const response = await fetch(url, { ...options, headers })
  if (response.status === 401 && getApiKey() === apiKey) markUnauthorized()
  return response
}

const apiError = async (response: Response, fallback: string) => {
  try {
    const payload = await response.json() as { detail?: string | { code?: string } }
    if (typeof payload.detail === 'string') return new Error(payload.detail)
    if (payload.detail?.code) return new Error(payload.detail.code.replaceAll('_', ' '))
  } catch {
    // The status fallback below is deliberately content-free.
  }
  return new Error(`${fallback} (${response.status})`)
}

export interface CertificateACapabilities {
  certificate: 'A'
  mode: 'shadow'
  pipeline: 'manifestation-v2'
  library_source: 'offline-folder'
  providers: string[]
  ocr: { enabled: boolean; backend: 'tesseract'; max_pages: number }
  writes_enabled: false
}

export const fetchHealth = async () => {
  const response = await fetchWithAuth('/api/health/ready')
  if (!response.ok) throw await apiError(response, 'Certificate A is not ready')
  return response.json() as Promise<{ status: 'ready'; certificate: 'A' }>
}

export const fetchCapabilities = async () => {
  const response = await fetchWithAuth('/api/capabilities')
  if (!response.ok) throw await apiError(response, 'Failed to load capabilities')
  return response.json() as Promise<CertificateACapabilities>
}

export type VerifyRunStatus =
  | 'pending'
  | 'inventorying'
  | 'running'
  | 'cancelling'
  | 'cancelled'
  | 'completed'
  | 'completed_with_errors'
  | 'failed'
  | 'source_changed'
  | 'blocked_recovery'

export interface VerifyRunSummary {
  run_id: string
  status: VerifyRunStatus
  started_at: string
  finished_at: string | null
  total: number | null
  completed: number
  counts: Record<string, number>
  error_code: string | null
}

export interface VerifyResultSummary {
  book_key: string
  state: string
  evidence_id: string | null
}

export interface VerifyRunDetail extends VerifyRunSummary {
  results: VerifyResultSummary[]
}

export const startVerify = async (
  request: { limit?: number; use_ocr: boolean; confirm_calibre_stopped: true },
  idempotencyKey: string,
) => {
  const response = await fetchWithAuth('/api/verify', {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
      'Idempotency-Key': idempotencyKey,
    },
    body: JSON.stringify(request),
  })
  if (!response.ok) throw await apiError(response, 'Failed to request verification')
  return response.json() as Promise<VerifyRunSummary>
}

export const fetchVerifyRuns = async () => {
  const response = await fetchWithAuth('/api/verify/runs')
  if (!response.ok) throw await apiError(response, 'Failed to load verification runs')
  return response.json() as Promise<{ runs: VerifyRunSummary[]; limit: number; offset: number }>
}

export const fetchVerifyRun = async (runId: string) => {
  const response = await fetchWithAuth(`/api/verify/${encodeURIComponent(runId)}`)
  if (!response.ok) throw await apiError(response, 'Failed to load verification run')
  return response.json() as Promise<VerifyRunDetail>
}

export const cancelVerifyRun = async (runId: string) => {
  const response = await fetchWithAuth(`/api/verify/${encodeURIComponent(runId)}/cancel`, { method: 'POST' })
  if (!response.ok) throw await apiError(response, 'Failed to cancel verification run')
  return response.json() as Promise<VerifyRunSummary>
}

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
    snapshot_sha256: string
  }
  formats: FormatEvidenceV2[]
  source_evidence: SourceEvidenceV2[]
  identity: {
    tier: IdentityTier
    manifestation_ids: Record<string, string>
    auto_patch: Record<string, unknown>
    risk_flags: string[]
    reasons: string[]
  }
  warnings: string[]
  error: string | null
  package_sha256: string
}

export interface ReviewV2Detail {
  package: EvidencePackageV2
  authorization: null
  operation: null
  writes_enabled: false
}

export const fetchReviewV2 = async (params: { runId?: string; tier?: IdentityTier; limit?: number; offset?: number } = {}) => {
  const query = new URLSearchParams({
    limit: String(params.limit ?? 50),
    offset: String(params.offset ?? 0),
  })
  if (params.runId) query.set('run_id', params.runId)
  if (params.tier) query.set('tier', params.tier)
  const response = await fetchWithAuth(`/api/review/v2?${query}`)
  if (!response.ok) throw await apiError(response, 'Failed to load sealed evidence')
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

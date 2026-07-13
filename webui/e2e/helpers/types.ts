/**
 * TypeScript types matching the v1.0 BookVerdict and FieldVerdict contracts.
 * Keep in sync with src/calibre_ai_auditor/verification/verdict.py.
 */

export type VerdictKind = 'confirmed' | 'mismatch' | 'missing' | 'ambiguous'

export interface EvidenceSpan {
  source: string
  text: string
  page_range?: string | null
  confidence: number
}

export interface FieldVerdict {
  field: string
  declared_value: unknown
  observed_value: unknown | null
  verdict: VerdictKind
  confidence: number
  evidence: EvidenceSpan[]
  requires_review?: boolean
  risk_flags?: string[]
  reason?: string | null
  is_deterministic?: boolean
  created_at?: string
}

export interface BookVerdict {
  book_key: string
  run_id: string
  field_verdicts: Record<string, FieldVerdict>
  overall_confidence: number
  risk_flags: string[]
  action: string
  auto_apply_eligible: boolean
  proposed_patch: Record<string, unknown>
  reasons: string[]
  created_at?: string
}

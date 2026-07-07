/**
 * Canned fixture data for E2E tests.
 *
 * Exports pre-built BookRecord, EvidencePackage, and BookVerdict payloads
 * that match the v1.0 contract. Tests use these via api-mock.ts.
 */

import type { BookVerdict, FieldVerdict } from './types'

export const SAMPLE_BOOK_VERDICT_CONFIRMED: BookVerdict = {
  book_key: 'calibre:1',
  run_id: 'test_run',
  field_verdicts: {
    title: {
      field: 'title',
      declared_value: 'The Great Gatsby',
      observed_value: 'The Great Gatsby',
      verdict: 'confirmed',
      confidence: 99,
      evidence: [
        { source: 'title_page', text: 'The Great Gatsby', page_range: '1', confidence: 99 },
      ],
      requires_review: false,
      risk_flags: [],
      reason: 'Titles match exactly.',
      is_deterministic: true,
      created_at: '2026-07-05T10:00:00Z',
    },
    authors: {
      field: 'authors',
      declared_value: ['F. Scott Fitzgerald'],
      observed_value: ['F. Scott Fitzgerald'],
      verdict: 'confirmed',
      confidence: 99,
      evidence: [],
      requires_review: false,
      risk_flags: [],
      reason: 'Authors match.',
      is_deterministic: true,
      created_at: '2026-07-05T10:00:00Z',
    },
  },
  overall_confidence: 99,
  risk_flags: [],
  action: 'no_change',
  auto_apply_eligible: false,
  proposed_patch: {},
  reasons: ['[title] Titles match exactly.', '[authors] Authors match.'],
  created_at: '2026-07-05T10:00:00Z',
}

export const SAMPLE_BOOK_VERDICT_MISMATCH: BookVerdict = {
  book_key: 'calibre:2',
  run_id: 'test_run',
  field_verdicts: {
    title: {
      field: 'title',
      declared_value: 'WRONG TITLE',
      observed_value: 'Some Real Book',
      verdict: 'mismatch',
      confidence: 95,
      evidence: [
        { source: 'title_page', text: 'Some Real Book', page_range: '1', confidence: 95 },
      ],
      requires_review: false,
      risk_flags: [],
      reason: 'Titles differ significantly (similarity 0.26).',
      is_deterministic: true,
      created_at: '2026-07-05T10:00:00Z',
    },
  },
  overall_confidence: 95,
  risk_flags: [],
  action: 'suggest_fix',
  auto_apply_eligible: true,
  proposed_patch: { title: 'Some Real Book' },
  reasons: ['[title] Titles differ significantly (similarity 0.26).'],
  created_at: '2026-07-05T10:00:00Z',
}

export const SAMPLE_BOOK_VERDICT_AUTHOR_SWAP: BookVerdict = {
  book_key: 'calibre:3',
  run_id: 'test_run',
  field_verdicts: {
    authors: {
      field: 'authors',
      declared_value: ['Wrong Author'],
      observed_value: ['Right Author'],
      verdict: 'mismatch',
      confidence: 92,
      evidence: [
        { source: 'title_page', text: 'By Right Author', page_range: '1', confidence: 92 },
      ],
      requires_review: true,
      risk_flags: ['author_swap'],
      reason: 'Author swap: declared ["Wrong Author"] but book has ["Right Author"].',
      is_deterministic: true,
      created_at: '2026-07-05T10:00:00Z',
    },
  },
  overall_confidence: 92,
  risk_flags: ['author_swap'],
  action: 'needs_review',
  auto_apply_eligible: false,
  proposed_patch: { authors: ['Right Author'] },
  reasons: ['[authors] Author swap detected.'],
  created_at: '2026-07-05T10:00:00Z',
}

export const SAMPLE_BOOK_VERDICT_AMBIGUOUS: BookVerdict = {
  book_key: 'calibre:4',
  run_id: 'test_run',
  field_verdicts: {
    title: {
      field: 'title',
      declared_value: 'The Novel',
      observed_value: 'The Novels',
      verdict: 'ambiguous',
      confidence: 60,
      evidence: [],
      requires_review: true,
      risk_flags: [],
      reason: 'Title similarity 0.62 is ambiguous; needs LLM adjudication.',
      is_deterministic: true,
      created_at: '2026-07-05T10:00:00Z',
    },
  },
  overall_confidence: 60,
  risk_flags: [],
  action: 'needs_review',
  auto_apply_eligible: false,
  proposed_patch: {},
  reasons: ['[title] Ambiguous match.'],
  created_at: '2026-07-05T10:00:00Z',
}

export const SAMPLE_BOOKS = [
  {
    book_key: 'calibre:1',
    run_id: 'test_run',
    current_metadata: { title: 'The Great Gatsby', authors: ['F. Scott Fitzgerald'] },
    status: 'suggest_fix',
  },
  {
    book_key: 'calibre:2',
    run_id: 'test_run',
    current_metadata: { title: 'WRONG TITLE', authors: ['Some Author'] },
    status: 'suggest_fix',
  },
  {
    book_key: 'calibre:3',
    run_id: 'test_run',
    current_metadata: { title: 'Right Book', authors: ['Wrong Author'] },
    status: 'needs_review',
  },
  {
    book_key: 'calibre:4',
    run_id: 'test_run',
    current_metadata: { title: 'The Novel', authors: ['Some Author'] },
    status: 'audited',
  },
]

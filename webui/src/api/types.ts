export type CqsTier = 'S' | 'A' | 'B' | 'C' | 'D'

export interface CoverQualityScore {
  cqs: number
  tier: CqsTier
  width: number
  height: number
  aspect_ratio?: number
  laplacian_var?: number
  entropy?: number
  color_space?: string
}

export interface CoverDeckCandidate {
  source: 'native_epub' | 'native_pdf' | 'openlibrary' | 'google_books' | 'hardcover'
  url?: string
  width?: number
  height?: number
  cqs?: number
  extracted_path?: string
}

export interface CoverDeckItem {
  book_id: number
  title: string
  author: string
  current_cqs: number
  current_tier: CqsTier
  width: number
  height: number
  laplacian_var?: number
  entropy?: number
  is_spurious?: boolean
  spurious_reason?: string
  candidates?: CoverDeckCandidate[]
}

export interface Audit360CoverAnomaly {
  book_id: number
  title: string
  author: string
  path: string
  width: number
  height: number
  file_size_mb: number
  cqs: number
  tier: CqsTier
  issue: 'decompression_bomb' | 'low_resolution' | 'missing' | 'spurious'
}

export interface AuthorSortDesync {
  author_id: number
  name: string
  current_sort: string
  canonical_sort: string
}

export interface PathDesync {
  book_id: number
  title: string
  db_path: string
  reason: 'missing_directory' | 'missing_file' | 'orphan_file'
}

export interface Audit360Report {
  summary: {
    total_books: number
    total_authors: number
    total_series: number
    average_cqs: number
    missing_covers_count: number
    decompression_bombs_count: number
    author_sort_desyncs_count: number
    missing_files_count: number
    tier_distribution: Record<CqsTier, number>
    audit_duration_seconds: number
    executed_at: string
  }
  author_desyncs: AuthorSortDesync[]
  cover_anomalies: Audit360CoverAnomaly[]
  path_desyncs: PathDesync[]
  hygiene: {
    empty_authors: string[]
    empty_series: string[]
    empty_tags: string[]
  }
}

export interface SeriesGapBook {
  id: number
  title: string
  series_index: number
}

export interface SeriesGap {
  series_id: number
  series_name: string
  authors: string
  total_owned: number
  owned_indices: number[]
  missing_indices: number[]
  books: SeriesGapBook[]
}

export interface DuplicateCluster {
  cluster_type: 'multi_format_duplicate' | 'isbn_collision' | 'cross_language_work'
  confidence: number
  primary_book_id: number
  duplicate_book_ids: number[]
  title: string
  author: string
  formats_by_book: Record<number, string[]>
  recommendation: 'MERGE_FORMATS' | 'LINK_EDITIONS' | 'INSPECT_MANUALLY'
  details: {
    isbn?: string
    shared_formats?: string[]
    discrepancies?: string[]
  }
}

export interface SystemHealth {
  status: 'ready' | 'degraded' | 'offline'
  certificate: string
  read_only: boolean
  library_mounted: boolean
  library_path?: string
  db_connected: boolean
  calibre_web_connected?: boolean
}

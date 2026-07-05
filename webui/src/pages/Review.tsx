import { useEffect, useState } from 'react'
import {
  Check,
  X,
  ShieldAlert,
  Loader2,
  Sparkles,
  HelpCircle,
  CheckCircle2,
  AlertTriangle,
  Eye,
} from 'lucide-react'
import { fetchBooks, fetchEvidence, approvePatch, rejectPatch, applyPatches } from '../api/client'
import { useToast } from '../context/ToastContext'
import clsx from 'clsx'
import CoverImage from '../components/CoverImage'

type VerdictKind = 'confirmed' | 'mismatch' | 'missing' | 'ambiguous'

interface FieldVerdict {
  field: string
  declared_value: unknown
  observed_value: unknown | null
  verdict: VerdictKind
  confidence: number
  evidence: Array<{
    source: string
    text: string
    page_range?: string | null
    confidence: number
  }>
  requires_review?: boolean
  risk_flags?: string[]
  reason?: string | null
}

interface BookVerdict {
  book_key: string
  field_verdicts: Record<string, FieldVerdict>
  overall_confidence: number
  risk_flags: string[]
  action: string
  auto_apply_eligible: boolean
  proposed_patch: Record<string, unknown>
  reasons: string[]
}

const VERDICT_META: Record<VerdictKind, { label: string; color: string; icon: typeof Check }> = {
  confirmed: {
    label: 'Confirmed',
    color: 'text-emerald-400 bg-emerald-950/30 border-emerald-900/40',
    icon: CheckCircle2,
  },
  mismatch: {
    label: 'Mismatch',
    color: 'text-rose-400 bg-rose-950/30 border-rose-900/40',
    icon: AlertTriangle,
  },
  missing: {
    label: 'Missing',
    color: 'text-amber-400 bg-amber-950/30 border-amber-900/40',
    icon: HelpCircle,
  },
  ambiguous: {
    label: 'Ambiguous',
    color: 'text-purple-400 bg-purple-950/30 border-purple-900/40',
    icon: Eye,
  },
}

export default function Review() {
  const [books, setBooks] = useState<any[]>([])
  const [loading, setLoading] = useState(true)
  const [selectedBook, setSelectedBook] = useState<any | null>(null)
  const [evidence, setEvidence] = useState<any | null>(null)
  const [bookVerdict, setBookVerdict] = useState<BookVerdict | null>(null)
  const [applying, setApplying] = useState(false)

  const { showToast } = useToast()

  useEffect(() => {
    loadBooks()
  }, [])

  const loadBooks = async () => {
    setLoading(true)
    try {
      const data = await fetchBooks()
      if (data && Array.isArray(data.data)) {
        const reviewable = data.data.filter(
          (b: any) =>
            b.status === 'needs_review' ||
            b.status === 'suggest_fix' ||
            b.status === 'audited'
        )
        setBooks(reviewable)
      } else {
        setBooks([])
      }
    } catch (e) {
      showToast('Failed to load books', 'error')
      setBooks([])
    } finally {
      setLoading(false)
    }
  }

  const handleApplyAll = async () => {
    const approved = books.filter((b) => b.status === 'suggest_fix')
    if (approved.length === 0) {
      showToast('No approved patches to apply', 'error')
      return
    }
    const confirmed = window.confirm(
      `Apply ${approved.length} approved metadata patch(es) to your Calibre library? ` +
        'OPF backups will be created first. This cannot be undone except via Changes & Undo.'
    )
    if (!confirmed) return

    setApplying(true)
    try {
      const res = await applyPatches(true)
      showToast(res.data?.message || 'Approved patches applied successfully', 'success')
      loadBooks()
    } catch (e: any) {
      showToast(e.message || 'Failed to apply approved patches', 'error')
    } finally {
      setApplying(false)
    }
  }

  const handleSelectBook = async (book: any) => {
    setSelectedBook(book)
    setEvidence(null)
    setBookVerdict(null)
    try {
      const data = await fetchEvidence(book.book_key)
      setEvidence(data.data)
      // BookVerdict is exposed via a separate endpoint in v1.0; fall back to legacy if absent.
      try {
        const verdictRes = await fetch(`/api/books/${encodeURIComponent(book.book_key)}/verdict`)
        if (verdictRes.ok) {
          const v = await verdictRes.json()
          setBookVerdict(v.data)
        }
      } catch {
        // legacy evidence-only mode
      }
    } catch (e) {
      showToast('Failed to load book evidence details', 'error')
      console.error(e)
    }
  }

  const handleApprove = async (bookKey: string) => {
    try {
      await approvePatch(bookKey)
      setBooks((prev) => prev.filter((b) => b.book_key !== bookKey))
      setSelectedBook(null)
      showToast('Metadata patch approved and queued for application', 'success')
    } catch (e) {
      showToast('Failed to approve changes', 'error')
    }
  }

  const handleReject = async (bookKey: string) => {
    try {
      await rejectPatch(bookKey)
      setBooks((prev) => prev.filter((b) => b.book_key !== bookKey))
      setSelectedBook(null)
      showToast('Metadata patch rejected and removed from queue', 'info')
    } catch (e) {
      showToast('Failed to reject changes', 'error')
    }
  }

  const renderBookVerdict = () => {
    if (!bookVerdict) return null
    const fields = Object.values(bookVerdict.field_verdicts)
    if (fields.length === 0) return null

    return (
      <div className="glass-card p-6 rounded-2xl space-y-4">
        <div className="flex items-center justify-between border-b border-slate-800/40 pb-3">
          <div className="flex items-center gap-2 text-slate-200 font-bold">
            <ShieldAlert className="w-5 h-5 text-purple-400" />
            Per-Field Content Verdicts (v1.0)
          </div>
          <div className="flex items-center gap-2 text-xs">
            <span
              className={clsx(
                'px-2.5 py-1 rounded-md font-mono uppercase tracking-wider border',
                bookVerdict.auto_apply_eligible
                  ? 'text-emerald-300 bg-emerald-950/30 border-emerald-900/40'
                  : 'text-slate-400 bg-slate-900/40 border-slate-800/60'
              )}
            >
              {bookVerdict.auto_apply_eligible ? 'auto-apply ready' : 'manual review'}
            </span>
            <span className="text-slate-500 font-mono">
              conf {bookVerdict.overall_confidence}
            </span>
          </div>
        </div>

        <div className="space-y-3">
          {fields.map((fv) => {
            const meta = VERDICT_META[fv.verdict as VerdictKind] || VERDICT_META.ambiguous
            const Icon = meta.icon
            return (
              <div
                key={fv.field}
                className={clsx(
                  'rounded-xl border p-4 transition-all',
                  meta.color
                )}
              >
                <div className="flex items-start gap-3">
                  <Icon className="w-4 h-4 mt-0.5 shrink-0" />
                  <div className="flex-1 min-w-0">
                    <div className="flex items-center gap-2 mb-1">
                      <span className="font-mono text-xs uppercase tracking-wider font-bold">
                        {fv.field}
                      </span>
                      <span className="text-[10px] opacity-70 font-mono">
                        conf {fv.confidence}
                      </span>
                      {fv.risk_flags && fv.risk_flags.length > 0 && (
                        <span className="text-[10px] font-mono px-1.5 py-0.5 rounded bg-rose-900/40 text-rose-300">
                          {fv.risk_flags.join(', ')}
                        </span>
                      )}
                    </div>

                    {fv.verdict === 'confirmed' && (
                      <p className="text-sm text-slate-300 font-mono break-words">
                        {formatValue(fv.declared_value)} ✓ matches book content
                      </p>
                    )}
                    {(fv.verdict === 'mismatch' || fv.verdict === 'missing') && (
                      <div className="space-y-1.5 text-sm font-mono">
                        <div className="text-rose-300 line-through">
                          declared: {formatValue(fv.declared_value)}
                        </div>
                        <div className="text-emerald-300 font-semibold">
                          observed: {formatValue(fv.observed_value)}
                        </div>
                      </div>
                    )}
                    {fv.verdict === 'ambiguous' && (
                      <p className="text-sm text-slate-300 font-mono">
                        cannot decide deterministically — observed:{' '}
                        {formatValue(fv.observed_value) || '∅'}
                      </p>
                    )}

                    {fv.evidence && fv.evidence.length > 0 && (
                      <div className="mt-2 pt-2 border-t border-current/20">
                        <p className="text-[10px] uppercase tracking-wider opacity-70 mb-1 font-bold">
                          Evidence ({fv.evidence.length}):
                        </p>
                        <ul className="space-y-1">
                          {fv.evidence.map((ev, idx) => (
                            <li
                              key={idx}
                              className="text-xs font-mono opacity-90 flex items-start gap-2"
                            >
                              <span className="opacity-50 shrink-0">
                                [{ev.source}
                                {ev.page_range ? ` ${ev.page_range}` : ''}]
                              </span>
                              <span className="break-words">"{ev.text}"</span>
                            </li>
                          ))}
                        </ul>
                      </div>
                    )}

                    {fv.reason && (
                      <p className="mt-2 text-[11px] opacity-70 italic">{fv.reason}</p>
                    )}
                  </div>
                </div>
              </div>
            )
          })}
        </div>

        {bookVerdict.proposed_patch && Object.keys(bookVerdict.proposed_patch).length > 0 && (
          <div className="pt-4 border-t border-slate-800/40">
            <h4 className="text-xs font-bold uppercase tracking-wider text-slate-400 mb-2">
              Proposed Patch (auto-applied if eligible)
            </h4>
            <pre className="text-xs font-mono bg-slate-950/40 rounded-lg p-3 overflow-x-auto">
              {JSON.stringify(bookVerdict.proposed_patch, null, 2)}
            </pre>
          </div>
        )}
      </div>
    )
  }

  return (
    <div className="flex h-full min-h-[calc(100vh-2rem)] page-transition">
      {/* Sidebar Queue */}
      <div className="w-80 border-r border-slate-800/40 bg-[#070b13]/80 flex flex-col backdrop-blur-md">
        <header className="p-4 border-b border-slate-800/40 space-y-3">
          <div className="flex items-center justify-between">
            <h1 className="text-md font-bold flex items-center gap-2 text-slate-200">
              <ShieldAlert className="w-5 h-5 text-purple-500 drop-shadow-[0_0_6px_rgba(139,92,246,0.3)]" />
              Review Queue
            </h1>
            <p className="text-[10px] text-slate-500 uppercase tracking-widest font-mono">
              {books.length} items
            </p>
          </div>

          <button
            onClick={handleApplyAll}
            disabled={applying || books.length === 0}
            className="w-full flex items-center justify-center gap-2 py-2 bg-gradient-to-r from-emerald-600 to-teal-600 hover:from-emerald-500 hover:to-teal-500 disabled:opacity-40 disabled:cursor-not-allowed text-white text-xs font-bold rounded-xl transition-all duration-300 shadow-[0_0_10px_rgba(16,185,129,0.15)] cursor-pointer"
          >
            {applying ? (
              <>
                <Loader2 className="w-3.5 h-3.5 animate-spin" />
                Applying fixes...
              </>
            ) : (
              <>
                <Sparkles className="w-3.5 h-3.5" />
                Apply Approved Fixes
              </>
            )}
          </button>
        </header>

        <div className="flex-1 overflow-y-auto divide-y divide-slate-800/30">
          {loading ? (
            <div className="p-12 flex justify-center">
              <Loader2 className="animate-spin text-purple-500 w-6 h-6" />
            </div>
          ) : Array.isArray(books) && books.length > 0 ? (
            books.map((book) => (
              <button
                key={book.book_key}
                onClick={() => handleSelectBook(book)}
                className={clsx(
                  'w-full text-left p-4 hover:bg-slate-900/30 transition-all duration-300 relative border-l-2',
                  selectedBook?.book_key === book.book_key
                    ? 'bg-purple-950/15 border-l-purple-500 text-purple-200'
                    : 'border-l-transparent text-slate-300 hover:text-slate-100'
                )}
              >
                <p className="font-semibold text-sm line-clamp-1">
                  {book.current_metadata.title}
                </p>
                <p className="text-xs text-slate-500 mt-1 line-clamp-1">
                  by {book.current_metadata.authors.join(', ')}
                </p>
              </button>
            ))
          ) : (
            <div className="p-8 text-center text-xs text-slate-500 italic">
              All caught up! Queue is empty.
            </div>
          )}
        </div>
      </div>

      {/* Main Review Panel */}
      <div className="flex-1 bg-[#090d16]/30 overflow-y-auto relative">
        {selectedBook ? (
          <div className="p-8 max-w-5xl mx-auto space-y-8 animate-in fade-in duration-300">
            {/* Header / Actions */}
            <div className="flex flex-col sm:flex-row sm:items-center justify-between gap-4 border-b border-slate-800/40 pb-6">
              <div className="flex gap-4">
                <CoverImage bookKey={selectedBook.book_key} className="w-16 h-20 shrink-0" />
                <div className="flex flex-col justify-center">
                  <h2 className="text-2xl font-extrabold text-slate-100 tracking-tight">
                    {selectedBook.current_metadata.title}
                  </h2>
                  <p className="text-slate-400 text-sm mt-1">
                    by {selectedBook.current_metadata.authors.join(', ')}
                  </p>
                </div>
              </div>

              <div className="flex items-center gap-3 shrink-0">
                <button
                  onClick={() => handleReject(selectedBook.book_key)}
                  className="flex items-center gap-2 px-4.5 py-2.5 bg-slate-900 hover:bg-slate-800/80 border border-slate-800/80 text-rose-400 rounded-xl transition-all duration-200 font-semibold text-sm cursor-pointer"
                >
                  <X className="w-4 h-4" />
                  Reject
                </button>
                <button
                  onClick={() => handleApprove(selectedBook.book_key)}
                  className="flex items-center gap-2 px-5 py-2.5 bg-gradient-to-r from-purple-600 to-indigo-600 hover:from-purple-500 hover:to-indigo-500 text-white rounded-xl transition-all duration-200 shadow-[0_0_12px_rgba(139,92,246,0.2)] font-semibold text-sm cursor-pointer"
                >
                  <Check className="w-4 h-4" />
                  Approve Changes
                </button>
              </div>
            </div>

            {/* v1.0 per-field verdicts (NEW) */}
            {renderBookVerdict()}

            {/* Legacy v0.9 evidence (fallback) */}
            {evidence && !bookVerdict && (
              <div className="glass-card p-6 rounded-2xl space-y-4">
                <div className="flex items-center gap-2 text-slate-200 font-bold border-b border-slate-800/40 pb-3">
                  <Sparkles className="w-5 h-5 text-purple-400" />
                  Proposed Changes Summary
                </div>
                <pre className="text-xs font-mono bg-slate-950/40 rounded-lg p-3 overflow-x-auto">
                  {JSON.stringify(evidence, null, 2)}
                </pre>
              </div>
            )}
          </div>
        ) : (
          <div className="flex h-full items-center justify-center text-slate-500 text-sm italic">
            Select a book from the queue to review.
          </div>
        )}
      </div>
    </div>
  )
}

function formatValue(v: unknown): string {
  if (v === null || v === undefined) return '∅'
  if (Array.isArray(v)) return v.join(', ')
  if (typeof v === 'object') return JSON.stringify(v)
  return String(v)
}
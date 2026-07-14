import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import {
  AlertTriangle,
  ArrowLeft,
  ArrowRight,
  BookOpenCheck,
  CheckCircle2,
  Clock3,
  FileCheck2,
  Fingerprint,
  Loader2,
  LockKeyhole,
  RefreshCw,
  ShieldAlert,
  ShieldCheck,
} from 'lucide-react'
import clsx from 'clsx'
import {
  authorizeReviewV2,
  fetchOperationV2,
  fetchReviewV2,
  fetchReviewV2Detail,
  queueReviewV2,
  type EvidencePackageV2,
  type IdentityTier,
  type OperationStatusV2,
  type ReviewV2Detail,
  type ReviewV2Summary,
} from '../api/client'
import { useToast } from '../context/ToastContext'

const PAGE_SIZE = 50
const TERMINAL_OPERATION_STATES = new Set([
  'succeeded',
  'restored',
  'restore_failed',
  'unknown',
  'failed',
  'cancelled',
])

const tierStyle: Record<IdentityTier, string> = {
  A: 'border-emerald-500/40 bg-emerald-950/40 text-emerald-300',
  B: 'border-amber-500/40 bg-amber-950/35 text-amber-300',
  C: 'border-rose-500/40 bg-rose-950/35 text-rose-300',
}

function titleOf(metadata: Record<string, unknown>): string {
  return typeof metadata.title === 'string' && metadata.title.trim() ? metadata.title : 'Untitled record'
}

function authorsOf(metadata: Record<string, unknown>): string {
  if (Array.isArray(metadata.authors)) {
    const authors = metadata.authors.map(String).filter(Boolean)
    if (authors.length) return authors.join(', ')
  }
  if (typeof metadata.authors === 'string' && metadata.authors.trim()) return metadata.authors
  return 'Unknown author'
}

function formatValue(value: unknown): string {
  if (value === null || value === undefined || value === '') return '∅'
  if (Array.isArray(value)) return value.map(String).join(', ')
  if (typeof value === 'object') return JSON.stringify(value)
  return String(value)
}

function currentPatchValue(packageData: EvidencePackageV2, field: string): unknown {
  if (field === 'edition_statement') {
    return packageData.snapshot.current_metadata['#edition']
      ?? packageData.snapshot.current_metadata.edition_statement
  }
  return packageData.snapshot.current_metadata[field]
}

function errorMessage(error: unknown, fallback: string): string {
  return error instanceof Error ? error.message : fallback
}

export default function Review() {
  const [items, setItems] = useState<ReviewV2Summary[]>([])
  const [total, setTotal] = useState(0)
  const [offset, setOffset] = useState(0)
  const [tierFilter, setTierFilter] = useState<'all' | IdentityTier>('all')
  const [loading, setLoading] = useState(true)
  const [selectedEvidenceId, setSelectedEvidenceId] = useState<string | null>(null)
  const [detail, setDetail] = useState<ReviewV2Detail | null>(null)
  const [detailLoading, setDetailLoading] = useState(false)
  const [reason, setReason] = useState('')
  const [authorizationId, setAuthorizationId] = useState<string | null>(null)
  const [operation, setOperation] = useState<OperationStatusV2 | null>(null)
  const [authorizing, setAuthorizing] = useState(false)
  const [queueing, setQueueing] = useState(false)
  const [refreshingOperation, setRefreshingOperation] = useState(false)
  const detailRequestId = useRef(0)
  const { showToast } = useToast()

  const loadQueue = useCallback(async () => {
    setLoading(true)
    try {
      const response = await fetchReviewV2({
        tier: tierFilter === 'all' ? undefined : tierFilter,
        limit: PAGE_SIZE,
        offset,
      })
      setItems(response.data)
      setTotal(response.meta.total)
    } catch (error) {
      setItems([])
      setTotal(0)
      showToast(errorMessage(error, 'Failed to load Manifestation V2 review queue'), 'error')
    } finally {
      setLoading(false)
    }
  }, [offset, showToast, tierFilter])

  useEffect(() => {
    void loadQueue()
  }, [loadQueue])

  const selectEvidence = async (evidenceId: string) => {
    const requestId = detailRequestId.current + 1
    detailRequestId.current = requestId
    setSelectedEvidenceId(evidenceId)
    setDetail(null)
    setReason('')
    setAuthorizationId(null)
    setOperation(null)
    setDetailLoading(true)
    try {
      const loaded = await fetchReviewV2Detail(evidenceId)
      if (detailRequestId.current !== requestId) return
      setDetail(loaded)
      setAuthorizationId(loaded.authorization?.authorization_id ?? null)
      setOperation(loaded.operation)
    } catch (error) {
      if (detailRequestId.current !== requestId) return
      showToast(errorMessage(error, 'Failed to load sealed V2 evidence'), 'error')
    } finally {
      if (detailRequestId.current === requestId) setDetailLoading(false)
    }
  }

  const packageData = detail?.package.evidence_id === selectedEvidenceId ? detail.package : null
  const patchEntries = useMemo(
    () => Object.entries(packageData?.identity.auto_patch ?? {}),
    [packageData],
  )
  const isTierA = packageData?.identity.tier === 'A'
  const operationIsTerminal = operation ? TERMINAL_OPERATION_STATES.has(operation.state) : false
  const canAuthorize = Boolean(isTierA && patchEntries.length > 0 && !operation)
  const canQueue = Boolean(isTierA && authorizationId && !operation)

  const authorize = async () => {
    if (!packageData || !canAuthorize || !reason.trim()) return
    setAuthorizing(true)
    try {
      const result = await authorizeReviewV2(packageData.evidence_id, reason.trim())
      setAuthorizationId(result.authorization_id)
      showToast('Exact sealed patch authorized. It has not been queued yet.', 'success')
    } catch (error) {
      showToast(errorMessage(error, 'Authorization failed'), 'error')
    } finally {
      setAuthorizing(false)
    }
  }

  const queueWrite = async () => {
    if (!packageData || !authorizationId || !canQueue) return
    setQueueing(true)
    try {
      const result = await queueReviewV2(packageData.evidence_id, authorizationId)
      const now = new Date().toISOString()
      setOperation({
        operation_id: result.operation_id,
        state: 'requested',
        pilot_id: result.pilot_id,
        change_id: null,
        created_at: now,
        updated_at: now,
        completed_at: null,
        error_code: null,
      })
      showToast('One supervised writer operation was queued.', 'success')
    } catch (error) {
      showToast(errorMessage(error, 'Supervised queue request failed'), 'error')
    } finally {
      setQueueing(false)
    }
  }

  const refreshOperation = async () => {
    if (!operation) return
    setRefreshingOperation(true)
    try {
      setOperation(await fetchOperationV2(operation.operation_id))
    } catch (error) {
      showToast(errorMessage(error, 'Could not refresh operation status'), 'error')
    } finally {
      setRefreshingOperation(false)
    }
  }

  return (
    <div className="page-transition min-h-[calc(100vh-2rem)] lg:flex">
      <aside className="border-b border-slate-800/50 bg-[#070b13]/85 backdrop-blur-md lg:w-[22rem] lg:shrink-0 lg:border-b-0 lg:border-r">
        <header className="space-y-4 border-b border-slate-800/50 p-5">
          <div>
            <div className="flex items-start justify-between gap-3">
              <div>
                <p className="mb-1 font-mono text-[10px] uppercase tracking-[0.22em] text-emerald-400">
                  Sealed evidence V2
                </p>
                <h1 className="flex items-center gap-2 text-lg font-bold text-slate-100">
                  <ShieldAlert className="h-5 w-5 text-purple-400" aria-hidden="true" />
                  Manifestation review
                </h1>
              </div>
              <span className="rounded-full border border-slate-700/70 bg-slate-950/50 px-2.5 py-1 font-mono text-xs text-slate-400">
                {total}
              </span>
            </div>
            <p className="mt-2 text-xs leading-5 text-slate-500">
              One evidence package, one authorization, one writer operation.
            </p>
          </div>

          <label className="block text-xs font-semibold text-slate-400">
            Identity tier
            <select
              value={tierFilter}
              onChange={(event) => {
                setTierFilter(event.target.value as 'all' | IdentityTier)
                setOffset(0)
              }}
              className="mt-1.5 w-full rounded-lg border border-slate-700/70 bg-slate-950/70 px-3 py-2 text-sm text-slate-200 outline-none focus:border-purple-500 focus:ring-2 focus:ring-purple-500/20"
            >
              <option value="all">All V2 tiers</option>
              <option value="A">Tier A — exact</option>
              <option value="B">Tier B — review only</option>
              <option value="C">Tier C — blocked</option>
            </select>
          </label>

          <div className="rounded-lg border border-slate-800 bg-slate-950/35 p-3 text-[11px] leading-5 text-slate-500">
            Legacy V1 records are historical and read-only. They remain in run history and cannot be
            authorized from this queue.
          </div>
        </header>

        <div className="max-h-[34vh] overflow-y-auto lg:max-h-[calc(100vh-19rem)]">
          {loading ? (
            <div className="flex justify-center p-12" role="status" aria-label="Loading review queue">
              <Loader2 className="h-6 w-6 animate-spin text-purple-400" />
            </div>
          ) : items.length ? (
            <div className="divide-y divide-slate-800/40">
              {items.map((item) => (
                <button
                  key={item.evidence_id}
                  type="button"
                  onClick={() => void selectEvidence(item.evidence_id)}
                  className={clsx(
                    'w-full border-l-2 p-4 text-left transition-colors',
                    selectedEvidenceId === item.evidence_id
                      ? 'border-l-purple-500 bg-purple-950/20'
                      : 'border-l-transparent hover:bg-slate-900/50',
                  )}
                >
                  <div className="flex items-start justify-between gap-3">
                    <div className="min-w-0">
                      <p className="truncate text-sm font-semibold text-slate-200">
                        {titleOf(item.current_metadata)}
                      </p>
                      <p className="mt-1 truncate text-xs text-slate-500">
                        {authorsOf(item.current_metadata)}
                      </p>
                    </div>
                    <span className={clsx('rounded border px-1.5 py-0.5 font-mono text-[10px]', tierStyle[item.tier])}>
                      Tier {item.tier}
                    </span>
                  </div>
                  <div className="mt-3 flex flex-wrap gap-1.5 font-mono text-[10px] text-slate-500">
                    <span>{item.state}</span>
                    <span aria-hidden="true">·</span>
                    <span>{item.patch_fields.length} patch fields</span>
                  </div>
                  {item.risk_flags.length > 0 && (
                    <p className="mt-2 truncate text-[11px] text-rose-400">{item.risk_flags.join(', ')}</p>
                  )}
                </button>
              ))}
            </div>
          ) : (
            <div className="p-10 text-center text-sm text-slate-500">No sealed V2 evidence matches this filter.</div>
          )}
        </div>

        <footer className="flex items-center justify-between border-t border-slate-800/50 p-3">
          <button
            type="button"
            disabled={offset === 0 || loading}
            onClick={() => setOffset((value) => Math.max(0, value - PAGE_SIZE))}
            className="rounded-lg border border-slate-800 p-2 text-slate-400 hover:text-slate-100 disabled:opacity-30"
            aria-label="Previous review page"
          >
            <ArrowLeft className="h-4 w-4" />
          </button>
          <span className="font-mono text-[10px] text-slate-500">
            {total ? `${offset + 1}–${Math.min(offset + PAGE_SIZE, total)} of ${total}` : '0 items'}
          </span>
          <button
            type="button"
            disabled={offset + PAGE_SIZE >= total || loading}
            onClick={() => setOffset((value) => value + PAGE_SIZE)}
            className="rounded-lg border border-slate-800 p-2 text-slate-400 hover:text-slate-100 disabled:opacity-30"
            aria-label="Next review page"
          >
            <ArrowRight className="h-4 w-4" />
          </button>
        </footer>
      </aside>

      <main className="min-w-0 flex-1 overflow-y-auto bg-[#090d16]/35">
        {detailLoading ? (
          <div className="flex min-h-[55vh] items-center justify-center" role="status">
            <Loader2 className="h-7 w-7 animate-spin text-purple-400" />
            <span className="sr-only">Validating sealed evidence</span>
          </div>
        ) : packageData ? (
          <div className="mx-auto max-w-6xl space-y-6 p-5 sm:p-8">
            <section className="flex flex-col gap-5 border-b border-slate-800/50 pb-6 xl:flex-row xl:items-start xl:justify-between">
              <div className="min-w-0">
                <div className="mb-3 flex flex-wrap items-center gap-2">
                  <span className={clsx('rounded-md border px-2.5 py-1 text-xs font-bold', tierStyle[packageData.identity.tier])}>
                    Tier {packageData.identity.tier}
                  </span>
                  <span className="rounded-md border border-slate-700/70 bg-slate-950/50 px-2.5 py-1 font-mono text-xs text-slate-400">
                    {packageData.state}
                  </span>
                </div>
                <h2 className="text-2xl font-extrabold tracking-tight text-slate-100 sm:text-3xl">
                  {titleOf(packageData.snapshot.current_metadata)}
                </h2>
                <p className="mt-1 text-sm text-slate-400">
                  {authorsOf(packageData.snapshot.current_metadata)}
                </p>
                <p className="mt-3 break-all font-mono text-[11px] text-slate-600">
                  {packageData.book_key} · evidence {packageData.evidence_id}
                </p>
              </div>

              <div className="w-full rounded-xl border border-slate-800/80 bg-slate-950/45 p-4 xl:max-w-md">
                <div className="flex items-center gap-2 text-sm font-semibold text-slate-200">
                  <Fingerprint className="h-4 w-4 text-cyan-400" aria-hidden="true" />
                  Sealed package
                </div>
                <p className="mt-2 break-all font-mono text-[10px] leading-5 text-slate-500">
                  {packageData.package_sha256}
                </p>
              </div>
            </section>

            {!isTierA && (
              <section className="flex gap-3 rounded-xl border border-amber-700/40 bg-amber-950/20 p-4 text-amber-200">
                <AlertTriangle className="mt-0.5 h-5 w-5 shrink-0" aria-hidden="true" />
                <div>
                  <p className="font-semibold">Tier {packageData.identity.tier} cannot be authorized or queued</p>
                  <p className="mt-1 text-sm leading-6 text-amber-300/70">
                    This package remains review-only because the exact manifestation was not corroborated.
                  </p>
                </div>
              </section>
            )}

            <section className="grid gap-6 xl:grid-cols-[1.2fr_0.8fr]">
              <div className="glass-card overflow-hidden rounded-2xl">
                <div className="flex items-center gap-2 border-b border-slate-800/50 px-5 py-4">
                  <BookOpenCheck className="h-5 w-5 text-emerald-400" aria-hidden="true" />
                  <h3 className="font-bold text-slate-200">Current versus exact patch</h3>
                </div>
                {patchEntries.length ? (
                  <div className="overflow-x-auto">
                    <table className="w-full min-w-[34rem] text-left text-sm">
                      <thead className="bg-slate-950/30 font-mono text-[10px] uppercase tracking-wider text-slate-500">
                        <tr>
                          <th className="px-5 py-3">Field</th>
                          <th className="px-5 py-3">Current</th>
                          <th className="px-5 py-3">Authorized value</th>
                        </tr>
                      </thead>
                      <tbody className="divide-y divide-slate-800/40">
                        {patchEntries.map(([field, value]) => (
                          <tr key={field}>
                            <th className="px-5 py-4 font-mono text-xs text-slate-400">{field}</th>
                            <td className="px-5 py-4 text-rose-300">{formatValue(currentPatchValue(packageData, field))}</td>
                            <td className="px-5 py-4 font-semibold text-emerald-300">{formatValue(value)}</td>
                          </tr>
                        ))}
                      </tbody>
                    </table>
                  </div>
                ) : (
                  <p className="p-5 text-sm text-slate-500">No canonical patch is permitted for this package.</p>
                )}
              </div>

              <div className="glass-card rounded-2xl p-5">
                <div className="flex items-center gap-2">
                  <ShieldCheck className="h-5 w-5 text-cyan-400" aria-hidden="true" />
                  <h3 className="font-bold text-slate-200">Manifestation identity</h3>
                </div>
                {Object.keys(packageData.identity.manifestation_ids).length ? (
                  <dl className="mt-4 space-y-3">
                    {Object.entries(packageData.identity.manifestation_ids).map(([kind, value]) => (
                      <div key={kind} className="rounded-lg border border-slate-800/70 bg-slate-950/35 p-3">
                        <dt className="font-mono text-[10px] uppercase tracking-wider text-slate-500">{kind}</dt>
                        <dd className="mt-1 break-all font-mono text-sm text-cyan-200">{value}</dd>
                      </div>
                    ))}
                  </dl>
                ) : (
                  <p className="mt-4 text-sm text-slate-500">No exact manifestation identifier was established.</p>
                )}
                {packageData.identity.reasons.length > 0 && (
                  <ul className="mt-4 space-y-2 text-xs leading-5 text-slate-400">
                    {packageData.identity.reasons.map((reasonText) => <li key={reasonText}>• {reasonText}</li>)}
                  </ul>
                )}
              </div>
            </section>

            <section className="glass-card rounded-2xl p-5">
              <div className="flex items-center gap-2">
                <FileCheck2 className="h-5 w-5 text-purple-400" aria-hidden="true" />
                <h3 className="font-bold text-slate-200">Inspected ebook formats</h3>
              </div>
              <div className="mt-4 grid gap-3">
                {packageData.formats.map((format) => (
                  <article key={format.path} className="rounded-xl border border-slate-800/70 bg-slate-950/30 p-4">
                    <div className="flex flex-wrap items-center justify-between gap-2">
                      <div className="flex items-center gap-2">
                        <span className="rounded border border-purple-500/30 bg-purple-950/30 px-2 py-0.5 font-mono text-xs text-purple-300">
                          {format.format}
                        </span>
                        <span className="text-xs text-emerald-400">{format.status}</span>
                      </div>
                      <span className="text-xs text-slate-500">{format.languages.join(', ') || 'language unknown'}</span>
                    </div>
                    <p className="mt-3 break-all font-mono text-[11px] text-slate-500">{format.path}</p>
                    <p className="mt-2 break-all font-mono text-[10px] text-slate-600">{format.sha256}</p>
                  </article>
                ))}
              </div>
            </section>

            <section className="glass-card rounded-2xl p-5">
              <h3 className="font-bold text-slate-200">Independent evidence roots</h3>
              <div className="mt-4 grid gap-3 md:grid-cols-2">
                {packageData.source_evidence.map((source) => (
                  <article key={source.evidence_id} className="rounded-xl border border-slate-800/70 bg-slate-950/30 p-4">
                    <div className="flex flex-wrap items-center gap-2">
                      <span className="font-mono text-[10px] uppercase tracking-wider text-cyan-400">{source.source_kind}</span>
                      <span className="text-[10px] text-slate-600">root {source.independence_root ?? source.root_id}</span>
                      {source.authoritative && (
                        <span className="rounded bg-emerald-950/40 px-1.5 py-0.5 text-[10px] text-emerald-300">authoritative</span>
                      )}
                    </div>
                    <p className="mt-2 text-sm text-slate-300">{source.field}</p>
                    {source.locator && <p className="mt-1 text-xs text-slate-500">{source.locator}</p>}
                    <code className="mt-3 block break-words rounded bg-black/20 p-2 text-[11px] text-slate-400">
                      value: {JSON.stringify(source.value)}
                    </code>
                  </article>
                ))}
              </div>
            </section>

            <section className="glass-card rounded-2xl border-purple-500/20 p-5">
              <div className="flex items-center gap-2">
                <LockKeyhole className="h-5 w-5 text-purple-400" aria-hidden="true" />
                <h3 className="font-bold text-slate-100">Supervised write gate</h3>
              </div>
              <p className="mt-2 max-w-3xl text-sm leading-6 text-slate-400">
                Authorization binds your reason to this package seal and canonical patch. Queueing is a separate action
                and always creates exactly one operation.
              </p>

              <div className="mt-5 grid gap-4 xl:grid-cols-[1fr_auto_auto] xl:items-end">
                <label htmlFor="authorization-reason" className="block text-sm font-semibold text-slate-300">
                  Authorization reason
                  <textarea
                    id="authorization-reason"
                    value={reason}
                    onChange={(event) => setReason(event.target.value)}
                    disabled={!canAuthorize || Boolean(authorizationId)}
                    rows={2}
                    placeholder="What exact evidence did you cross-check?"
                    className="mt-2 w-full resize-y rounded-xl border border-slate-700/80 bg-slate-950/60 px-3 py-2.5 text-sm text-slate-200 outline-none placeholder:text-slate-600 focus:border-purple-500 focus:ring-2 focus:ring-purple-500/20 disabled:opacity-50"
                  />
                </label>
                <button
                  type="button"
                  onClick={() => void authorize()}
                  disabled={!canAuthorize || !reason.trim() || Boolean(authorizationId) || authorizing}
                  className="inline-flex min-h-11 items-center justify-center gap-2 rounded-xl border border-purple-500/40 bg-purple-600/20 px-4 py-2.5 text-sm font-semibold text-purple-200 transition-colors hover:bg-purple-600/30 disabled:cursor-not-allowed disabled:opacity-35"
                >
                  {authorizing ? <Loader2 className="h-4 w-4 animate-spin" /> : <ShieldCheck className="h-4 w-4" />}
                  Authorize exact patch
                </button>
                <button
                  type="button"
                  onClick={() => void queueWrite()}
                  disabled={!canQueue || queueing}
                  className="inline-flex min-h-11 items-center justify-center gap-2 rounded-xl bg-gradient-to-r from-emerald-600 to-teal-600 px-4 py-2.5 text-sm font-bold text-white shadow-[0_0_14px_rgba(16,185,129,0.15)] transition-opacity disabled:cursor-not-allowed disabled:opacity-35"
                >
                  {queueing ? <Loader2 className="h-4 w-4 animate-spin" /> : <CheckCircle2 className="h-4 w-4" />}
                  Queue this write
                </button>
              </div>

              {authorizationId && (
                <p className="mt-4 break-all rounded-lg border border-emerald-800/40 bg-emerald-950/20 p-3 font-mono text-xs text-emerald-300">
                  Authorization {authorizationId} is bound to this exact package.
                </p>
              )}

              {operation && (
                <div className="mt-4 rounded-xl border border-cyan-800/40 bg-cyan-950/15 p-4">
                  <div className="flex flex-wrap items-center justify-between gap-3">
                    <div>
                      <p className="flex items-center gap-2 text-sm font-semibold text-cyan-200">
                        <Clock3 className="h-4 w-4" aria-hidden="true" />
                        Writer operation: {operation.state}
                      </p>
                      <p className="mt-2 break-all font-mono text-xs text-cyan-400">{operation.operation_id}</p>
                      {operation.error_code && <p className="mt-2 text-xs text-rose-300">{operation.error_code}</p>}
                      {operationIsTerminal && operation.change_id && (
                        <p className="mt-2 text-xs text-slate-400">Restore point change #{operation.change_id}</p>
                      )}
                    </div>
                    <button
                      type="button"
                      onClick={() => void refreshOperation()}
                      disabled={refreshingOperation}
                      className="inline-flex items-center gap-2 rounded-lg border border-cyan-700/40 px-3 py-2 text-xs font-semibold text-cyan-300 hover:bg-cyan-950/30 disabled:opacity-40"
                    >
                      <RefreshCw className={clsx('h-3.5 w-3.5', refreshingOperation && 'animate-spin')} />
                      Refresh status
                    </button>
                  </div>
                </div>
              )}
            </section>
          </div>
        ) : (
          <div className="flex min-h-[60vh] items-center justify-center p-8 text-center">
            <div>
              <Fingerprint className="mx-auto h-10 w-10 text-slate-700" aria-hidden="true" />
              <p className="mt-4 text-sm text-slate-500">Select a sealed V2 evidence package to inspect it.</p>
            </div>
          </div>
        )}
      </main>
    </div>
  )
}

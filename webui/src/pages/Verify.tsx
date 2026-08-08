import { useMemo, useState } from 'react'
import { useMutation, useQuery } from '@tanstack/react-query'
import { Link, useNavigate, useParams } from 'react-router-dom'
import {
  AlertTriangle,
  Ban,
  CheckCircle2,
  Clock3,
  Eye,
  FileCheck2,
  Loader2,
  OctagonX,
  Play,
  ScanSearch,
  Square,
} from 'lucide-react'
import clsx from 'clsx'

import {
  cancelVerifyRun,
  fetchVerifyRun,
  fetchVerifyRuns,
  startVerify,
  type VerifyRunStatus,
  type VerifyRunSummary,
} from '../api/client'
import { useToast } from '../context/ToastContext'

const TERMINAL_STATUSES = new Set<VerifyRunStatus>([
  'cancelled',
  'completed',
  'completed_with_errors',
  'failed',
  'source_changed',
  'blocked_recovery',
])

const STATUS_STYLE: Record<VerifyRunStatus, string> = {
  pending: 'border-cyan-700/60 bg-cyan-950/30 text-cyan-300',
  inventorying: 'border-cyan-700/60 bg-cyan-950/30 text-cyan-300',
  running: 'border-cyan-700/60 bg-cyan-950/30 text-cyan-300',
  cancelling: 'border-amber-700/60 bg-amber-950/30 text-amber-300',
  cancelled: 'border-slate-700 bg-slate-900/60 text-slate-300',
  completed: 'border-emerald-700/60 bg-emerald-950/30 text-emerald-300',
  completed_with_errors: 'border-amber-700/60 bg-amber-950/30 text-amber-300',
  failed: 'border-rose-700/60 bg-rose-950/30 text-rose-300',
  source_changed: 'border-rose-700/60 bg-rose-950/30 text-rose-300',
  blocked_recovery: 'border-rose-700/60 bg-rose-950/30 text-rose-300',
}

function errorMessage(error: unknown, fallback: string): string {
  return error instanceof Error ? error.message : fallback
}

function statusLabel(status: VerifyRunStatus): string {
  return status.replaceAll('_', ' ')
}

function progressLabel(run: VerifyRunSummary): string {
  if (run.total === null) return `${run.completed} processed · inventory pending`
  return `${run.completed}/${run.total} processed`
}

export default function Verify() {
  const { runId } = useParams<{ runId: string }>()
  const navigate = useNavigate()
  const { showToast } = useToast()
  const [limit, setLimit] = useState('50')
  const [useOcr, setUseOcr] = useState(true)
  const [calibreStopped, setCalibreStopped] = useState(false)

  const parsedLimit = limit.trim() === '' ? undefined : Number(limit)
  const limitValid = parsedLimit === undefined
    || (Number.isInteger(parsedLimit) && parsedLimit >= 1 && parsedLimit <= 10_000)

  const runsQuery = useQuery({
    queryKey: ['verifyRuns'],
    queryFn: fetchVerifyRuns,
    refetchInterval: 5_000,
  })
  const runQuery = useQuery({
    queryKey: ['verifyRun', runId],
    queryFn: () => fetchVerifyRun(runId!),
    enabled: Boolean(runId),
    refetchInterval: (query) => {
      const status = query.state.data?.status
      return status && !TERMINAL_STATUSES.has(status) ? 2_000 : false
    },
  })

  const startMutation = useMutation({
    mutationFn: () => startVerify(
      {
        ...(parsedLimit === undefined ? {} : { limit: parsedLimit }),
        use_ocr: useOcr,
        confirm_calibre_stopped: true,
      },
      `certificate-a:${crypto.randomUUID()}`,
    ),
    onSuccess: async (run) => {
      await runsQuery.refetch()
      showToast('Certificate A audit request accepted.', 'success')
      navigate(`/verify/${encodeURIComponent(run.run_id)}`)
    },
    onError: (error) => showToast(errorMessage(error, 'The audit request was rejected.'), 'error'),
  })

  const cancelMutation = useMutation({
    mutationFn: () => cancelVerifyRun(runId!),
    onSuccess: async () => {
      await Promise.all([runQuery.refetch(), runsQuery.refetch()])
      showToast('Cancellation requested. The verifier will stop at a safe boundary.', 'warning')
    },
    onError: (error) => showToast(errorMessage(error, 'The cancellation request was rejected.'), 'error'),
  })

  const runs = runsQuery.data?.runs ?? []
  const activeRun = runQuery.data
  const canCancel = activeRun ? !TERMINAL_STATUSES.has(activeRun.status) : false
  const progress = activeRun?.total ? Math.min(100, (activeRun.completed / activeRun.total) * 100) : 0
  const resultCounts = useMemo(
    () => Object.entries(activeRun?.counts ?? {}).sort(([left], [right]) => left.localeCompare(right)),
    [activeRun?.counts],
  )

  return (
    <div className="mx-auto max-w-6xl space-y-8 p-6 md:p-10">
      <header className="space-y-3 border-b border-slate-800/60 pb-7">
        <div className="flex items-center gap-3 text-cyan-300">
          <ScanSearch aria-hidden="true" className="h-7 w-7" />
          <span className="text-xs font-bold uppercase tracking-[0.25em]">Manifestation V2 · Certificate A</span>
        </div>
        <h1 className="text-3xl font-extrabold tracking-tight text-slate-50">Verify a stopped Calibre library</h1>
        <p className="max-w-3xl text-sm leading-6 text-slate-400">
          The verifier reads a frozen offline folder, hashes every selected format, and stores sealed evidence. This release cannot modify Calibre metadata.
        </p>
      </header>

      <section aria-labelledby="new-run-heading" className="glass-card space-y-5 rounded-2xl p-6">
        <div className="flex items-start gap-3">
          <Square aria-hidden="true" className="mt-0.5 h-5 w-5 text-amber-300" />
          <div>
            <h2 id="new-run-heading" className="font-bold text-slate-100">Request a bounded shadow audit</h2>
            <p className="mt-1 text-sm text-slate-400">Close Calibre Desktop and its Content Server before continuing.</p>
          </div>
        </div>

        <div className="grid gap-5 md:grid-cols-2">
          <label className="text-sm font-semibold text-slate-300">
            Maximum books
            <input
              aria-describedby="limit-help"
              type="number"
              min="1"
              max="10000"
              value={limit}
              onChange={(event) => setLimit(event.target.value)}
              className="mt-2 w-full rounded-xl border border-slate-700 bg-slate-950/70 px-4 py-3 font-mono text-slate-100 outline-none focus:border-cyan-500 focus:ring-2 focus:ring-cyan-500/20"
            />
            <span id="limit-help" className={clsx('mt-2 block text-xs font-normal', limitValid ? 'text-slate-500' : 'text-rose-400')}>
              {limitValid ? '1–10,000. Clear the field to audit the full frozen snapshot.' : 'Enter a whole number from 1 to 10,000.'}
            </span>
          </label>

          <div className="space-y-3">
            <label className="flex cursor-pointer items-start gap-3 rounded-xl border border-slate-800 bg-slate-950/40 p-4 text-sm text-slate-300">
              <input
                type="checkbox"
                checked={useOcr}
                onChange={(event) => setUseOcr(event.target.checked)}
                className="mt-0.5 h-4 w-4 accent-cyan-500"
              />
              <span><strong className="block text-slate-100">Bounded Tesseract OCR</strong><span className="mt-1 block text-xs leading-5 text-slate-500">Local-only, limited pages, and a hard process timeout.</span></span>
            </label>
          </div>
        </div>

        <label className="flex cursor-pointer items-start gap-3 rounded-xl border border-amber-700/40 bg-amber-950/20 p-4 text-sm text-amber-100">
          <input
            type="checkbox"
            checked={calibreStopped}
            onChange={(event) => setCalibreStopped(event.target.checked)}
            className="mt-0.5 h-4 w-4 accent-amber-500"
          />
          <span><strong className="block">I confirm Calibre and its Content Server are stopped.</strong><span className="mt-1 block text-xs leading-5 text-amber-200/70">If anything changes the folder during inventory, the run stops with source_changed.</span></span>
        </label>

        <div className="flex flex-wrap items-center justify-between gap-3 border-t border-slate-800 pt-5">
          <p className="flex items-center gap-2 text-xs text-slate-500"><Eye aria-hidden="true" className="h-4 w-4" /> Shadow only · exact ISBN providers · no uploads or AI services</p>
          <button
            type="button"
            onClick={() => startMutation.mutate()}
            disabled={!calibreStopped || !limitValid || startMutation.isPending}
            className="inline-flex items-center gap-2 rounded-xl bg-cyan-600 px-5 py-3 text-sm font-bold text-white hover:bg-cyan-500 disabled:cursor-not-allowed disabled:opacity-40"
          >
            {startMutation.isPending ? <Loader2 aria-hidden="true" className="h-4 w-4 animate-spin" /> : <Play aria-hidden="true" className="h-4 w-4" />}
            Request shadow audit
          </button>
        </div>
      </section>

      {runId && (
        <section aria-labelledby="run-heading" className="glass-card space-y-5 rounded-2xl border border-cyan-900/40 p-6">
          {runQuery.isLoading ? (
            <div className="flex items-center gap-3 text-sm text-slate-400" role="status"><Loader2 aria-hidden="true" className="h-5 w-5 animate-spin" /> Loading run…</div>
          ) : runQuery.isError || !activeRun ? (
            <div className="flex items-center gap-3 text-sm text-rose-300"><OctagonX aria-hidden="true" className="h-5 w-5" /> {errorMessage(runQuery.error, 'Run unavailable.')}</div>
          ) : (
            <>
              <div className="flex flex-wrap items-start justify-between gap-4">
                <div className="min-w-0">
                  <p className="text-xs uppercase tracking-wider text-slate-500">Selected run</p>
                  <h2 id="run-heading" className="mt-1 break-all font-mono text-sm font-bold text-slate-200">{activeRun.run_id}</h2>
                  <p className="mt-2 text-xs text-slate-500">Started {new Date(activeRun.started_at).toLocaleString()}</p>
                </div>
                <span className={clsx('rounded-full border px-3 py-1 font-mono text-xs', STATUS_STYLE[activeRun.status])}>{statusLabel(activeRun.status)}</span>
              </div>

              <div>
                <div className="mb-2 flex items-center justify-between text-xs text-slate-400"><span>{progressLabel(activeRun)}</span><span>{activeRun.total === null ? 'Freezing inventory' : `${Math.round(progress)}%`}</span></div>
                <progress
                  aria-label="Verification progress"
                  className="certificate-progress h-2 w-full overflow-hidden rounded-full"
                  max={100}
                  value={progress}
                />
              </div>

              {activeRun.error_code && <div className="flex items-start gap-2 rounded-xl border border-rose-800/50 bg-rose-950/25 p-4 text-sm text-rose-200"><AlertTriangle aria-hidden="true" className="mt-0.5 h-4 w-4 shrink-0" />{activeRun.error_code.replaceAll('_', ' ')}</div>}

              {resultCounts.length > 0 && (
                <dl className="grid gap-3 sm:grid-cols-2 lg:grid-cols-4">
                  {resultCounts.map(([label, count]) => <div key={label} className="rounded-xl border border-slate-800 bg-slate-950/35 p-4"><dt className="text-xs text-slate-500">{label.replaceAll('_', ' ')}</dt><dd className="mt-1 font-mono text-xl font-bold text-slate-100">{count}</dd></div>)}
                </dl>
              )}

              {activeRun.results.length > 0 && (
                <div className="overflow-x-auto rounded-xl border border-slate-800">
                  <table className="w-full text-left text-sm">
                    <thead className="bg-slate-950/70 text-xs uppercase tracking-wider text-slate-500"><tr><th className="px-4 py-3">Book</th><th className="px-4 py-3">State</th><th className="px-4 py-3">Evidence</th></tr></thead>
                    <tbody className="divide-y divide-slate-800/70">{activeRun.results.map((result) => <tr key={result.book_key}><td className="px-4 py-3 font-mono text-xs text-slate-300">{result.book_key}</td><td className="px-4 py-3 text-slate-400">{result.state.replaceAll('_', ' ')}</td><td className="px-4 py-3">{result.evidence_id ? <Link className="inline-flex items-center gap-1 text-cyan-300 hover:text-cyan-200" to={`/review/${encodeURIComponent(result.evidence_id)}`}><FileCheck2 aria-hidden="true" className="h-4 w-4" /> Open sealed evidence</Link> : <span className="text-slate-600">—</span>}</td></tr>)}</tbody>
                  </table>
                </div>
              )}

              <div className="flex flex-wrap justify-end gap-3 border-t border-slate-800 pt-4">
                <Link to={`/review?run_id=${encodeURIComponent(activeRun.run_id)}`} className="inline-flex items-center gap-2 rounded-xl border border-slate-700 px-4 py-2.5 text-sm font-semibold text-slate-300 hover:bg-slate-900"><FileCheck2 aria-hidden="true" className="h-4 w-4" /> View run evidence</Link>
                {canCancel && <button type="button" onClick={() => cancelMutation.mutate()} disabled={cancelMutation.isPending} className="inline-flex items-center gap-2 rounded-xl border border-rose-800/70 px-4 py-2.5 text-sm font-semibold text-rose-300 hover:bg-rose-950/30 disabled:opacity-40"><Ban aria-hidden="true" className="h-4 w-4" /> Request cancellation</button>}
              </div>
            </>
          )}
        </section>
      )}

      <section aria-labelledby="recent-runs-heading" className="space-y-3">
        <h2 id="recent-runs-heading" className="flex items-center gap-2 text-sm font-bold uppercase tracking-wider text-slate-300"><Clock3 aria-hidden="true" className="h-4 w-4 text-cyan-400" /> Recent Certificate A runs</h2>
        {runsQuery.isLoading ? <p className="glass-card rounded-2xl p-6 text-sm text-slate-500">Loading runs…</p> : runs.length === 0 ? <p className="glass-card rounded-2xl p-8 text-center text-sm text-slate-500">No Certificate A runs yet.</p> : (
          <div className="glass-card divide-y divide-slate-800/60 overflow-hidden rounded-2xl">
            {runs.map((run) => (
              <button key={run.run_id} type="button" onClick={() => navigate(`/verify/${encodeURIComponent(run.run_id)}`)} className={clsx('flex w-full items-center gap-4 p-4 text-left hover:bg-slate-900/50', run.run_id === runId && 'bg-cyan-950/20')}>
                {TERMINAL_STATUSES.has(run.status) ? <CheckCircle2 aria-hidden="true" className="h-5 w-5 shrink-0 text-slate-500" /> : <Loader2 aria-hidden="true" className="h-5 w-5 shrink-0 animate-spin text-cyan-400" />}
                <div className="min-w-0 flex-1"><p className="truncate font-mono text-xs text-slate-300">{run.run_id}</p><p className="mt-1 text-xs text-slate-500">{new Date(run.started_at).toLocaleString()} · {progressLabel(run)}</p></div>
                <span className={clsx('hidden rounded-full border px-2.5 py-1 font-mono text-[10px] sm:block', STATUS_STYLE[run.status])}>{statusLabel(run.status)}</span>
              </button>
            ))}
          </div>
        )}
      </section>
    </div>
  )
}

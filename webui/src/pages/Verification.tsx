import { useState } from 'react'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import {
  ScanSearch,
  ShieldCheck,
  ChevronRight,
  RefreshCw,
  Terminal,
} from 'lucide-react'
import clsx from 'clsx'

import { fetchCapabilities, fetchVerifyRuns, startVerify, fetchReviewV2 } from '../api/client'
import type { VerifyRunSummary } from '../api/client'
import { Header } from '../components/Header'
import { BentoCard } from '../components/BentoCard'
import { Badge } from '../components/Badge'

export default function Verification() {
  const [selectedRunId, setSelectedRunId] = useState<string | null>(null)
  const [confirmStopped, setConfirmStopped] = useState(false)
  const [limit, setLimit] = useState(25)
  const [useOcr, setUseOcr] = useState(false)
  const queryClient = useQueryClient()

  const capabilities = useQuery({ queryKey: ['capabilities'], queryFn: fetchCapabilities })
  const runsQuery = useQuery({ queryKey: ['verifyRuns'], queryFn: fetchVerifyRuns, refetchInterval: 5000 })
  const evidenceQuery = useQuery({
    queryKey: ['reviewEvidence', selectedRunId],
    queryFn: () => fetchReviewV2({ runId: selectedRunId ?? undefined, limit: 20 }),
    enabled: !!selectedRunId,
  })

  const startMutation = useMutation({
    mutationFn: () =>
      startVerify(
        { limit, use_ocr: useOcr, confirm_calibre_stopped: true },
        `web-run-${Date.now()}`,
      ),
    onSuccess: (data) => {
      setSelectedRunId(data.run_id)
      queryClient.invalidateQueries({ queryKey: ['verifyRuns'] })
    },
  })

  const runs: VerifyRunSummary[] = runsQuery.data?.runs ?? []
  const evidenceItems = evidenceQuery.data?.data ?? []

  return (
    <div className="flex flex-col min-h-screen">
      <Header
        title="Cryptographic Verification & Sealed Evidence"
        subtitle="Certificate A shadow-only verification with immutable SHA-256 evidence packages"
        badge="PostgreSQL Fenced"
        actions={
          <button
            onClick={() => runsQuery.refetch()}
            className="flex items-center gap-2 rounded-xl border border-slate-700 bg-slate-800/80 px-3.5 py-2 text-xs font-bold text-slate-200 hover:bg-slate-800 transition-all"
          >
            <RefreshCw className={clsx('h-3.5 w-3.5', runsQuery.isFetching && 'animate-spin text-cyan-400')} />
            <span>Refresh Runs</span>
          </button>
        }
      />

      <main className="flex-1 space-y-6 p-6 md:p-10 max-w-7xl mx-auto w-full">
        {/* Top: Start Run Form & Capabilities */}
        <div className="grid grid-cols-1 gap-6 lg:grid-cols-12">
          <BentoCard
            title="Launch Verified Shadow Run"
            subtitle="Requires confirmation that Calibre desktop / server is stopped"
            className="lg:col-span-7 space-y-4"
          >
            <div className="space-y-4 text-xs">
              <div className="flex items-start gap-3 rounded-xl border border-cyan-500/30 bg-cyan-950/20 p-3.5 text-cyan-200">
                <ShieldCheck className="h-5 w-5 shrink-0 text-cyan-400 mt-0.5" />
                <p className="leading-relaxed">
                  The verifier takes a frozen read-only snapshot of your library. In Certificate A mode,
                  all writes, file uploads, and stochastic modifications are mathematically disabled.
                </p>
              </div>

              <div className="grid grid-cols-1 sm:grid-cols-2 gap-4">
                <div>
                  <label className="block text-slate-300 font-bold mb-1">Book Limit</label>
                  <input
                    type="number"
                    min={1}
                    max={500}
                    value={limit}
                    onChange={(e) => setLimit(Number(e.target.value))}
                    className="w-full rounded-xl border border-slate-800 bg-slate-900 px-3 py-2 text-slate-100 focus:border-cyan-500/60 focus:outline-none font-mono"
                  />
                </div>

                <div className="flex items-center pt-5">
                  <label className="flex items-center gap-2 cursor-pointer text-slate-300">
                    <input
                      type="checkbox"
                      checked={useOcr}
                      onChange={(e) => setUseOcr(e.target.checked)}
                      className="rounded border-slate-700 bg-slate-900 text-cyan-500 focus:ring-0"
                    />
                    <span className="font-semibold">Enable Bounded Tesseract OCR</span>
                  </label>
                </div>
              </div>

              <div className="pt-2">
                <label className="flex items-center gap-2 cursor-pointer text-slate-300">
                  <input
                    type="checkbox"
                    checked={confirmStopped}
                    onChange={(e) => setConfirmStopped(e.target.checked)}
                    className="rounded border-slate-700 bg-slate-900 text-cyan-500 focus:ring-0"
                  />
                  <span className="font-semibold text-amber-300">
                    I confirm Calibre is stopped and metadata.db will not be written to concurrently.
                  </span>
                </label>
              </div>

              <div className="pt-3 border-t border-slate-800 flex justify-end">
                <button
                  onClick={() => startMutation.mutate()}
                  disabled={!confirmStopped || startMutation.isPending}
                  className="flex items-center gap-2 rounded-xl bg-cyan-600 px-5 py-2.5 text-xs font-bold text-white hover:bg-cyan-500 disabled:opacity-40 disabled:pointer-events-none transition-all shadow-lg shadow-cyan-950/30"
                >
                  <ScanSearch className="h-4 w-4" />
                  <span>{startMutation.isPending ? 'Requesting Run...' : 'Start Shadow Verification'}</span>
                </button>
              </div>
            </div>
          </BentoCard>

          {/* Capabilities Summary */}
          <BentoCard
            title="Fencing & Security Contract"
            subtitle="Certificate A runtime boundaries"
            className="lg:col-span-5 space-y-3"
          >
            <dl className="space-y-2.5 text-xs">
              <div className="flex items-center justify-between border-b border-slate-800/60 pb-2">
                <dt className="text-slate-400">Security Certificate</dt>
                <dd className="font-mono font-bold text-emerald-400">Certificate A (Verified)</dd>
              </div>
              <div className="flex items-center justify-between border-b border-slate-800/60 pb-2">
                <dt className="text-slate-400">Database Role</dt>
                <dd className="font-mono text-slate-200">PostgreSQL (Separated Worker)</dd>
              </div>
              <div className="flex items-center justify-between border-b border-slate-800/60 pb-2">
                <dt className="text-slate-400">Pipeline Mode</dt>
                <dd className="font-mono text-cyan-300">
                  {capabilities.data?.pipeline ?? 'manifestation-v2'}
                </dd>
              </div>
              <div className="flex items-center justify-between border-b border-slate-800/60 pb-2">
                <dt className="text-slate-400">Authoritative Providers</dt>
                <dd className="font-mono text-slate-300">
                  {capabilities.data?.providers?.join(' + ') ?? 'Google Books + OpenLibrary'}
                </dd>
              </div>
              <div className="flex items-center justify-between">
                <dt className="text-slate-400">External Writes</dt>
                <dd className="font-mono font-bold text-rose-400">Strictly Disabled (Fails Closed)</dd>
              </div>
            </dl>
          </BentoCard>
        </div>

        {/* Verification Runs Table */}
        <BentoCard
          title="Recent Verification Runs"
          subtitle="Select a run to inspect individual cryptographic evidence packages"
        >
          {runs.length === 0 ? (
            <div className="flex flex-col items-center justify-center py-12 text-center">
              <Terminal className="h-10 w-10 text-slate-600 mb-2" />
              <p className="text-sm font-bold text-slate-300">No Verification Runs Recorded</p>
              <p className="text-xs text-slate-400 mt-1">Start a shadow audit above to generate sealed evidence packages.</p>
            </div>
          ) : (
            <div className="overflow-x-auto">
              <table className="w-full text-left text-xs font-mono">
                <thead>
                  <tr className="border-b border-slate-800 text-slate-400 font-semibold uppercase tracking-wider text-[10px]">
                    <th className="py-3 px-4 font-sans">Run ID</th>
                    <th className="py-3 px-4 font-sans">Status</th>
                    <th className="py-3 px-4 font-sans">Progress</th>
                    <th className="py-3 px-4 font-sans">Started</th>
                    <th className="py-3 px-4 text-right font-sans">Action</th>
                  </tr>
                </thead>
                <tbody className="divide-y divide-slate-800/60">
                  {runs.map((r) => {
                    const isSelected = selectedRunId === r.run_id
                    return (
                      <tr
                        key={r.run_id}
                        onClick={() => setSelectedRunId(r.run_id)}
                        className={clsx(
                          'cursor-pointer transition-colors',
                          isSelected ? 'bg-cyan-500/10' : 'hover:bg-slate-900/40',
                        )}
                      >
                        <td className="py-3 px-4 font-bold text-slate-200">{r.run_id}</td>
                        <td className="py-3 px-4">
                          <Badge
                            variant={
                              r.status === 'completed'
                                ? 'emerald'
                                : r.status === 'running'
                                  ? 'cyan'
                                  : 'rose'
                            }
                          >
                            {r.status.toUpperCase()}
                          </Badge>
                        </td>
                        <td className="py-3 px-4 text-slate-300">
                          {r.completed} / {r.total ?? '?'} books
                        </td>
                        <td className="py-3 px-4 text-slate-400">
                          {new Date(r.started_at).toLocaleTimeString()}
                        </td>
                        <td className="py-3 px-4 text-right">
                          <button className="text-cyan-400 hover:text-cyan-300 font-bold flex items-center gap-1 ml-auto font-sans">
                            <span>Inspect</span>
                            <ChevronRight className="h-3 w-3" />
                          </button>
                        </td>
                      </tr>
                    )
                  })}
                </tbody>
              </table>
            </div>
          )}
        </BentoCard>

        {/* Evidence Inspector Drawer / Section */}
        {selectedRunId && (
          <BentoCard
            title={`Evidence Packages: ${selectedRunId}`}
            subtitle="Sealed cryptographically verified packages with ISBN check digit matching"
          >
            {evidenceItems.length === 0 ? (
              <p className="text-xs text-slate-400 py-6 text-center">No evidence records available for this run.</p>
            ) : (
              <div className="grid grid-cols-1 md:grid-cols-2 gap-3">
                {evidenceItems.map((item) => (
                  <div
                    key={item.evidence_id}
                    className="rounded-xl border border-slate-800 bg-slate-950/60 p-4 space-y-2.5 font-mono text-xs"
                  >
                    <div className="flex items-center justify-between">
                      <span className="font-bold text-slate-200 font-sans">{String(item.current_metadata?.title ?? item.book_key)}</span>
                      <Badge variant="cyan">{item.tier}</Badge>
                    </div>
                    <div className="text-[11px] text-slate-400">
                      <p>Book Key: {item.book_key}</p>
                      <p className="truncate text-slate-400">SHA-256: {item.evidence_id}</p>
                    </div>
                  </div>
                ))}
              </div>
            )}
          </BentoCard>
        )}
      </main>
    </div>
  )
}

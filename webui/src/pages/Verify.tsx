import { useState } from 'react'
import { useQuery, useMutation } from '@tanstack/react-query'
import {
  Play,
  Loader2,
  CheckCircle2,
  Sparkles,
  Clock,
} from 'lucide-react'
import {
  startVerify,
  fetchVerifyRuns,
  fetchVerifyRun,
  type VerifyRunSummary,
} from '../api/client'
import { useToast } from '../context/ToastContext'
import clsx from 'clsx'

const ACTION_COLORS: Record<string, string> = {
  shadowed: 'text-emerald-400 bg-emerald-950/30 border-emerald-900/40',
  review: 'text-amber-400 bg-amber-950/30 border-amber-900/40',
  deferred: 'text-purple-400 bg-purple-950/30 border-purple-900/40',
  failed: 'text-rose-400 bg-rose-950/30 border-rose-900/40',
  blocked_recovery: 'text-rose-400 bg-rose-950/30 border-rose-900/40',
}

const ACTION_LABELS: Record<string, string> = {
  shadowed: 'Tier A shadowed',
  review: 'Needs review',
  deferred: 'Conflict / deferred',
  failed: 'Failed',
  blocked_recovery: 'Recovery blocked',
}

export default function Verify() {
  const [activeRunId, setActiveRunId] = useState<string | null>(null)
  const [limit, setLimit] = useState<number>(50)
  const [useLlm, setUseLlm] = useState<boolean>(false)
  const { showToast } = useToast()

  const runsQuery = useQuery({
    queryKey: ['verifyRuns'],
    queryFn: fetchVerifyRuns,
    refetchInterval: 5000,
  })

  const activeRunQuery = useQuery({
    queryKey: ['verifyRun', activeRunId],
    queryFn: () => fetchVerifyRun(activeRunId!),
    enabled: !!activeRunId,
    refetchInterval: 1500,
  })

  const startMutation = useMutation({
    mutationFn: () => startVerify({
      pipeline: 'v2',
      limit,
      use_llm: useLlm,
      use_ocr: true,
      use_vision: false,
      allow_remote_text: false,
      allow_remote_images: false,
    }),
    onSuccess: (res) => {
      setActiveRunId(res.data.run_id)
      showToast(`Verify run started: ${res.data.total} books`, 'success')
      runsQuery.refetch()
    },
    onError: (e: any) => {
      showToast(e.message ?? 'Failed to start verify', 'error')
    },
  })

  const runs: VerifyRunSummary[] = runsQuery.data?.data?.runs ?? []
  const activeRun = activeRunQuery.data?.data

  return (
    <div className="p-8 max-w-6xl mx-auto space-y-8 page-transition">
      {/* Header */}
      <div className="flex items-center justify-between border-b border-slate-800/40 pb-6">
        <div className="flex items-center gap-4">
          <Sparkles className="w-7 h-7 text-purple-400 drop-shadow-[0_0_8px_rgba(139,92,246,0.5)]" />
          <div>
            <h1 className="text-2xl font-extrabold text-slate-100 tracking-tight">
              Verify Library
            </h1>
            <p className="text-sm text-slate-400 mt-1">
              Manifestation V2 — every attached format, exact-edition evidence, shadow-only decisions
            </p>
          </div>
        </div>
      </div>

      {/* Start panel */}
      <div className="glass-card p-6 rounded-2xl space-y-4">
        <h2 className="text-sm font-bold uppercase tracking-wider text-slate-300">
          Start a new verify run
        </h2>
        <div className="flex flex-wrap items-end gap-4">
          <div className="flex flex-col gap-1">
            <label className="text-xs font-mono text-slate-400 uppercase">
              Limit
            </label>
            <input
              type="number"
              min={1}
              max={100000}
              value={limit}
              onChange={(e) => setLimit(Number(e.target.value))}
              className="bg-slate-950/60 border border-slate-800 rounded-md px-3 py-2 text-sm font-mono w-32 text-slate-100"
            />
          </div>
          <label className="flex items-center gap-2 text-sm text-slate-300 select-none cursor-pointer">
            <input
              type="checkbox"
              checked={useLlm}
              onChange={(e) => setUseLlm(e.target.checked)}
              className="w-4 h-4 accent-purple-500"
            />
            Use local LLM transcription witness (non-authoritative)
          </label>
          <button
            onClick={() => startMutation.mutate()}
            disabled={startMutation.isPending}
            className="ml-auto flex items-center gap-2 px-6 py-2.5 bg-gradient-to-r from-purple-600 to-indigo-600 hover:from-purple-500 hover:to-indigo-500 disabled:opacity-40 disabled:cursor-not-allowed text-white text-sm font-bold rounded-xl transition-all duration-300 shadow-[0_0_10px_rgba(139,92,246,0.25)] cursor-pointer"
          >
            {startMutation.isPending ? (
              <>
                <Loader2 className="w-4 h-4 animate-spin" />
                Starting…
              </>
            ) : (
              <>
                <Play className="w-4 h-4" />
                Run Manifestation V2
              </>
            )}
          </button>
        </div>
      </div>

      {/* Live run panel */}
      {activeRun && (
        <div className="glass-card p-6 rounded-2xl space-y-4 border-purple-500/30">
          <div className="flex items-center justify-between">
            <h2 className="text-sm font-bold uppercase tracking-wider text-slate-300 flex items-center gap-2">
              {activeRun.status === 'completed' ? (
                <CheckCircle2 className="w-4 h-4 text-emerald-400" />
              ) : (
                <Loader2 className="w-4 h-4 text-purple-400 animate-spin" />
              )}
              Run: {activeRun.run_id}
            </h2>
            <span className="text-xs font-mono text-slate-500">
              {activeRun.status} · {activeRun.completed}/{activeRun.total}
            </span>
          </div>

          {/* Progress bar */}
          <div className="w-full bg-slate-950/60 rounded-full h-2 overflow-hidden border border-slate-800/40">
            <div
              className="h-full bg-gradient-to-r from-purple-600 to-cyan-500 transition-all duration-500 ease-out"
              style={{
                width: `${activeRun.total > 0 ? (activeRun.completed * 100) / activeRun.total : 0}%`,
              }}
            />
          </div>

          {/* Action counters */}
          <div className="grid grid-cols-2 md:grid-cols-4 gap-3">
            {Object.entries(activeRun.counts).map(([action, count]) => (
              <div
                key={action}
                className={clsx(
                  'rounded-xl border p-3 flex flex-col gap-1',
                  ACTION_COLORS[action] ?? 'text-slate-400 bg-slate-900/30 border-slate-800/40',
                )}
              >
                <span className="text-[10px] uppercase font-mono tracking-wider opacity-80">
                  {ACTION_LABELS[action] ?? action}
                </span>
                <span className="text-2xl font-extrabold font-mono">{count}</span>
              </div>
            ))}
          </div>
        </div>
      )}

      {/* Recent runs */}
      <div className="space-y-3">
        <h2 className="text-sm font-bold uppercase tracking-wider text-slate-300">
          Recent verify runs
        </h2>
        {runs.length === 0 ? (
          <div className="glass-card p-8 rounded-2xl text-center text-slate-500 text-sm italic">
            No verify runs yet. Start one above.
          </div>
        ) : (
          <div className="divide-y divide-slate-800/40 glass-card rounded-2xl overflow-hidden">
            {runs.slice().reverse().map((r) => (
              <button
                key={r.run_id}
                onClick={() => setActiveRunId(r.run_id)}
                className={clsx(
                  'w-full text-left p-4 hover:bg-slate-900/30 transition-all duration-200 flex items-center gap-4',
                  activeRunId === r.run_id ? 'bg-purple-950/20' : '',
                )}
              >
                <div className="flex-1 min-w-0">
                  <p className="font-mono text-xs text-slate-300 truncate">{r.run_id}</p>
                  <p className="text-[10px] text-slate-500 mt-1 flex items-center gap-2">
                    <Clock className="w-3 h-3" />
                    {r.started_at} · {r.pipeline_version} · {r.mode}
                  </p>
                </div>
                <div className="flex items-center gap-2 shrink-0">
                  {r.status === 'completed' ? (
                    <span className="text-xs font-mono px-2 py-1 rounded bg-emerald-950/30 border border-emerald-900/40 text-emerald-300">
                      done
                    </span>
                  ) : (
                    <span className="text-xs font-mono px-2 py-1 rounded bg-amber-950/30 border border-amber-900/40 text-amber-300 flex items-center gap-1">
                      <Loader2 className="w-3 h-3 animate-spin" />
                      running
                    </span>
                  )}
                  <span className="text-xs font-mono text-slate-400">
                    {r.completed}/{r.total}
                  </span>
                </div>
              </button>
            ))}
          </div>
        )}
      </div>
    </div>
  )
}

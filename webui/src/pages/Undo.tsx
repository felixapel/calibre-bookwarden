import { useEffect, useState } from 'react'
import { History, RotateCcw, Loader2, CheckCircle2 } from 'lucide-react'
import { fetchRuns, revertRun } from '../api/client'
import { useToast } from '../context/ToastContext'
import clsx from 'clsx'

export default function Undo() {
  const [runs, setRuns] = useState<any[]>([])
  const [loading, setLoading] = useState(true)

  const [revertingId, setRevertingId] = useState<string | null>(null)
  
  const { showToast } = useToast()

  useEffect(() => {
    load()
  }, [])

  const load = async () => {
    setLoading(true)
    try {
      const data = await fetchRuns()
      if (Array.isArray(data)) {
        setRuns(data)
      } else {
        console.error('Invalid runs data format:', data)
        setRuns([])
      }
    } catch (e) {
      showToast('Failed to load runs', 'error')
      setRuns([])
    } finally {
      setLoading(false)
    }
  }

  const handleRevert = async (runId: string) => {
    if (!window.confirm(`Are you sure you want to revert all changes for run ${runId}?`)) {
      return
    }
    setRevertingId(runId)
    try {
      await revertRun(runId, true)
      showToast(`Successfully reverted changes for run ${runId}`, 'success')
      load()
    } catch (e: any) {
      showToast(e.message || 'Failed to revert run changes', 'error')
    } finally {
      setRevertingId(null)
    }
  }

  const formatDate = (dateStr: any) => {
    if (!dateStr) return 'N/A'
    try {
      const d = new Date(dateStr)
      return isNaN(d.getTime()) ? 'N/A' : d.toLocaleString()
    } catch (e) {
      return 'N/A'
    }
  }

  return (
    <div className="p-8 max-w-6xl mx-auto space-y-8 page-transition">
      <header>
        <h1 className="text-3xl font-extrabold tracking-tight bg-clip-text text-transparent bg-gradient-to-r from-slate-100 to-slate-400 flex items-center gap-3">
          <History className="w-8 h-8 text-purple-500 drop-shadow-[0_0_8px_rgba(139,92,246,0.3)]" />
          Changes & Undo Logs
        </h1>
        <p className="mt-1 text-slate-400 text-sm">
          Review applied audit operations and revert metadata changes to original states.
        </p>
      </header>

      {loading ? (
        <div className="flex flex-col items-center justify-center p-24 text-slate-500">
          <Loader2 className="animate-spin text-purple-500 w-8 h-8 mb-4" />
          <p className="text-sm font-medium">Retrieving audit history...</p>
        </div>
      ) : (
        <div className="glass-card rounded-2xl overflow-hidden border border-slate-800/40 relative">
          <div className="overflow-x-auto">
            <table className="w-full text-left border-collapse">
              <thead>
                <tr className="bg-slate-950/60 border-b border-slate-800/40">
                  <th className="px-6 py-4 text-[10px] font-bold uppercase tracking-wider text-slate-400 font-mono">Run ID / Reference</th>
                  <th className="px-6 py-4 text-[10px] font-bold uppercase tracking-wider text-slate-400 font-mono">Execution Date</th>
                  <th className="px-6 py-4 text-[10px] font-bold uppercase tracking-wider text-slate-400 font-mono">Run Status</th>
                  <th className="px-6 py-4 text-[10px] font-bold uppercase tracking-wider text-slate-400 font-mono text-right">Actions</th>
                </tr>
              </thead>
              <tbody className="divide-y divide-slate-800/30">
                {Array.isArray(runs) && runs.map((run) => {
                  const runId = run?.run_id || 'unknown'
                  return (
                    <tr key={runId} className="hover:bg-slate-900/10 transition-colors">
                      <td className="px-6 py-4 font-mono text-xs text-purple-400 font-semibold">{runId}</td>
                      <td className="px-6 py-4 text-xs text-slate-300">
                        {formatDate(run?.created_at)}
                      </td>
                      <td className="px-6 py-4">
                        <span className={clsx(
                          "inline-flex items-center gap-1.5 px-3 py-1 rounded-full text-[10px] font-bold uppercase tracking-wider",
                          run?.status === 'completed' 
                            ? "bg-emerald-500/10 text-emerald-400 border border-emerald-500/20" 
                            : "bg-purple-500/10 text-purple-400 border border-purple-500/20"
                        )}>
                          {run?.status === 'completed' ? (
                            <CheckCircle2 className="w-3.5 h-3.5 text-emerald-400" />
                          ) : (
                            <Loader2 className="w-3.5 h-3.5 animate-spin text-purple-400" />
                          )}
                          {run?.status || 'started'}
                        </span>
                      </td>
                      <td className="px-6 py-4 text-right">
                        <button 
                          onClick={() => handleRevert(runId)}
                          disabled={revertingId !== null || !run?.run_id}
                          className="text-purple-400 hover:text-purple-300 font-bold text-xs inline-flex items-center gap-1.5 px-3 py-1.5 rounded-lg bg-purple-500/5 hover:bg-purple-500/10 border border-purple-500/10 hover:border-purple-500/20 transition-all duration-200 cursor-pointer disabled:opacity-50 disabled:cursor-not-allowed"
                        >
                          {revertingId === runId ? (
                            <Loader2 className="w-3.5 h-3.5 animate-spin" />
                          ) : (
                            <RotateCcw className="w-3.5 h-3.5" />
                          )}
                          Revert Run Changes
                        </button>
                      </td>
                    </tr>
                  )
                })}
                {runs.length === 0 && (
                  <tr>
                    <td colSpan={4} className="px-6 py-16 text-center text-slate-500 text-sm italic">
                      No matching audit runs found in database. Start a scan to create history log.
                    </td>
                  </tr>
                )}
              </tbody>
            </table>
          </div>
        </div>
      )}
    </div>
  )
}

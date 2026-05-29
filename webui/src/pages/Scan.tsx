import { useState, useEffect, useRef } from 'react'
import { Search, Loader2, Play, BookOpen, CheckCircle } from 'lucide-react'
import { scanLibrary, fetchJobStatus, startAudit } from '../api/client'
import { useToast } from '../context/ToastContext'
import clsx from 'clsx'

export default function Scan() {
  const [limit, setLimit] = useState(10)
  const [status, setStatus] = useState<any>(null)
  const [loading, setLoading] = useState(false)
  const [audited, setAudited] = useState(false)

  const { showToast } = useToast()
  const intervalRef = useRef<any>(null)

  useEffect(() => {
    return () => {
      if (intervalRef.current) {
        clearInterval(intervalRef.current)
      }
    }
  }, [])

  const handleScan = async () => {
    setLoading(true)
    setStatus(null)
    setAudited(false)
    try {
      const data = await scanLibrary({ limit })
      if (data && data.job_id) {
        pollStatus(data.job_id)
      } else {
        showToast('Failed to start library scan: Invalid response format', 'error')
      }
    } catch (e) {
      showToast('Failed to start library scan', 'error')
    } finally {
      setLoading(false)
    }
  }

  const pollStatus = async (id: string) => {
    if (intervalRef.current) {
      clearInterval(intervalRef.current)
    }
    intervalRef.current = setInterval(async () => {
      try {
        const data = await fetchJobStatus(id)
        if (data) {
          setStatus(data)
          if (data.status === 'completed' || data.status === 'failed') {
            if (intervalRef.current) {
              clearInterval(intervalRef.current)
              intervalRef.current = null
            }
          }
        }
      } catch (e) {
        if (intervalRef.current) {
          clearInterval(intervalRef.current)
          intervalRef.current = null
        }
      }
    }, 2000)
  }

  const handleAudit = async (runId: string) => {
    try {
      await startAudit(runId)
      setAudited(true)
      showToast('Metadata auditing process started', 'success')
    } catch (e) {
      showToast('Failed to start metadata auditing process.', 'error')
    }
  }

  return (
    <div className="p-8 max-w-4xl mx-auto space-y-8 page-transition">
      <header>
        <h1 className="text-3xl font-extrabold tracking-tight bg-clip-text text-transparent bg-gradient-to-r from-slate-100 to-slate-400">
          Scan Library
        </h1>
        <p className="mt-1 text-slate-400 text-sm">
          Scan the Calibre library database to register books and queue them for AI auditing.
        </p>
      </header>

      <div className="glass-card rounded-2xl p-6 space-y-6 relative overflow-hidden">
        <div className="absolute top-0 right-0 w-48 h-48 bg-purple-500/5 rounded-full blur-2xl pointer-events-none" />
        
        <div className="flex flex-col md:flex-row md:items-end gap-6">
          <div className="flex-1 space-y-2">
            <label className="text-xs font-semibold uppercase tracking-wider text-slate-400 font-mono">
              Limit Scan (Number of books)
            </label>
            <input
              type="number"
              value={limit}
              onChange={(e) => setLimit(Math.max(1, parseInt(e.target.value) || 1))}
              className="w-full px-4 py-3 bg-slate-950/40 border border-slate-800/80 focus:border-purple-500 rounded-xl text-slate-200 outline-none transition-colors font-mono"
            />
          </div>
          <button
            onClick={handleScan}
            disabled={loading || (status && status.status === 'running')}
            className="flex items-center justify-center gap-2 px-6 py-3 bg-gradient-to-r from-purple-600 to-indigo-600 hover:from-purple-500 hover:to-indigo-500 text-white font-semibold rounded-xl transition-all duration-300 disabled:opacity-50 h-[48px] shadow-[0_0_15px_rgba(139,92,246,0.2)] cursor-pointer"
          >
            {loading || (status && status.status === 'running') ? (
              <Loader2 className="w-5 h-5 animate-spin" />
            ) : (
              <Search className="w-5 h-5" />
            )}
            Start Scan Process
          </button>
        </div>

        {status && (
          <div className="border-t border-slate-800/50 pt-6 space-y-6">
            <h3 className="font-bold text-slate-200 text-sm uppercase tracking-wider font-mono">Job Execution Details</h3>
            
            <div className="grid grid-cols-1 md:grid-cols-2 gap-4">
              <div className="p-4 rounded-xl bg-slate-950/40 border border-slate-900/80 flex items-center justify-between">
                <div>
                  <p className="text-[10px] text-slate-500 font-mono uppercase font-semibold">Current State</p>
                  <p className="text-sm font-bold capitalize text-slate-200 mt-1">{status.status}</p>
                </div>
                <span className={clsx(
                  "w-3 h-3 rounded-full shadow-[0_0_10px]",
                  status.status === 'completed' ? "bg-emerald-500 shadow-emerald-500/20" : status.status === 'failed' ? "bg-rose-500 shadow-rose-500/20" : "bg-purple-500 animate-pulse shadow-purple-500/20"
                )} />
              </div>

              <div className="p-4 rounded-xl bg-slate-950/40 border border-slate-900/80">
                <p className="text-[10px] text-slate-500 font-mono uppercase font-semibold">Reference Key</p>
                <p className="text-sm font-mono text-slate-300 mt-1 truncate">{status.job_id}</p>
              </div>
            </div>

            {status.status === 'completed' && status.result && (
              <div className="space-y-6 animate-in fade-in slide-in-from-bottom-2 duration-300">
                <div className="flex items-center gap-4 p-5 rounded-2xl bg-gradient-to-r from-purple-950/20 to-slate-950/50 border border-purple-900/20">
                  <div className="p-3 bg-purple-500/10 border border-purple-500/20 rounded-xl text-purple-400">
                    <BookOpen className="w-6 h-6" />
                  </div>
                  <div>
                    <h4 className="text-2xl font-black text-purple-300">
                      {typeof status.result === 'object' ? status.result.books_found : '—'}
                    </h4>
                    <p className="text-xs text-slate-400 uppercase tracking-widest font-mono mt-0.5">Ebooks Registered</p>
                    <p className="text-[10px] text-slate-500 font-mono mt-1">
                      Run: {typeof status.result === 'object' ? status.result.run_id : status.result}
                    </p>
                  </div>
                </div>

                <div className="flex justify-end pt-2">
                  {audited ? (
                    <div className="flex items-center gap-2 text-emerald-400 font-semibold px-4 py-3 bg-emerald-500/10 border border-emerald-500/20 rounded-xl">
                      <CheckCircle className="w-5 h-5" />
                      Audit Task Triggered Successfully
                    </div>
                  ) : (
                    <button 
                      onClick={() => handleAudit(typeof status.result === 'object' ? status.result.run_id : status.result)}
                      className="flex items-center gap-2 px-6 py-3 bg-gradient-to-r from-emerald-600 to-teal-600 hover:from-emerald-500 hover:to-teal-500 text-white font-semibold rounded-xl transition-all duration-300 shadow-[0_0_15px_rgba(16,185,129,0.2)] cursor-pointer"
                    >
                      <Play className="w-5 h-5" />
                      Audit Registered Run
                    </button>
                  )}
                </div>
              </div>
            )}
          </div>
        )}
      </div>
    </div>
  )
}

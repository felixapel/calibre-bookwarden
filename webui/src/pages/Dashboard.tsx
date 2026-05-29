import { useQuery } from '@tanstack/react-query'
import { fetchConfig, fetchDoctor, fetchHealth, fetchBooks, fetchDuplicates, fetchRuns } from '../api/client'
import { Link } from 'react-router-dom'
import { 
  Activity, 
  Database, 
  CheckCircle2, 
  XCircle, 
  Terminal, 
  Link as LinkIcon, 
  ShieldAlert, 
  Copy, 
  History, 
  ArrowRight,
  TrendingUp
} from 'lucide-react'
import clsx from 'clsx'

export default function Dashboard() {
  const { data: health } = useQuery({ queryKey: ['health'], queryFn: fetchHealth, refetchInterval: 10000 })
  const { data: config } = useQuery({ queryKey: ['config'], queryFn: fetchConfig })
  const { data: doctor } = useQuery({ queryKey: ['doctor'], queryFn: fetchDoctor, refetchInterval: 15000 })
  const { data: booksData } = useQuery({ queryKey: ['books'], queryFn: fetchBooks })
  const { data: duplicatesData } = useQuery({ queryKey: ['duplicates'], queryFn: fetchDuplicates })
  const { data: runsData } = useQuery({ queryKey: ['runs'], queryFn: fetchRuns })

  const isOk = health?.status === 'ok'

  // Extract counts for stats cards
  const booksList = Array.isArray(booksData?.data) ? booksData.data : []
  const reviewQueueCount = booksList.filter((b: any) => b.status === 'needs_review').length
  const auditedCount = booksList.filter((b: any) => b.status === 'audited').length
  const appliedCount = booksList.filter((b: any) => b.status === 'applied').length
  
  const duplicateCount = Array.isArray(duplicatesData?.data) ? duplicatesData.data.length : 0
  const runsCount = Array.isArray(runsData) ? runsData.length : 0

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
      {/* Header section with profile name */}
      <header className="flex flex-col md:flex-row justify-between items-start md:items-center gap-4">
        <div>
          <h1 className="text-3xl font-extrabold tracking-tight bg-clip-text text-transparent bg-gradient-to-r from-slate-100 to-slate-400">
            Dashboard
          </h1>
          <p className="text-slate-400 text-sm mt-1">
            Real-time status overview of Calibre AI auditor sidecars and library connections.
          </p>
        </div>
        <div className="flex items-center gap-2 px-4 py-2 rounded-xl bg-slate-900/40 border border-slate-800/40 backdrop-blur-md">
          <span className="text-xs text-slate-500 font-mono">PROFILE:</span>
          <span className="text-xs font-semibold text-purple-400 font-mono">{config?.profile || 'DEFAULT'}</span>
        </div>
      </header>

      {/* Metrics Row */}
      <div className="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-4 gap-6">
        {/* Review Queue Card */}
        <Link to="/review" className="glass-card p-6 rounded-2xl flex flex-col justify-between min-h-[140px] hover:scale-[1.02] hover:border-purple-500/30 transition-all duration-300 group">
          <div className="flex justify-between items-start">
            <div className="space-y-1">
              <span className="text-xs text-slate-500 font-mono tracking-wider uppercase">Review Queue</span>
              <h3 className="text-3xl font-extrabold text-slate-100 tracking-tight group-hover:text-purple-400 transition-colors">
                {reviewQueueCount}
              </h3>
            </div>
            <div className="p-2.5 rounded-xl border bg-purple-500/10 border-purple-500/20 text-purple-400 group-hover:bg-purple-500/20 group-hover:text-purple-300 transition-all">
              <ShieldAlert className="w-5 h-5" />
            </div>
          </div>
          <div className="mt-4 flex items-center justify-between text-xs text-slate-400">
            <span>Books needing review</span>
            <ArrowRight className="w-4 h-4 opacity-0 group-hover:opacity-100 group-hover:translate-x-1 transition-all" />
          </div>
        </Link>

        {/* Duplicates Card */}
        <Link to="/duplicates" className="glass-card p-6 rounded-2xl flex flex-col justify-between min-h-[140px] hover:scale-[1.02] hover:border-cyan-500/30 transition-all duration-300 group">
          <div className="flex justify-between items-start">
            <div className="space-y-1">
              <span className="text-xs text-slate-500 font-mono tracking-wider uppercase">Duplicates</span>
              <h3 className="text-3xl font-extrabold text-slate-100 tracking-tight group-hover:text-cyan-400 transition-colors">
                {duplicateCount}
              </h3>
            </div>
            <div className="p-2.5 rounded-xl border bg-cyan-500/10 border-cyan-500/20 text-cyan-400 group-hover:bg-cyan-500/20 group-hover:text-cyan-300 transition-all">
              <Copy className="w-5 h-5" />
            </div>
          </div>
          <div className="mt-4 flex items-center justify-between text-xs text-slate-400">
            <span>Potential duplicate pairs</span>
            <ArrowRight className="w-4 h-4 opacity-0 group-hover:opacity-100 group-hover:translate-x-1 transition-all" />
          </div>
        </Link>

        {/* Changes Card */}
        <Link to="/undo" className="glass-card p-6 rounded-2xl flex flex-col justify-between min-h-[140px] hover:scale-[1.02] hover:border-amber-500/30 transition-all duration-300 group">
          <div className="flex justify-between items-start">
            <div className="space-y-1">
              <span className="text-xs text-slate-500 font-mono tracking-wider uppercase">Audit Runs</span>
              <h3 className="text-3xl font-extrabold text-slate-100 tracking-tight group-hover:text-amber-400 transition-colors">
                {runsCount}
              </h3>
            </div>
            <div className="p-2.5 rounded-xl border bg-amber-500/10 border-amber-500/20 text-amber-400 group-hover:bg-amber-500/20 group-hover:text-amber-300 transition-all">
              <History className="w-5 h-5" />
            </div>
          </div>
          <div className="mt-4 flex items-center justify-between text-xs text-slate-400">
            <span>Total execution history</span>
            <ArrowRight className="w-4 h-4 opacity-0 group-hover:opacity-100 group-hover:translate-x-1 transition-all" />
          </div>
        </Link>

        {/* Applied Card */}
        <div className="glass-card p-6 rounded-2xl flex flex-col justify-between min-h-[140px] relative overflow-hidden group">
          <div className="flex justify-between items-start">
            <div className="space-y-1">
              <span className="text-xs text-slate-500 font-mono tracking-wider uppercase">Applied Fixes</span>
              <h3 className="text-3xl font-bold tracking-tight text-emerald-400">
                {appliedCount}
              </h3>
            </div>
            <div className="p-2.5 rounded-xl border bg-emerald-500/10 border-emerald-500/20 text-emerald-400">
              <CheckCircle2 className="w-5 h-5" />
            </div>
          </div>
          <div className="mt-4 flex items-center gap-1.5 text-xs text-slate-400">
            <TrendingUp className="w-3.5 h-3.5 text-emerald-400" />
            <span>{auditedCount} approved & queued</span>
          </div>
        </div>
      </div>

      {/* Grid Cards Row 2 */}
      <div className="grid grid-cols-1 md:grid-cols-3 gap-6">
        {/* System Health Card */}
        <div className="glass-card p-6 rounded-2xl flex flex-col justify-between min-h-[160px] relative overflow-hidden">
          <div className="absolute top-0 right-0 w-24 h-24 bg-purple-500/5 rounded-full blur-xl pointer-events-none" />
          <div className="flex justify-between items-start">
            <div className="space-y-1">
              <span className="text-xs text-slate-500 font-mono tracking-wider uppercase">System Health</span>
              <h3 className="text-2xl font-bold mt-1 tracking-tight">
                {isOk ? 'Operational' : 'Warning'}
              </h3>
            </div>
            <div className={clsx(
              "p-2.5 rounded-xl border",
              isOk ? "bg-emerald-500/10 border-emerald-500/20 text-emerald-400" : "bg-rose-500/10 border-rose-500/20 text-rose-400"
            )}>
              <Activity className="w-5 h-5" />
            </div>
          </div>
          <div className="mt-4 flex items-center gap-2 text-xs">
            <span className={clsx("w-2 h-2 rounded-full", isOk ? "bg-emerald-500 animate-pulse" : "bg-rose-500")} />
            <span className="text-slate-400 font-medium">
              {isOk ? 'API responds correctly' : 'Database or service issues'}
            </span>
          </div>
        </div>

        {/* Configuration Card */}
        <div className="glass-card p-6 rounded-2xl md:col-span-2 space-y-4 relative overflow-hidden">
          <div className="absolute top-0 right-0 w-32 h-32 bg-cyan-500/5 rounded-full blur-2xl pointer-events-none" />
          <div className="flex justify-between items-start">
            <div className="space-y-1">
              <span className="text-xs text-slate-500 font-mono tracking-wider uppercase">Database & Storage</span>
              <h3 className="text-lg font-bold text-slate-200 mt-1">Calibre Library Configuration</h3>
            </div>
            <div className="p-2 bg-slate-900/80 border border-slate-800/80 text-cyan-400 rounded-xl">
              <Database className="w-5 h-5" />
            </div>
          </div>

          <div className="grid grid-cols-1 sm:grid-cols-2 gap-4 text-xs font-mono text-slate-400 pt-2">
            <div className="p-3 bg-slate-950/40 rounded-xl border border-slate-900/60 space-y-1">
              <span className="text-[10px] text-slate-500 uppercase font-semibold block">Library Directory</span>
              <span className="text-slate-300 truncate block">{config?.library?.path || 'Not mounted'}</span>
            </div>
            <div className="p-3 bg-slate-950/40 rounded-xl border border-slate-900/60 space-y-1">
              <span className="text-[10px] text-slate-500 uppercase font-semibold block">Database Engine</span>
              <span className="text-slate-300 truncate block">{config?.database?.backend?.toUpperCase() || 'SQLite'}</span>
            </div>
          </div>

          <div className="flex items-center justify-between pt-2 border-t border-slate-800/40 text-xs">
            <span className="text-slate-500">Protection Mode:</span>
            <span className={clsx(
              "inline-flex items-center gap-1.5 px-3 py-1 rounded-full text-[10px] font-bold uppercase tracking-wider",
              config?.library?.read_only 
                ? "bg-amber-500/10 text-amber-400 border border-amber-500/20" 
                : "bg-purple-500/10 text-purple-400 border border-purple-500/20"
            )}>
              {config?.library?.read_only ? 'READ ONLY' : 'WRITE ENABLED'}
            </span>
          </div>
        </div>
      </div>

      {/* Sidecar Connectivity & Doctor Panel Row 3 */}
      <div className="grid grid-cols-1 md:grid-cols-3 gap-6">
        {/* Sidecar Services Status */}
        <div className="glass-card p-6 rounded-2xl md:col-span-1 space-y-4">
          <div className="flex items-center gap-2 text-slate-400 border-b border-slate-800/50 pb-3">
            <LinkIcon className="w-4 h-4 text-purple-400" />
            <h3 className="font-semibold text-sm uppercase tracking-wider text-slate-200">Sidecar Connectivity</h3>
          </div>
          
          <div className="space-y-3.5">
            {doctor?.connectivity ? (
              Object.entries(doctor.connectivity).flatMap(([svc, info]: [string, any]) => {
                // LLMs is a nested object: { ollama: { ok, name } }
                if (svc === 'llms' && typeof info === 'object') {
                  return Object.entries(info).map(([llmName, llmInfo]: [string, any]) => (
                    <div key={`llm-${llmName}`} className="flex items-center justify-between p-3 rounded-xl bg-slate-950/30 border border-slate-900">
                      <div className="flex items-center gap-3">
                        <span className={clsx(
                          "w-2.5 h-2.5 rounded-full shadow-[0_0_8px]",
                          llmInfo.ok ? "bg-emerald-500 shadow-emerald-500/30" : "bg-rose-500 shadow-rose-500/30"
                        )} />
                        <div className="min-w-0">
                          <p className="text-sm font-semibold capitalize text-slate-200">{llmName}</p>
                          <p className="text-[10px] text-slate-500 truncate max-w-[140px] font-mono mt-0.5">LLM Provider</p>
                        </div>
                      </div>
                      <span className={clsx(
                        "text-[9px] px-2 py-0.5 rounded-md font-bold uppercase",
                        llmInfo.ok ? "bg-emerald-500/10 text-emerald-400" : "bg-rose-500/10 text-rose-400"
                      )}>
                        {llmInfo.ok ? 'Online' : 'Offline'}
                      </span>
                    </div>
                  ))
                }
                return (
                  <div key={svc} className="flex items-center justify-between p-3 rounded-xl bg-slate-950/30 border border-slate-900">
                    <div className="flex items-center gap-3">
                      <span className={clsx(
                        "w-2.5 h-2.5 rounded-full shadow-[0_0_8px]",
                        !info.enabled ? "bg-slate-600 shadow-slate-600/30" : info.ok ? "bg-emerald-500 shadow-emerald-500/30" : "bg-rose-500 shadow-rose-500/30"
                      )} />
                      <div className="min-w-0">
                        <p className="text-sm font-semibold capitalize text-slate-200">{svc}</p>
                        <p className="text-[10px] text-slate-500 truncate max-w-[140px] font-mono mt-0.5">{info.url || 'Internal / Direct'}</p>
                      </div>
                    </div>
                    <span className={clsx(
                      "text-[9px] px-2 py-0.5 rounded-md font-bold uppercase",
                      !info.enabled ? "bg-slate-900 text-slate-500" : info.ok ? "bg-emerald-500/10 text-emerald-400" : "bg-rose-500/10 text-rose-400"
                    )}>
                      {!info.enabled ? 'Disabled' : info.ok ? 'Online' : 'Offline'}
                    </span>
                  </div>
                )
              })
            ) : (
              <div className="py-8 text-center text-xs text-slate-500 animate-pulse">Checking connections...</div>
            )}
          </div>
        </div>

        {/* Recent Audit Runs */}
        <div className="glass-card p-6 rounded-2xl md:col-span-1 space-y-4">
          <div className="flex items-center justify-between border-b border-slate-800/50 pb-3">
            <div className="flex items-center gap-2 text-slate-400">
              <History className="w-4 h-4 text-amber-400" />
              <h3 className="font-semibold text-sm uppercase tracking-wider text-slate-200">Recent Runs</h3>
            </div>
            <Link to="/undo" className="text-purple-400 hover:text-purple-300 text-xs font-semibold hover:underline">
              View all
            </Link>
          </div>

          <div className="space-y-3.5">
            {runsData && Array.isArray(runsData) ? (
              runsData.slice(0, 3).map((run: any) => (
                <div key={run.run_id} className="flex items-center justify-between p-3 rounded-xl bg-slate-950/30 border border-slate-900">
                  <div className="min-w-0">
                    <p className="text-xs font-semibold font-mono text-purple-400 truncate">{run.run_id}</p>
                    <p className="text-[10px] text-slate-500 mt-0.5">{formatDate(run.created_at)}</p>
                  </div>
                  <span className={clsx(
                    "text-[9px] px-2 py-0.5 rounded font-bold uppercase",
                    run.status === 'completed' ? "bg-emerald-500/10 text-emerald-400" : "bg-purple-500/10 text-purple-400"
                  )}>
                    {run.status}
                  </span>
                </div>
              ))
            ) : (
              <div className="py-8 text-center text-xs text-slate-500 animate-pulse">Loading runs...</div>
            )}
            {runsData && runsData.length === 0 && (
              <div className="py-8 text-center text-xs text-slate-500 italic">No runs recorded yet</div>
            )}
          </div>
        </div>

        {/* Doctor Dependencies Panel */}
        <div className="glass-card p-6 rounded-2xl md:col-span-1 space-y-4">
          <div className="flex items-center justify-between border-b border-slate-800/50 pb-3">
            <div className="flex items-center gap-2 text-slate-400">
              <Terminal className="w-4 h-4 text-cyan-400" />
              <h3 className="font-semibold text-sm uppercase tracking-wider text-slate-200">System Binaries</h3>
            </div>
            <span className="text-[10px] text-slate-500 font-mono">DOCTOR</span>
          </div>

          <div className="overflow-hidden">
            {doctor?.dependencies ? (
              <div className="divide-y divide-slate-800/20 max-h-[220px] overflow-y-auto pr-1">
                {Object.entries(doctor.dependencies).map(([tool, info]: [string, any]) => (
                  <div key={tool} className="py-2.5 flex items-center justify-between text-xs hover:bg-slate-900/10 rounded-lg transition-colors">
                    <div className="flex items-center gap-2.5">
                      {info.found ? (
                        <CheckCircle2 className="w-4 h-4 text-emerald-400 shrink-0" />
                      ) : (
                        <XCircle className="w-4 h-4 text-rose-400 shrink-0" />
                      )}
                      <span className="font-bold text-slate-200 font-mono truncate max-w-[90px]">{tool}</span>
                    </div>
                    <span className={clsx(
                      "px-1.5 py-0.5 rounded text-[8px] font-bold uppercase shrink-0",
                      info.found ? "bg-emerald-500/10 text-emerald-400" : "bg-rose-500/10 text-rose-400"
                    )}>
                      {info.found ? 'FOUND' : 'MISSING'}
                    </span>
                  </div>
                ))}
              </div>
            ) : (
              <div className="py-12 text-center text-xs text-slate-500 animate-pulse">Running diagnostics...</div>
            )}
          </div>
        </div>
      </div>
    </div>
  )
}

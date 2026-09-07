import type { ReactNode } from 'react'
import { ShieldCheck, Database } from 'lucide-react'

interface HeaderProps {
  title: string
  subtitle?: string
  badge?: string
  actions?: ReactNode
}

export function Header({ title, subtitle, badge, actions }: HeaderProps) {
  return (
    <header className="sticky top-0 z-30 flex flex-wrap items-center justify-between gap-4 border-b border-slate-800/80 bg-[#070b13]/80 px-6 py-4 backdrop-blur-xl md:px-10">
      <div className="space-y-1">
        <div className="flex items-center gap-2.5">
          <h1 className="text-xl font-black tracking-tight text-slate-100 md:text-2xl">{title}</h1>
          {badge && (
            <span className="rounded-full border border-cyan-500/30 bg-cyan-500/10 px-2.5 py-0.5 text-xs font-bold text-cyan-300 font-mono">
              {badge}
            </span>
          )}
        </div>
        {subtitle && <p className="text-xs text-slate-400 max-w-2xl">{subtitle}</p>}
      </div>

      <div className="flex items-center gap-3">
        <div className="hidden sm:flex items-center gap-2 rounded-xl border border-slate-800 bg-slate-900/60 px-3 py-1.5 text-xs text-slate-300">
          <ShieldCheck className="h-4 w-4 text-emerald-400" />
          <span className="font-semibold">Immutable Vault</span>
          <span className="text-slate-600">|</span>
          <Database className="h-3.5 w-3.5 text-cyan-400" />
          <span className="text-slate-400">Atomic Snapshot</span>
        </div>

        {actions}
      </div>
    </header>
  )
}

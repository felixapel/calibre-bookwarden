import { useLocation, Link } from 'react-router-dom'
import { useQuery } from '@tanstack/react-query'
import {
  Shield,
  LayoutDashboard,
  ScanSearch,
  Sparkles,
  Library,
  FileCheck2,
  Lock,
  ExternalLink,
} from 'lucide-react'
import clsx from 'clsx'

import { fetchHealth } from '../api/client'

const navItems = [
  { to: '/', icon: LayoutDashboard, label: 'Command Center', badge: 'Live' },
  { to: '/audit-360', icon: ScanSearch, label: '360° Forensic Audit' },
  { to: '/covers', icon: Sparkles, label: 'Cover Studio & Deck', badge: 'New' },
  { to: '/curation', icon: Library, label: 'Curation Guardian' },
  { to: '/verify', icon: FileCheck2, label: 'Sealed Evidence' },
]

export function Sidebar() {
  const location = useLocation()
  const health = useQuery({
    queryKey: ['health'],
    queryFn: fetchHealth,
    refetchInterval: 15_000,
    retry: false,
  })

  const isReady = health.data?.status === 'ready'

  return (
    <aside className="relative hidden h-full w-72 shrink-0 flex-col border-r border-slate-800/80 bg-[#070b13] p-5 md:flex select-none">
      {/* Brand Header */}
      <div className="mb-8 px-3 pt-2">
        <Link to="/" className="group flex items-center gap-3">
          <div className="flex h-11 w-11 items-center justify-center rounded-2xl bg-gradient-to-tr from-cyan-600 via-cyan-500 to-indigo-500 text-slate-950 shadow-lg shadow-cyan-500/20 group-hover:scale-105 group-hover:shadow-cyan-500/40 transition-all duration-300">
            <Shield className="h-6 w-6 stroke-[2.5]" />
          </div>
          <div>
            <div className="flex items-center gap-1.5">
              <span className="text-base font-black tracking-tight text-slate-50 group-hover:text-cyan-300 transition-colors">
                Bookwarden
              </span>
              <span className="rounded bg-cyan-500/20 px-1.5 py-0.5 text-[9px] font-extrabold uppercase tracking-wider text-cyan-300">
                v1.3
              </span>
            </div>
            <p className="text-[11px] font-semibold text-slate-400">Calibre Forensic Guardian</p>
          </div>
        </Link>
      </div>

      {/* Navigation Links */}
      <nav className="flex-1 space-y-1.5" aria-label="Main Navigation">
        <p className="px-3 mb-2 text-[10px] font-bold uppercase tracking-wider text-slate-400">Modules</p>
        {navItems.map((item) => {
          const isActive = item.to === '/' ? location.pathname === '/' : location.pathname.startsWith(item.to)
          const Icon = item.icon

          return (
            <Link
              key={item.to}
              to={item.to}
              className={clsx(
                'group flex items-center justify-between rounded-xl px-3.5 py-3 text-sm font-semibold transition-all duration-200',
                isActive
                  ? 'bg-gradient-to-r from-cyan-500/15 to-indigo-500/10 text-cyan-200 border border-cyan-500/30 shadow-sm shadow-cyan-950/40'
                  : 'text-slate-400 hover:bg-slate-900/80 hover:text-slate-100 hover:translate-x-1',
              )}
            >
              <div className="flex items-center gap-3">
                <Icon
                  className={clsx(
                    'h-4 w-4 transition-colors',
                    isActive ? 'text-cyan-400' : 'text-slate-400 group-hover:text-slate-200',
                  )}
                />
                <span>{item.label}</span>
              </div>
              {item.badge && (
                <span
                  className={clsx(
                    'rounded-full px-2 py-0.5 text-[10px] font-bold font-mono tracking-wider uppercase',
                    isActive
                      ? 'bg-cyan-400 text-slate-950'
                      : 'bg-slate-800 text-slate-400 group-hover:bg-slate-700 group-hover:text-slate-300',
                  )}
                >
                  {item.badge}
                </span>
              )}
            </Link>
          )
        })}
      </nav>

      {/* Safety & System Status Box */}
      <div className="mt-auto space-y-3 pt-4 border-t border-slate-800/80">
        <div className="rounded-xl border border-slate-800/80 bg-slate-950/60 p-3.5 space-y-2.5">
          <div className="flex items-center justify-between">
            <div className="flex items-center gap-2">
              <span
                className={clsx(
                  'h-2.5 w-2.5 rounded-full ring-4 transition-all',
                  isReady
                    ? 'bg-emerald-400 ring-emerald-500/20 animate-pulse'
                    : 'bg-rose-400 ring-rose-500/20',
                )}
              />
              <span className="text-xs font-bold text-slate-200">
                {isReady ? 'Engine Bound & Ready' : 'Connecting to Engine'}
              </span>
            </div>
            <span className="text-[10px] font-mono text-slate-400">POSTGRES/SQLITE</span>
          </div>

          <div className="flex items-center justify-between text-[11px] text-slate-400 pt-1 border-t border-slate-800/60">
            <div className="flex items-center gap-1.5">
              <Lock className="h-3 w-3 text-emerald-400" />
              <span>Safety Invariant</span>
            </div>
            <span className="font-semibold text-emerald-400">Read-Only Vault</span>
          </div>
        </div>

        {/* External Links */}
        <div className="flex items-center justify-between px-2 text-xs text-slate-400">
          <a
            href="/docs"
            target="_blank"
            rel="noreferrer"
            className="flex items-center gap-1 hover:text-cyan-300 transition-colors"
          >
            <span>API Docs</span>
            <ExternalLink className="h-3 w-3" />
          </a>
          <a
            href="https://github.com/felixapel/calibre-bookwarden"
            target="_blank"
            rel="noreferrer"
            className="flex items-center gap-1 hover:text-cyan-300 transition-colors"
          >
            <span>GitHub</span>
            <ExternalLink className="h-3 w-3" />
          </a>
        </div>
      </div>
    </aside>
  )
}

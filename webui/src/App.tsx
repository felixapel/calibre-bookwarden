import { useEffect, useRef, useState, useSyncExternalStore, type ComponentType, type SVGProps } from 'react'
import { BrowserRouter, Link, Route, Routes, useLocation } from 'react-router-dom'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { BookOpen, FileCheck2, KeyRound, Radar, ShieldCheck } from 'lucide-react'
import clsx from 'clsx'

import { getAuthRevision, getUnauthorized, setApiKey, subscribeAuth } from './api/auth'
import { fetchHealth } from './api/client'
import Dashboard from './pages/Dashboard'
import Review from './pages/Review'
import Verify from './pages/Verify'

type Icon = ComponentType<SVGProps<SVGSVGElement>>

const navigation: ReadonlyArray<[string, Icon, string]> = [
  ['/', Radar, 'Overview'],
  ['/verify', ShieldCheck, 'Verify'],
  ['/review', FileCheck2, 'Evidence'],
]

function NavItem({ to, icon: IconComponent, label }: { to: string; icon: Icon; label: string }) {
  const location = useLocation()
  const active = to === '/' ? location.pathname === '/' : location.pathname.startsWith(to)
  return (
    <Link
      to={to}
      className={clsx(
        'flex items-center gap-3 rounded-xl px-4 py-3 text-sm font-medium transition-colors',
        active
          ? 'border-l-2 border-cyan-400 bg-cyan-500/10 text-cyan-200'
          : 'text-slate-400 hover:bg-slate-900/70 hover:text-slate-100',
      )}
    >
      <IconComponent aria-hidden="true" className="h-5 w-5" />
      <span>{label}</span>
    </Link>
  )
}

function Sidebar() {
  const health = useQuery({ queryKey: ['health'], queryFn: fetchHealth, refetchInterval: 15_000, retry: false })
  const ready = health.data?.status === 'ready'
  return (
    <aside className="relative hidden h-full w-64 shrink-0 flex-col border-r border-slate-800/60 bg-[#070b13]/95 md:flex">
      <div className="p-6">
        <div className="flex items-center gap-3 text-lg font-extrabold tracking-wide text-slate-100">
          <BookOpen aria-hidden="true" className="h-6 w-6 text-cyan-400" />
          Calibre Auditor
        </div>
        <p className="mt-2 text-[10px] uppercase tracking-[0.2em] text-slate-400">Certificate A · shadow only</p>
      </div>
      <nav aria-label="Primary navigation" className="flex-1 space-y-2 px-4">
        {navigation.map(([to, icon, label]) => <NavItem key={to} to={to} icon={icon} label={label} />)}
      </nav>
      <div className="border-t border-slate-800/60 p-4">
        <div className="flex items-center gap-3 rounded-xl bg-slate-950/60 px-3 py-3">
          <span className={clsx('h-3 w-3 rounded-full', ready ? 'bg-emerald-400' : 'bg-rose-400')} />
          <div>
            <p className="text-xs font-semibold text-slate-300">Verifier</p>
            <p className="text-[10px] text-slate-400">{ready ? 'Ready and exactly bound' : 'Not ready'}</p>
          </div>
        </div>
      </div>
    </aside>
  )
}

function MobileNavigation() {
  const location = useLocation()
  return (
    <nav aria-label="Primary navigation" className="fixed inset-x-0 bottom-0 z-40 flex border-t border-slate-800 bg-[#070b13]/95 p-2 md:hidden">
      {navigation.map(([to, IconComponent, label]) => {
        const active = to === '/' ? location.pathname === '/' : location.pathname.startsWith(to)
        return (
          <Link
            key={to}
            to={to}
            aria-label={label}
            className={clsx('flex flex-1 justify-center rounded-lg p-3', active ? 'bg-cyan-500/15 text-cyan-300' : 'text-slate-400')}
          >
            <IconComponent aria-hidden="true" className="h-5 w-5" />
          </Link>
        )
      })}
    </nav>
  )
}

export default function App() {
  const [apiKeyInput, setApiKeyInput] = useState('')
  const unauthorized = useSyncExternalStore(subscribeAuth, getUnauthorized, getUnauthorized)
  const authRevision = useSyncExternalStore(subscribeAuth, getAuthRevision, getAuthRevision)
  const apiKeyInputRef = useRef<HTMLInputElement>(null)
  const queryClient = useQueryClient()

  useEffect(() => {
    const authenticated = () => void queryClient.resetQueries()
    window.addEventListener('bookaudit-authenticated', authenticated)
    return () => window.removeEventListener('bookaudit-authenticated', authenticated)
  }, [queryClient])

  useEffect(() => {
    if (unauthorized) apiKeyInputRef.current?.focus()
  }, [unauthorized])

  const saveApiKey = () => {
    setApiKey(apiKeyInput)
    setApiKeyInput('')
  }

  return (
    <BrowserRouter>
      <div className="flex h-screen overflow-hidden bg-[#090d16] text-slate-100">
        <Sidebar />
        <main className="min-w-0 flex-1 overflow-y-auto pb-20 md:pb-0">
          <Routes key={authRevision}>
            <Route path="/" element={<Dashboard />} />
            <Route path="/verify" element={<Verify />} />
            <Route path="/verify/:runId" element={<Verify />} />
            <Route path="/review" element={<Review />} />
            <Route path="/review/:evidenceId" element={<Review />} />
            <Route path="*" element={<div className="p-10 text-center text-slate-400">Page not found</div>} />
          </Routes>
        </main>
        <MobileNavigation />
      </div>

      {unauthorized && (
        <div className="fixed inset-0 z-50 flex items-center justify-center bg-slate-950/85 p-4 backdrop-blur-sm">
          <div role="dialog" aria-modal="true" aria-labelledby="api-key-title" className="w-full max-w-md space-y-4 rounded-2xl border border-slate-700 bg-[#0b0f19] p-6 shadow-2xl">
            <h2 id="api-key-title" className="flex items-center gap-2 text-lg font-bold text-slate-100">
              <KeyRound aria-hidden="true" className="h-5 w-5 text-cyan-400" />
              API key required
            </h2>
            <p className="text-sm text-slate-400">Enter the local Certificate A API key. It stays only in page memory and is cleared on reload.</p>
            <input
              ref={apiKeyInputRef}
              aria-label="API key"
              type="password"
              autoComplete="off"
              value={apiKeyInput}
              onChange={(event) => setApiKeyInput(event.target.value)}
              className="w-full rounded-xl border border-slate-700 bg-slate-950 px-4 py-3 font-mono text-sm outline-none focus:border-cyan-500"
            />
            <button
              type="button"
              onClick={saveApiKey}
              disabled={!apiKeyInput.trim()}
              className="w-full rounded-xl bg-cyan-600 px-4 py-3 text-sm font-bold text-white disabled:opacity-40"
            >
              Save and retry
            </button>
          </div>
        </div>
      )}
    </BrowserRouter>
  )
}

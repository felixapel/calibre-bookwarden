import { useEffect, useRef, useState, useSyncExternalStore } from 'react'
import { BrowserRouter, Link, Route, Routes, useLocation } from 'react-router-dom'
import { useQueryClient } from '@tanstack/react-query'
import {
  KeyRound,
  LayoutDashboard,
  ScanSearch,
  Sparkles,
  Library,
  FileCheck2,
} from 'lucide-react'
import clsx from 'clsx'

import { getAuthRevision, getUnauthorized, setApiKey, subscribeAuth } from './api/auth'
import { Sidebar } from './components/Sidebar'
import Dashboard from './pages/Dashboard'
import Verify from './pages/Verify'
import Review from './pages/Review'
import CommandCenter from './pages/CommandCenter'
import Audit360 from './pages/Audit360'
import CoverStudio from './pages/CoverStudio'
import Curation from './pages/Curation'

const mobileNavItems = [
  { to: '/', icon: LayoutDashboard, label: 'Overview' },
  { to: '/audit-360', icon: ScanSearch, label: '360° Audit' },
  { to: '/covers', icon: Sparkles, label: 'Covers' },
  { to: '/curation', icon: Library, label: 'Curation' },
  { to: '/verify', icon: FileCheck2, label: 'Verify' },
]

function MobileNavigation() {
  const location = useLocation()
  return (
    <nav
      aria-label="Mobile navigation"
      className="fixed inset-x-0 bottom-0 z-40 flex border-t border-slate-800 bg-[#070b13]/95 backdrop-blur-lg p-2 md:hidden"
    >
      {mobileNavItems.map((item) => {
        const isActive = item.to === '/' ? location.pathname === '/' : location.pathname.startsWith(item.to)
        const Icon = item.icon
        return (
          <Link
            key={item.to}
            to={item.to}
            aria-label={item.label}
            className={clsx(
              'flex flex-1 flex-col items-center justify-center rounded-xl py-2 px-1 transition-all',
              isActive
                ? 'bg-cyan-500/15 text-cyan-300 font-bold'
                : 'text-slate-400 hover:text-slate-200',
            )}
          >
            <Icon className="h-5 w-5" />
            <span className="text-[10px] mt-1">{item.label}</span>
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
      <div className="flex h-screen overflow-hidden bg-[#070b13] text-slate-100 font-sans selection:bg-cyan-500/30 selection:text-cyan-200">
        <Sidebar />
        <main className="min-w-0 flex-1 overflow-y-auto pb-20 md:pb-0">
          <Routes key={authRevision}>
            <Route path="/" element={<Dashboard />} />
            <Route path="/command-center" element={<CommandCenter />} />
            <Route path="/audit-360" element={<Audit360 />} />
            <Route path="/covers" element={<CoverStudio />} />
            <Route path="/curation" element={<Curation />} />
            <Route path="/verify" element={<Verify />} />
            <Route path="/verify/:runId" element={<Verify />} />
            <Route path="/review" element={<Review />} />
            <Route path="/review/:evidenceId" element={<Review />} />
            <Route path="*" element={<Dashboard />} />
          </Routes>
        </main>
        <MobileNavigation />
      </div>

      {unauthorized && (
        <div className="fixed inset-0 z-50 flex items-center justify-center bg-slate-950/85 p-4 backdrop-blur-sm">
          <div
            role="dialog"
            aria-modal="true"
            aria-labelledby="api-key-title"
            className="w-full max-w-md space-y-4 rounded-2xl border border-slate-700 bg-[#0b0f19] p-6 shadow-2xl"
          >
            <h2 id="api-key-title" className="flex items-center gap-2 text-lg font-bold text-slate-100">
              <KeyRound aria-hidden="true" className="h-5 w-5 text-cyan-400" />
              API key required
            </h2>
            <p className="text-sm text-slate-400">
              Enter the local Certificate A API key. It stays only in page memory and is cleared on reload.
            </p>
            <input
              ref={apiKeyInputRef}
              aria-label="API key"
              type="password"
              autoComplete="off"
              value={apiKeyInput}
              onChange={(event) => setApiKeyInput(event.target.value)}
              className="w-full rounded-xl border border-slate-700 bg-slate-950 px-4 py-3 font-mono text-sm outline-none focus:border-cyan-500 text-slate-100"
            />
            <button
              type="button"
              onClick={saveApiKey}
              disabled={!apiKeyInput.trim()}
              className="w-full rounded-xl bg-cyan-600 px-4 py-3 text-sm font-bold text-white hover:bg-cyan-500 disabled:opacity-40 transition-colors"
            >
              Save and retry
            </button>
          </div>
        </div>
      )}
    </BrowserRouter>
  )
}

import { useEffect, useRef, useState, useSyncExternalStore } from 'react'
import { getAuthRevision, getUnauthorized, setApiKey, subscribeAuth } from './api/auth'
import { BrowserRouter as Router, Routes, Route, Link, useLocation } from 'react-router-dom'
import { LayoutDashboard, FileSearch, Search, Settings, ShieldAlert, History, BookOpen, KeyRound, ShieldCheck, Copy, Sparkles } from 'lucide-react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { fetchHealth } from './api/client'
import clsx from 'clsx'

import Dashboard from './pages/Dashboard'
import Inspect from './pages/Inspect'
import Scan from './pages/Scan'
import Review from './pages/Review'
import Undo from './pages/Undo'
import SettingsPage from './pages/Settings'
import Duplicates from './pages/Duplicates'
import Verify from './pages/Verify'


function NavItem({ to, icon: Icon, children }: { to: string, icon: any, children: React.ReactNode }) {
  const location = useLocation()
  const isActive = location.pathname === to
  return (
    <Link
      to={to}
      className={clsx(
        "flex items-center gap-3 px-4 py-3 rounded-xl text-sm font-medium transition-all duration-300 relative group",
        isActive 
          ? "bg-gradient-to-r from-purple-600/20 to-cyan-600/10 text-purple-300 border-l-2 border-purple-500 shadow-[inset_0_0_12px_rgba(139,92,246,0.15)]" 
          : "text-slate-400 hover:bg-slate-900/50 hover:text-slate-100 hover:translate-x-1"
      )}
    >
      <Icon className={clsx("w-5 h-5 transition-transform duration-300 group-hover:scale-110", isActive ? "text-purple-400" : "text-slate-500")} />
      <span>{children}</span>
      {isActive && (
        <span className="absolute right-4 w-1.5 h-1.5 rounded-full bg-purple-400 shadow-[0_0_8px_#8b5cf6]" />
      )}
    </Link>
  )
}

function Sidebar() {
  const { data: health } = useQuery({ queryKey: ['health'], queryFn: fetchHealth, refetchInterval: 10000 })
  const isOk = health?.status === 'ready'

  return (
    <div className="hidden md:flex w-64 shrink-0 bg-[#070b13]/90 border-r border-slate-800/40 flex-col h-full backdrop-blur-xl relative z-10">
      {/* Glow Effects */}
      <div className="absolute top-0 left-0 w-full h-32 bg-gradient-to-b from-purple-500/5 to-transparent pointer-events-none" />
      
      <div className="p-6">
        <div className="flex items-center gap-3 font-bold text-lg tracking-wider text-transparent bg-clip-text bg-gradient-to-r from-purple-400 to-cyan-400">
          <BookOpen className="w-6 h-6 text-purple-500 drop-shadow-[0_0_8px_rgba(139,92,246,0.5)]" />
          <span className="font-extrabold uppercase">Calibre AI</span>
        </div>
        <p className="text-[10px] text-slate-500 font-mono tracking-widest mt-1 uppercase">Metadata Auditor</p>
      </div>

      <nav className="flex-1 px-4 py-2 space-y-1.5 overflow-y-auto">
        <NavItem to="/" icon={LayoutDashboard}>Dashboard</NavItem>
        <NavItem to="/verify" icon={Sparkles}>Verify (V2)</NavItem>
        <NavItem to="/scan" icon={Search}>Scan Library</NavItem>
        <NavItem to="/inspect" icon={FileSearch}>Inspect File</NavItem>
        <NavItem to="/duplicates" icon={Copy}>Duplicates</NavItem>
        <NavItem to="/review" icon={ShieldAlert}>Review Queue</NavItem>
        <NavItem to="/undo" icon={History}>Changes & Undo</NavItem>
        <div className="pt-4 mt-4 border-t border-slate-800/50">
          <NavItem to="/settings" icon={Settings}>Settings</NavItem>
        </div>
      </nav>

      {/* Sidebar Footer Status */}
      <div className="p-4 border-t border-slate-800/50 bg-[#060a11]/60">
        <div className="flex items-center gap-3 px-3 py-2.5 rounded-xl bg-slate-950/50 border border-slate-800/30">
          <div className="relative flex">
            <span className={clsx(
              "absolute inline-flex h-3.5 w-3.5 rounded-full opacity-75 animate-ping",
              isOk ? "bg-emerald-400" : "bg-rose-400"
            )} />
            <span className={clsx(
              "relative inline-flex rounded-full h-3.5 w-3.5",
              isOk ? "bg-emerald-500" : "bg-rose-500"
            )} />
          </div>
          <div className="flex-1 min-w-0">
            <p className="text-xs font-semibold text-slate-300">System Status</p>
            <p className="text-[10px] text-slate-400 font-mono mt-0.5 truncate">
              {isOk ? 'Auditor Online' : 'Connecting...'}
            </p>
          </div>
        </div>
      </div>
    </div>
  )
}

const mobileNavigation = [
  ['/', LayoutDashboard, 'Dashboard'],
  ['/verify', Sparkles, 'Verify'],
  ['/scan', Search, 'Scan'],
  ['/inspect', FileSearch, 'Inspect'],
  ['/duplicates', Copy, 'Duplicates'],
  ['/review', ShieldAlert, 'Review'],
  ['/undo', History, 'Undo'],
  ['/settings', Settings, 'Settings'],
] as const

function MobileNavigation() {
  const location = useLocation()
  return (
    <nav aria-label="Primary navigation" className="md:hidden fixed inset-x-0 bottom-0 z-40 flex overflow-x-auto border-t border-slate-800 bg-[#070b13]/95 px-2 py-2 backdrop-blur-xl">
      {mobileNavigation.map(([to, Icon, label]) => (
        <Link
          key={to}
          to={to}
          aria-label={label}
          title={label}
          className={clsx(
            'flex min-w-12 flex-1 items-center justify-center rounded-lg p-3',
            location.pathname === to ? 'bg-purple-500/20 text-purple-300' : 'text-slate-400',
          )}
        >
          <Icon aria-hidden="true" className="h-5 w-5" />
        </Link>
      ))}
    </nav>
  )
}

export default function App() {
  const [apiKeyInput, setApiKeyInput] = useState('')
  const showAuthModal = useSyncExternalStore(subscribeAuth, getUnauthorized, getUnauthorized)
  const authRevision = useSyncExternalStore(subscribeAuth, getAuthRevision, getAuthRevision)
  const apiKeyInputRef = useRef<HTMLInputElement>(null)
  const queryClient = useQueryClient()

  useEffect(() => {
    const handleAuthenticated = () => {
      void queryClient.resetQueries()
    }
    window.addEventListener('bookaudit-authenticated', handleAuthenticated)
    return () => {
      window.removeEventListener('bookaudit-authenticated', handleAuthenticated)
    }
  }, [queryClient])

  useEffect(() => {
    if (showAuthModal) apiKeyInputRef.current?.focus()
  }, [showAuthModal])

  const handleSaveApiKey = () => {
    setApiKey(apiKeyInput)
    setApiKeyInput('')
  }

  return (
    <Router>
      <div className="flex h-screen bg-[#090d16] overflow-hidden text-slate-100">
        {/* Background Ambient Glows */}
        <div className="absolute top-[-10%] left-[-10%] w-[50%] h-[50%] rounded-full bg-purple-900/10 blur-[120px] pointer-events-none" />
        <div className="absolute bottom-0 right-0 w-[40%] h-[40%] rounded-full bg-cyan-900/10 blur-[120px] pointer-events-none" />
        
        <Sidebar />
        
        <main className="min-w-0 flex-1 overflow-y-auto relative z-0 flex flex-col pb-16 md:pb-0">
          <div className="flex-1">
            <Routes key={authRevision}>
              <Route path="/" element={<Dashboard />} />
              <Route path="/verify" element={<Verify />} />
              <Route path="/scan" element={<Scan />} />
              <Route path="/inspect" element={<Inspect />} />
              <Route path="/duplicates" element={<Duplicates />} />
              <Route path="/review" element={<Review />} />
              <Route path="/undo" element={<Undo />} />
              <Route path="/settings" element={<SettingsPage />} />
              <Route path="*" element={<div className="p-8 text-center text-slate-400 font-medium">Page not found</div>} />
            </Routes>
          </div>
        </main>
        <MobileNavigation />
      </div>

      {showAuthModal && (
        <div className="fixed inset-0 z-50 flex items-center justify-center p-4 bg-slate-950/80 backdrop-blur-sm animate-in fade-in duration-200">
          <div
            role="dialog"
            aria-modal="true"
            aria-labelledby="api-key-dialog-title"
            className="bg-[#0b0f19] border border-slate-800 rounded-2xl w-full max-w-md p-6 shadow-2xl space-y-4"
          >
            <div className="flex items-center justify-between">
              <h3 id="api-key-dialog-title" className="text-lg font-bold text-slate-200 flex items-center gap-2">
                <KeyRound className="w-5 h-5 text-purple-400" />
                API Key Authentication
              </h3>
            </div>
            <p className="text-xs text-slate-400">
              The server returned an authentication failure (401 Unauthorized). Please provide the valid `BOOKAUDIT_API_KEY` below to resume requests.
            </p>
            <div className="space-y-2">
              <input
                ref={apiKeyInputRef}
                aria-label="API key"
                type="password"
                value={apiKeyInput}
                onChange={(e) => setApiKeyInput(e.target.value)}
                placeholder="Enter API Key..."
                className="w-full px-4 py-2.5 bg-slate-950 border border-slate-800 focus:border-purple-500 rounded-xl text-slate-200 outline-none transition-colors font-mono text-sm"
              />
            </div>
            <div className="flex justify-end gap-3 pt-2">
              <button
                onClick={handleSaveApiKey}
                disabled={!apiKeyInput.trim()}
                className="flex items-center gap-1.5 px-4 py-2 bg-gradient-to-r from-purple-600 to-indigo-600 hover:from-purple-500 hover:to-indigo-500 text-white text-xs font-semibold rounded-xl transition-all duration-300 shadow-[0_0_10px_rgba(139,92,246,0.2)] cursor-pointer"
              >
                <ShieldCheck className="w-4 h-4" />
                Save and retry
              </button>
            </div>
          </div>
        </div>
      )}
    </Router>
  )
}

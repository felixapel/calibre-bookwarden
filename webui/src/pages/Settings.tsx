import { useCallback, useEffect, useState } from 'react'
import { Settings as SettingsIcon, Save, Loader2, Database, Shield, Zap, Globe, Check, Key } from 'lucide-react'
import { fetchConfig, updateConfig } from '../api/client'
import { setApiKey } from '../api/auth'
import { useToast } from '../context/ToastContext'

export default function Settings() {
  const [config, setConfig] = useState<any>(null)
  const [loading, setLoading] = useState(true)
  const [saved, setSaved] = useState(false)
  const [mutable, setMutable] = useState(false)
  const [localApiKey, setLocalApiKey] = useState('')

  const { showToast } = useToast()

  const handleSaveLocalKey = () => {
    setApiKey(localApiKey)
    showToast('API key loaded into memory for this page session', 'success')
  }

  const load = useCallback(async () => {
    setLoading(true)
    try {
      const data = await fetchConfig()
      setConfig(data.data.config)
      setMutable(data.data.mutable)
    } catch (e) {
      showToast('Failed to load settings', 'error')
    } finally {
      setLoading(false)
    }
  }, [showToast])

  useEffect(() => {
    load()
  }, [load])

  const handleSave = async () => {
    try {
      await updateConfig(config)
      setSaved(true)
      showToast('Settings saved successfully', 'success')
      setTimeout(() => setSaved(false), 3000)
    } catch (e) {
      showToast('Failed to save settings', 'error')
    }
  }

  if (loading) {
    return (
      <div className="p-24 flex flex-col items-center justify-center text-slate-500">
        <Loader2 className="animate-spin text-purple-500 w-8 h-8 mb-4" />
        <p className="text-sm font-medium">Loading configuration settings...</p>
      </div>
    )
  }

  return (
    <div className="p-8 max-w-5xl mx-auto space-y-8 page-transition">
      <header className="flex flex-col sm:flex-row sm:items-center justify-between gap-4 border-b border-slate-800/40 pb-6">
        <div>
          <h1 className="text-3xl font-extrabold tracking-tight bg-clip-text text-transparent bg-gradient-to-r from-slate-100 to-slate-400 flex items-center gap-3">
            <SettingsIcon className="w-8 h-8 text-purple-500 drop-shadow-[0_0_8px_rgba(139,92,246,0.3)]" />
            Settings
          </h1>
          <p className="mt-1 text-slate-400 text-sm">
            Manage your local metadata agent parameters, provider priority, and homelab connections.
          </p>
        </div>
        
        <div className="flex items-center gap-3">
          {saved && (
            <span className="text-xs text-emerald-400 font-semibold bg-emerald-500/10 border border-emerald-500/20 px-3.5 py-2 rounded-xl flex items-center gap-1.5 animate-in fade-in duration-300">
              <Check className="w-3.5 h-3.5" /> Changes saved
            </span>
          )}
          <button 
            onClick={handleSave}
            disabled={!mutable}
            title={mutable ? 'Save configuration' : 'Production configuration is managed by deployment inputs'}
            className="flex items-center gap-2 px-5 py-2.5 bg-gradient-to-r from-purple-600 to-indigo-600 hover:from-purple-500 hover:to-indigo-500 text-white font-semibold rounded-xl transition-all duration-300 shadow-[0_0_12px_rgba(139,92,246,0.2)] text-sm cursor-pointer"
          >
            <Save className="w-4 h-4" />
            {mutable ? 'Save Changes' : 'Managed by deployment'}
          </button>
        </div>
      </header>

      <div className="space-y-8">
        {/* General & AI */}
        <section className="glass-card p-6 rounded-2xl space-y-6">
          <div className="flex items-center gap-2 text-md font-bold text-slate-200 border-b border-slate-800/40 pb-3">
            <Zap className="w-5 h-5 text-amber-500" />
            General & AI Judge Settings
          </div>
          
          <div className="grid grid-cols-1 md:grid-cols-2 gap-6">
            <div className="space-y-2">
              <label className="text-xs font-semibold uppercase tracking-wider text-slate-400 font-mono">Judge LLM Model</label>
              <input 
                type="text" 
                value={config?.judge_model || 'Unknown'} 
                className="w-full px-4 py-3 bg-slate-950/40 border border-slate-800/80 rounded-xl text-slate-300 font-mono text-sm outline-none cursor-not-allowed" 
                readOnly 
              />
            </div>
            
            <div className="space-y-2">
              <label className="text-xs font-semibold uppercase tracking-wider text-slate-400 font-mono">Log Reporting Level</label>
              <select 
                value={config?.log_level || 'INFO'}
                onChange={(e) => setConfig({ ...config, log_level: e.target.value })}
                className="w-full px-4 py-3 bg-slate-950 border border-slate-800/80 rounded-xl text-slate-300 font-mono text-sm outline-none cursor-pointer focus:border-purple-500"
              >
                <option value="DEBUG">DEBUG</option>
                <option value="INFO">INFO</option>
                <option value="WARNING">WARNING</option>
                <option value="ERROR">ERROR</option>
              </select>
            </div>
          </div>
        </section>

        {/* Database & Infrastructure */}
        <section className="glass-card p-6 rounded-2xl space-y-6">
          <div className="flex items-center gap-2 text-md font-bold text-slate-200 border-b border-slate-800/40 pb-3">
            <Database className="w-5 h-5 text-cyan-500" />
            Infrastructure Backends
          </div>
          
          <div className="grid grid-cols-1 md:grid-cols-2 gap-6">
            <div className="space-y-2">
              <label className="text-xs font-semibold uppercase tracking-wider text-slate-400 font-mono">Auditor Database</label>
              <input 
                type="text" 
                value={config?.database?.backend || 'SQLite'} 
                className="w-full px-4 py-3 bg-slate-950/40 border border-slate-800/80 rounded-xl text-slate-300 font-mono text-sm outline-none cursor-not-allowed" 
                readOnly 
              />
            </div>
            
            <div className="space-y-2">
              <label className="text-xs font-semibold uppercase tracking-wider text-slate-400 font-mono">Process Queue Engine</label>
              <input 
                type="text" 
                value={config?.queue?.backend || 'InMemory'} 
                className="w-full px-4 py-3 bg-slate-950/40 border border-slate-800/80 rounded-xl text-slate-300 font-mono text-sm outline-none cursor-not-allowed" 
                readOnly 
              />
            </div>
          </div>
        </section>

        {/* Privacy Toggles */}
        <section className="glass-card p-6 rounded-2xl space-y-6">
          <div className="flex items-center gap-2 text-md font-bold text-slate-200 border-b border-slate-800/40 pb-3">
            <Shield className="w-5 h-5 text-emerald-500" />
            Privacy Controls
          </div>
          
          <div className="space-y-5">
            <div className="flex items-center justify-between p-4 bg-slate-950/30 border border-slate-900 rounded-xl">
               <div>
                 <p className="font-semibold text-slate-200 text-sm">Allow Remote Text API Calls</p>
                 <p className="text-xs text-slate-500 mt-0.5">Allows sending book metadata snippets to external cloud LLM providers.</p>
               </div>
               <input 
                 type="checkbox" 
                 checked={config?.privacy?.allow_remote_text || false} 
                 onChange={(e) => setConfig({
                   ...config,
                   privacy: { ...config.privacy, allow_remote_text: e.target.checked }
                 })}
                 className="w-5 h-5 rounded-md border-slate-700 bg-slate-900/50 text-purple-600 focus:ring-purple-500 cursor-pointer" 
               />
            </div>
            
            <div className="flex items-center justify-between p-4 bg-slate-950/30 border border-slate-900 rounded-xl">
               <div>
                 <p className="font-semibold text-slate-200 text-sm">Allow Remote Cover Image OCR</p>
                 <p className="text-xs text-slate-500 mt-0.5">Allows passing cover image thumbnails to remote vision endpoint providers.</p>
               </div>
               <input 
                 type="checkbox" 
                 checked={config?.privacy?.allow_remote_images || false} 
                 onChange={(e) => setConfig({
                   ...config,
                   privacy: { ...config.privacy, allow_remote_images: e.target.checked }
                 })}
                 className="w-5 h-5 rounded-md border-slate-700 bg-slate-900/50 text-purple-600 focus:ring-purple-500 cursor-pointer" 
               />
            </div>
          </div>
        </section>

        {/* Security & Authentication */}
        <section className="glass-card p-6 rounded-2xl space-y-6">
          <div className="flex items-center gap-2 text-md font-bold text-slate-200 border-b border-slate-800/40 pb-3">
            <Key className="w-5 h-5 text-purple-500" />
            Security & Authentication
          </div>
          <div className="space-y-4">
            <div className="space-y-2">
              <label className="text-xs font-semibold uppercase tracking-wider text-slate-400 font-mono">
                Browser API Key (X-API-Key)
              </label>
              <div className="flex gap-4">
                <input
                  type="password"
                  value={localApiKey}
                  onChange={(e) => setLocalApiKey(e.target.value)}
                  placeholder="Enter API Key to authenticate browser requests..."
                  className="flex-1 px-4 py-3 bg-slate-950 border border-slate-800/80 rounded-xl text-slate-300 font-mono text-sm outline-none focus:border-purple-500 transition-colors"
                />
                <button
                  onClick={handleSaveLocalKey}
                  className="px-5 py-3 bg-purple-600/10 hover:bg-purple-600/20 text-purple-400 border border-purple-500/20 hover:border-purple-500/30 font-semibold rounded-xl text-sm transition-all duration-300 cursor-pointer"
                >
                  Apply Key
                </button>
              </div>
              <p className="text-[10px] text-slate-500 mt-1">
                If the backend has `BOOKAUDIT_API_KEY` enabled, enter it here. The key remains only in memory and is cleared when the page reloads.
              </p>
            </div>
          </div>
        </section>

        {/* Providers */}
        <section className="glass-card p-6 rounded-2xl space-y-6">
          <div className="flex items-center gap-2 text-md font-bold text-slate-200 border-b border-slate-800/40 pb-3">
            <Globe className="w-5 h-5 text-purple-500" />
            Metadata Discovery Providers
          </div>
          
          <div className="grid grid-cols-1 sm:grid-cols-3 gap-4">
             {Object.keys(config?.providers || {}).map(p => (
               <div key={p} className="p-4 bg-slate-950/30 border border-slate-900 rounded-xl flex items-center justify-between">
                 <span className="capitalize font-semibold text-sm text-slate-300">{p.replace('_', ' ')}</span>
                 <input 
                   type="checkbox" 
                   checked={config?.providers[p] || false} 
                   onChange={(e) => setConfig({
                     ...config,
                     providers: { ...config.providers, [p]: e.target.checked }
                   })}
                   className="w-5 h-5 rounded-md border-slate-700 bg-slate-900/50 text-purple-600 focus:ring-purple-500 cursor-pointer"
                 />
               </div>
             ))}
          </div>
        </section>
      </div>
    </div>
  )
}

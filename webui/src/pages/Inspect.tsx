import { useState, useEffect } from 'react'
import { useMutation, useQuery } from '@tanstack/react-query'
import { useSearchParams } from 'react-router-dom'
import { inspectPath, fetchFs, fetchConfig } from '../api/client'
import { Search, AlertCircle, FileText, ImageIcon, BookOpen, Layers, Folder, File, FolderUp, FolderSearch, X, ChevronDown, ChevronUp } from 'lucide-react'
import clsx from 'clsx'
import CoverImage from '../components/CoverImage'

function FileBrowserModal({ isOpen, onClose, onSelect }: { isOpen: boolean, onClose: () => void, onSelect: (path: string) => void }) {
  const { data: configData } = useQuery({
    queryKey: ['config'],
    queryFn: fetchConfig,
    enabled: isOpen,
  })

  const [currentDir, setCurrentDir] = useState('/library')
  const [searchQuery, setSearchQuery] = useState('')

  useEffect(() => {
    if (isOpen && configData?.library?.path) {
      setCurrentDir(configData.library.path)
    }
  }, [isOpen, configData])

  const { data, isLoading, isError, error } = useQuery({
    queryKey: ['fs', currentDir],
    queryFn: () => fetchFs(currentDir),
    enabled: isOpen && !!currentDir,
  })

  if (!isOpen) return null;

  const parts = currentDir.split('/').filter(Boolean)

  const filteredDirs = data?.directories?.filter((d: any) =>
    d.name.toLowerCase().includes(searchQuery.toLowerCase())
  ) || []

  const filteredFiles = data?.files?.filter((f: any) =>
    f.name.toLowerCase().includes(searchQuery.toLowerCase())
  ) || []

  function formatBytes(bytes: number) {
    if (!bytes) return '0 B'
    const k = 1024
    const sizes = ['B', 'KB', 'MB', 'GB']
    const i = Math.floor(Math.log(bytes) / Math.log(k))
    return parseFloat((bytes / Math.pow(k, i)).toFixed(1)) + ' ' + sizes[i]
  }

  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center p-4 bg-slate-950/80 backdrop-blur-sm animate-in fade-in duration-200">
      <div className="bg-[#0b0f19] border border-slate-800 rounded-2xl w-full max-w-2xl max-h-[85vh] flex flex-col shadow-2xl overflow-hidden">
        
        {/* Header */}
        <div className="px-6 py-4 border-b border-slate-800/60 flex items-center justify-between bg-slate-900/40">
          <h3 className="text-lg font-bold text-slate-200 flex items-center gap-2">
            <FolderSearch className="w-5 h-5 text-purple-400" />
            Browse Server Files
          </h3>
          <button onClick={onClose} className="p-1 text-slate-400 hover:text-slate-200 hover:bg-slate-800 rounded-md transition-colors cursor-pointer">
            <X className="w-5 h-5" />
          </button>
        </div>

        {/* Path Input & Breadcrumbs */}
        <div className="px-6 py-3 bg-slate-950/50 border-b border-slate-800/40 space-y-2">
          <div className="flex gap-2">
            <input 
              type="text" 
              value={currentDir}
              onChange={(e) => setCurrentDir(e.target.value)}
              className="flex-1 bg-slate-900 border border-slate-800 text-purple-300 text-xs font-mono px-3 py-1.5 rounded-lg outline-none focus:border-purple-500 transition-colors"
              placeholder="Enter directory path..."
            />
            <input
              type="text"
              value={searchQuery}
              onChange={(e) => setSearchQuery(e.target.value)}
              className="w-1/3 bg-slate-900 border border-slate-800 text-slate-300 text-xs px-3 py-1.5 rounded-lg outline-none focus:border-purple-500 transition-colors"
              placeholder="Filter current folder..."
            />
          </div>
          {/* Breadcrumbs */}
          <div className="flex flex-wrap items-center gap-1 text-xs font-mono text-slate-400 bg-slate-950/20 p-2 rounded-lg border border-slate-900/50">
            <button 
              onClick={() => setCurrentDir('/')} 
              className="hover:text-purple-400 transition-colors font-semibold cursor-pointer"
            >
              root
            </button>
            {parts.map((part, index) => {
              const pathUpTo = '/' + parts.slice(0, index + 1).join('/')
              return (
                <span key={pathUpTo} className="flex items-center gap-1">
                  <span className="text-slate-600">/</span>
                  <button 
                    onClick={() => setCurrentDir(pathUpTo)} 
                    className="hover:text-purple-400 transition-colors cursor-pointer truncate max-w-[120px]"
                    title={part}
                  >
                    {part}
                  </button>
                </span>
              )
            })}
          </div>
        </div>

        {/* File List */}
        <div className="flex-1 overflow-y-auto p-4 space-y-1">
          {isLoading ? (
            <div className="flex flex-col items-center justify-center py-12 text-slate-500">
              <Loader2 className="w-6 h-6 animate-spin text-purple-500 mb-3" />
              <p className="text-sm">Loading directory contents...</p>
            </div>
          ) : isError ? (
            <div className="p-4 bg-rose-500/10 border border-rose-500/20 text-rose-400 rounded-xl text-sm">
              {(error as Error).message}
            </div>
          ) : data ? (
            <>
              {data.parent_dir && (
                <button 
                  onClick={() => setCurrentDir(data.parent_dir)}
                  className="w-full text-left px-3 py-2 flex items-center gap-3 text-sm text-slate-300 hover:bg-slate-800/40 hover:text-slate-100 rounded-lg transition-colors group cursor-pointer"
                >
                  <FolderUp className="w-4 h-4 text-purple-400/70 group-hover:text-purple-400" />
                  <span className="font-semibold text-xs text-purple-400/90 font-mono">.. (Go Up)</span>
                </button>
              )}
              
              {filteredDirs.map((d: any) => (
                <button 
                  key={d.path}
                  onClick={() => { setCurrentDir(d.path); setSearchQuery(''); }}
                  className="w-full text-left px-3 py-2 flex items-center gap-3 text-sm text-slate-300 hover:bg-slate-800/40 hover:text-slate-100 rounded-lg transition-colors group cursor-pointer"
                >
                  <Folder className="w-4 h-4 text-cyan-400/70 group-hover:text-cyan-400" />
                  <span className="truncate">{d.name}</span>
                </button>
              ))}

              {filteredFiles.map((f: any) => (
                <button 
                  key={f.path}
                  onClick={() => { onSelect(f.path); onClose(); }}
                  className="w-full text-left px-3 py-2 flex items-center justify-between text-sm text-slate-400 hover:bg-purple-500/10 hover:text-purple-300 rounded-lg transition-colors group border border-transparent hover:border-purple-500/20 cursor-pointer"
                >
                  <div className="flex items-center gap-3 min-w-0">
                    <File className="w-4 h-4 text-slate-500 group-hover:text-purple-400 shrink-0" />
                    <span className="truncate">{f.name}</span>
                  </div>
                  {f.size_bytes !== undefined && (
                    <span className="text-[10px] text-slate-500 font-mono group-hover:text-purple-300/80 shrink-0 ml-2">
                      {formatBytes(f.size_bytes)}
                    </span>
                  )}
                </button>
              ))}

              {filteredDirs.length === 0 && filteredFiles.length === 0 && (
                <div className="py-12 text-center text-slate-500 text-sm italic">
                  No folders or supported files matching the filter.
                </div>
              )}
            </>
          ) : null}
        </div>
      </div>
    </div>
  )
}

export default function Inspect() {
  const [searchParams] = useSearchParams()
  const [path, setPath] = useState('')
  const [noProviders, setNoProviders] = useState(false)
  const [showJson, setShowJson] = useState(false)
  const [isBrowserOpen, setIsBrowserOpen] = useState(false)

  const mutation = useMutation({
    mutationFn: () => inspectPath(path, noProviders),
  })

  useEffect(() => {
    const queryPath = searchParams.get('path')
    if (queryPath) {
      setPath(queryPath)
    }
  }, [searchParams])

  return (
    <div className="p-8 max-w-6xl mx-auto space-y-8 page-transition">
      <header>
        <h1 className="text-3xl font-extrabold tracking-tight bg-clip-text text-transparent bg-gradient-to-r from-slate-100 to-slate-400">
          Inspect File
        </h1>
        <p className="mt-1 text-slate-400 text-sm">
          Run metadata extraction, cover matching, and online lookup manually on any local ebook file path.
        </p>
      </header>

      {/* Input controls */}
      <div className="glass-card p-6 rounded-2xl relative overflow-hidden">
        <div className="absolute top-0 right-0 w-32 h-32 bg-purple-500/5 rounded-full blur-2xl pointer-events-none" />
        
        <div className="flex flex-col gap-6 md:flex-row md:items-end">
          <div className="flex-1 space-y-2">
            <label className="text-xs font-semibold uppercase tracking-wider text-slate-400 font-mono flex items-center justify-between">
              <span>Direct File Path</span>
              <button 
                onClick={() => setIsBrowserOpen(true)}
                className="text-purple-400 hover:text-purple-300 font-bold tracking-normal inline-flex items-center gap-1.5 bg-purple-500/10 px-2.5 py-1 rounded-lg transition-all duration-200 hover:bg-purple-500/20 cursor-pointer text-xs"
              >
                <FolderSearch className="w-3.5 h-3.5" />
                Browse Files
              </button>
            </label>
            <input 
              type="text" 
              value={path}
              onChange={(e) => setPath(e.target.value)}
              className="w-full px-4 py-3 bg-slate-950/40 border border-slate-800/80 focus:border-purple-500 rounded-xl text-slate-200 outline-none transition-colors font-mono text-sm"
              placeholder="/library/book.epub"
            />
          </div>
          
          <div className="flex items-center gap-3 h-[48px] px-2">
            <input 
              type="checkbox" 
              id="noProviders"
              checked={noProviders}
              onChange={(e) => setNoProviders(e.target.checked)}
              className="w-5 h-5 rounded-md border-slate-700 bg-slate-900/50 text-purple-600 focus:ring-purple-500 focus:ring-offset-slate-900 cursor-pointer"
            />
            <label htmlFor="noProviders" className="text-sm text-slate-300 font-medium cursor-pointer">Skip Providers</label>
          </div>

          <button
            onClick={() => mutation.mutate()}
            disabled={mutation.isPending || !path}
            className="flex items-center justify-center gap-2 px-6 py-3 bg-gradient-to-r from-purple-600 to-indigo-600 hover:from-purple-500 hover:to-indigo-500 text-white font-semibold rounded-xl transition-all duration-300 disabled:opacity-50 h-[48px] shadow-[0_0_15px_rgba(139,92,246,0.2)] cursor-pointer"
          >
            {mutation.isPending ? (
              <>
                <Loader2 className="w-5 h-5 animate-spin" />
                Inspecting...
              </>
            ) : (
              <>
                <Search className="w-5 h-5" />
                Analyze File
              </>
            )}
          </button>
        </div>
      </div>

      {mutation.isError && (
        <div className="p-4 bg-rose-500/10 border border-rose-500/20 text-rose-300 rounded-xl flex items-center gap-3 animate-in fade-in duration-300">
          <AlertCircle className="w-5 h-5 text-rose-400 shrink-0" />
          <p className="text-sm font-medium">{mutation.error.message}</p>
        </div>
      )}

      {mutation.data && (
        <div className="space-y-8 animate-in fade-in duration-500">
          {/* Extracted Metadata and Cover Evidence */}
          <div className="grid grid-cols-1 md:grid-cols-2 gap-6">
            <div className="glass-card p-6 rounded-2xl space-y-4">
              <h2 className="text-lg font-bold flex items-center gap-2.5 text-slate-200 border-b border-slate-800/40 pb-3">
                <FileText className="w-5 h-5 text-purple-400" /> 
                Extracted Metadata
              </h2>
              <div className="space-y-4 text-sm">
                <div className="grid grid-cols-[100px_1fr] gap-2">
                  <span className="text-slate-400 font-mono text-xs">TITLE:</span>
                  <span className="font-semibold text-slate-100">{mutation.data.extracted?.title || 'Unknown'}</span>
                </div>
                <div className="grid grid-cols-[100px_1fr] gap-2">
                  <span className="text-slate-400 font-mono text-xs">AUTHORS:</span>
                  <span className="font-semibold text-slate-100">{mutation.data.extracted?.authors?.join(', ') || 'Unknown'}</span>
                </div>
                <div className="grid grid-cols-[100px_1fr] gap-2">
                  <span className="text-slate-400 font-mono text-xs">ISBN:</span>
                  <span className="font-mono text-xs bg-slate-950/60 px-2 py-0.5 border border-slate-900 rounded text-cyan-400 w-fit">
                    {mutation.data.extracted?.identifiers?.isbn || 'None'}
                  </span>
                </div>
              </div>
            </div>

            <div className="glass-card p-6 rounded-2xl space-y-4">
              <h2 className="text-lg font-bold flex items-center gap-2.5 text-slate-200 border-b border-slate-800/40 pb-3">
                <ImageIcon className="w-5 h-5 text-purple-400" /> 
                Cover Image Evidence
              </h2>
              {mutation.data.cover ? (
                <div className="flex flex-col sm:flex-row gap-4">
                  <CoverImage bookKey={mutation.data.book_key} className="w-24 h-32 shrink-0 rounded-lg shadow-lg border border-slate-800/50 object-cover bg-slate-950/50" />
                  <div className="space-y-4 text-sm flex-1 min-w-0">
                    <div className="grid grid-cols-[100px_1fr] gap-2">
                      <span className="text-slate-400 font-mono text-xs">PATH:</span>
                      <span className="font-mono text-xs text-slate-300 break-all bg-slate-950/30 p-2 rounded border border-slate-900">{mutation.data.cover.embedded_cover_path}</span>
                    </div>
                    <div className="grid grid-cols-[100px_1fr] gap-2">
                      <span className="text-slate-400 font-mono text-xs">PHASH:</span>
                      <span className="font-mono text-xs bg-slate-950/60 px-2 py-0.5 border border-slate-900 rounded text-amber-400 w-fit">{mutation.data.phash || mutation.data.cover.phash}</span>
                    </div>
                  </div>
                </div>
              ) : (
                <p className="text-sm text-slate-500 italic py-4">No embedded cover image found inside the archive.</p>
              )}
            </div>
          </div>

          {/* Providers Candidates */}
          {!noProviders && (
            <div className="glass-card p-6 rounded-2xl space-y-4">
              <h2 className="text-lg font-bold flex items-center gap-2.5 text-slate-200 border-b border-slate-800/40 pb-3">
                <BookOpen className="w-5 h-5 text-cyan-400" /> 
                Metadata Providers Matches ({mutation.data.candidates?.length || 0})
              </h2>
              
              {mutation.data.candidates?.length > 0 ? (
                <div className="grid grid-cols-1 gap-3.5">
                  {mutation.data.candidates.map((c: any, i: number) => (
                    <div key={i} className="p-4 bg-slate-950/30 border border-slate-900 rounded-xl flex flex-col sm:flex-row sm:items-center justify-between gap-4">
                      <div>
                        <p className="font-bold text-slate-200 text-sm">
                          {c.metadata.title}
                          <span className="text-slate-400 text-xs font-normal ml-2">by {c.metadata.authors?.join(', ')}</span>
                        </p>
                        {c.metadata.identifiers?.isbn && (
                          <p className="text-[10px] text-slate-500 font-mono mt-1">ISBN: {c.metadata.identifiers.isbn}</p>
                        )}
                      </div>
                      <span className="text-[9px] px-2.5 py-1 bg-cyan-500/10 text-cyan-400 border border-cyan-500/20 rounded-full font-bold uppercase tracking-wider shrink-0 w-fit">
                        {c.provider}
                      </span>
                    </div>
                  ))}
                </div>
              ) : (
                <p className="text-sm text-slate-500 italic py-4">No lookup results retrieved from enabled providers.</p>
              )}
            </div>
          )}

          {/* JSON Collapse */}
          <div className="pt-4 border-t border-slate-800/50">
            <button 
              onClick={() => setShowJson(!showJson)}
              className="text-xs font-bold font-mono uppercase tracking-wider text-purple-400 hover:text-purple-300 transition-colors focus:outline-none flex items-center gap-2 cursor-pointer"
            >
              <Layers className="w-4 h-4" />
              <span>{showJson ? 'Hide' : 'Show'} Full Resolution Evidence JSON</span>
              {showJson ? <ChevronUp className="w-4 h-4 text-purple-500" /> : <ChevronDown className="w-4 h-4 text-purple-500" />}
            </button>
            {showJson && (
              <pre className="mt-4 p-5 bg-slate-950 text-slate-300 border border-slate-900 rounded-2xl overflow-x-auto text-xs font-mono shadow-inner max-h-[500px] overflow-y-auto">
                {JSON.stringify(mutation.data, null, 2)}
              </pre>
            )}
          </div>
        </div>
      )}

      <FileBrowserModal 
        isOpen={isBrowserOpen} 
        onClose={() => setIsBrowserOpen(false)} 
        onSelect={(newPath) => setPath(newPath)} 
      />
    </div>
  )
}

// Inline Loader implementation
function Loader2({ className }: { className?: string }) {
  return (
    <svg className={clsx("animate-spin", className)} xmlns="http://www.w3.org/2000/svg" fill="none" viewBox="0 0 24 24">
      <circle className="opacity-25" cx="12" cy="12" r="10" stroke="currentColor" strokeWidth="4"></circle>
      <path className="opacity-75" fill="currentColor" d="M4 12a8 8 0 018-8V0C5.373 0 0 5.373 0 12h4zm2 5.291A7.962 7.962 0 014 12H0c0 3.042 1.135 5.824 3 7.938l3-2.647z"></path>
    </svg>
  )
}
